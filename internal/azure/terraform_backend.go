package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	terraformCloudTimeout        = 10 * time.Second
	terraformProbeTimeout        = 30 * time.Second
	maxCloudSuffixOutput         = 256
	maxTerraformProbeErrorOutput = 8 * 1024
)

const genericTerraformProbeGuidance = "the named container properties could not be verified; check Azure login, container metadata read permission, target names, active cloud, and network connectivity"

type terraformProbeFailure uint8

const (
	terraformProbeFailureUnknown terraformProbeFailure = iota
	terraformProbeFailureCLIUnavailable
	terraformProbeFailureTimeout
	terraformProbeFailureLogin
	terraformProbeFailureNetwork
)

type terraformProbeError struct {
	failure terraformProbeFailure
}

func (e *terraformProbeError) Error() string {
	return TerraformProbeGuidance(e)
}

// TerraformProbeGuidance returns fixed guidance for a backend probe error. It
// never includes subprocess output or the text of an arbitrary error.
func TerraformProbeGuidance(err error) string {
	var probeError *terraformProbeError
	if !errors.As(err, &probeError) {
		return genericTerraformProbeGuidance
	}
	switch probeError.failure {
	case terraformProbeFailureCLIUnavailable:
		return "Azure CLI is unavailable; install it, then retry"
	case terraformProbeFailureTimeout:
		return "the metadata request timed out; check the network path and retry"
	case terraformProbeFailureLogin:
		return "Azure CLI login is required; run bivrost login or az login, then retry"
	case terraformProbeFailureNetwork:
		return "a network request failed while checking backend metadata; check connectivity, proxy settings, and the private route, then retry"
	default:
		return genericTerraformProbeGuidance
	}
}

var (
	storageAccountPattern = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
	containerPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	supportedStorageCloud = map[string]string{
		"AzureCloud":        "core.windows.net",
		"AzureUSGovernment": "core.usgovcloudapi.net",
		"AzureChinaCloud":   "core.chinacloudapi.cn",
	}
)

// TerraformBackend identifies only the Azure container that a project may use
// as its backend. It deliberately has no state key or workspace field.
type TerraformBackend struct {
	Subscription string
	Account      string
	Container    string
}

// ValidateTerraformBackend validates identifiers before passing them to Azure CLI.
func ValidateTerraformBackend(target TerraformBackend) error {
	if !validAzureArgument(target.Subscription) {
		return errors.New("--subscription must be a non-empty Azure subscription name or ID without control or format characters")
	}
	if !storageAccountPattern.MatchString(target.Account) {
		return errors.New("--account must be a lowercase Azure storage account name containing 3 to 24 letters or digits")
	}
	if !containerPattern.MatchString(target.Container) || strings.Contains(target.Container, "--") {
		return errors.New("--container must be a lowercase Azure container name containing 3 to 63 letters, digits, or single hyphens")
	}
	return nil
}

func validAzureArgument(value string) bool {
	if value == "" || len(value) > 256 || strings.HasPrefix(value, "-") || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	return true
}

// TerraformCloudArguments returns a read-only query for the active Azure CLI
// cloud's storage suffix. It does not change the selected cloud or subscription.
func TerraformCloudArguments() []string {
	return []string{"cloud", "show", "--query", "[name,suffixes.storageEndpoint]", "--output", "json", "--only-show-errors"}
}

// TerraformBackendProbeArguments returns a container-properties request using
// the signed-in Azure CLI identity. Output is disabled because container
// properties can contain user-defined metadata.
func TerraformBackendProbeArguments(target TerraformBackend, endpoint string) []string {
	return []string{
		"storage", "container", "show",
		"--subscription", target.Subscription,
		"--account-name", target.Account,
		"--name", target.Container,
		"--blob-endpoint", endpoint,
		"--auth-mode", "login",
		"--output", "none",
		"--only-show-errors",
	}
}

// TerraformBlobEndpoint resolves the Blob endpoint from the active, supported
// Azure CLI cloud. Custom clouds are rejected so login tokens cannot be sent to
// an endpoint supplied by mutable custom-cloud configuration.
func TerraformBlobEndpoint(ctx context.Context, account string, environment []string) (string, error) {
	if !storageAccountPattern.MatchString(account) {
		return "", errors.New("invalid Azure storage account name")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cloudCtx, cancel := context.WithTimeout(ctx, terraformCloudTimeout)
	defer cancel()
	cmd, err := Command(cloudCtx, TerraformCloudArguments()...)
	if err != nil {
		return "", errors.New("cannot run Azure CLI; install it, then retry")
	}
	var output boundedBuffer
	output.limit = maxCloudSuffixOutput
	cmd.Env = withoutStorageCredentials(environment)
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = subscriptionWaitDelay
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(cloudCtx.Err(), context.DeadlineExceeded) {
			return "", errors.New("Azure cloud discovery timed out; check the local Azure CLI configuration and retry")
		}
		var executableError *exec.Error
		if errors.As(err, &executableError) {
			return "", errors.New("cannot run Azure CLI; install it, then retry")
		}
		return "", errors.New("cannot read the active Azure CLI cloud; check the local Azure CLI configuration and retry")
	}
	if output.exceeded {
		return "", errors.New("Azure CLI returned an invalid storage endpoint for the active cloud")
	}
	cloud, suffix, err := decodeTerraformCloud(output.data.Bytes())
	if err != nil || supportedStorageCloud[cloud] != suffix {
		return "", errors.New("the active Azure CLI cloud has an unsupported storage endpoint; this diagnostic supports Azure public, US Government, and China clouds")
	}
	return "https://" + account + ".blob." + suffix, nil
}

func decodeTerraformCloud(data []byte) (string, string, error) {
	var fields []string
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&fields); err != nil {
		return "", "", err
	}
	if err := ensureJSONEnd(decoder); err != nil || len(fields) != 2 || fields[0] == "" || fields[1] == "" {
		return "", "", errors.New("invalid Azure cloud metadata")
	}
	return fields[0], fields[1], nil
}

// ProbeTerraformBackend requests only the named container's properties. It
// does not enumerate containers or blobs, read state, or acquire a state lock.
func ProbeTerraformBackend(ctx context.Context, target TerraformBackend, endpoint string, environment []string) error {
	if err := ValidateTerraformBackend(target); err != nil {
		return err
	}
	if !validTerraformBlobEndpoint(target.Account, endpoint) {
		return errors.New("invalid Azure Blob endpoint")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, terraformProbeTimeout)
	defer cancel()
	cmd, err := Command(probeCtx, TerraformBackendProbeArguments(target, endpoint)...)
	if err != nil {
		return &terraformProbeError{failure: terraformProbeFailureCLIUnavailable}
	}
	var stderr boundedBuffer
	stderr.limit = maxTerraformProbeErrorOutput
	cmd.Env = withoutStorageCredentials(environment)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	cmd.WaitDelay = subscriptionWaitDelay
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			return &terraformProbeError{failure: terraformProbeFailureTimeout}
		}
		var executableError *exec.Error
		if errors.As(err, &executableError) {
			return &terraformProbeError{failure: terraformProbeFailureCLIUnavailable}
		}
		return &terraformProbeError{failure: classifyTerraformProbeStderr(stderr.data.Bytes(), stderr.exceeded)}
	}
	return nil
}

func classifyTerraformProbeStderr(stderr []byte, truncated bool) terraformProbeFailure {
	if truncated {
		return terraformProbeFailureUnknown
	}
	// Azure CLI owns this exact login message:
	// https://github.com/Azure/azure-cli/blob/dev/src/azure-cli-core/azure/cli/core/_profile.py
	if bytes.Contains(stderr, []byte("Please run 'az login' to setup account.")) {
		return terraformProbeFailureLogin
	}
	// Azure Core defines ServiceRequestError as a failure before a request reaches
	// the service; Azure CLI reports the qualified requests/urllib3 network types:
	// https://github.com/Azure/azure-sdk-for-python/blob/main/sdk/core/azure-core/README.md#azure-core-library-exceptions
	// https://github.com/Azure/azure-cli/issues/33691
	for _, token := range [][]byte{
		[]byte("azure.core.exceptions.ServiceRequestError:"),
		[]byte("urllib3.exceptions.NameResolutionError:"),
		[]byte("urllib3.exceptions.NewConnectionError:"),
		[]byte("urllib3.exceptions.ProxyError:"),
		[]byte("requests.exceptions.ConnectionError:"),
	} {
		if bytes.Contains(stderr, token) {
			return terraformProbeFailureNetwork
		}
	}
	return terraformProbeFailureUnknown
}

func validTerraformBlobEndpoint(account, endpoint string) bool {
	for _, suffix := range supportedStorageCloud {
		if endpoint == "https://"+account+".blob."+suffix {
			return true
		}
	}
	return false
}

func withoutStorageCredentials(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "AZURE_STORAGE_") {
			continue
		}
		result = append(result, entry)
	}
	return result
}
