package azure

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	heimdalTokenTimeout      = 15 * time.Second
	heimdalReadTimeout       = 30 * time.Second
	maxHeimdalTokenOutput    = 64 * 1024
	maxHeimdalReadBytes      = 64 * 1024
	heimdalStorageAPIVersion = "2023-11-03"
)

type heimdalReadFailure uint8

const (
	heimdalReadFailureUnknown heimdalReadFailure = iota
	heimdalReadFailureCLIUnavailable
	heimdalReadFailureTokenTimeout
	heimdalReadFailureToken
	heimdalReadFailureInvalidToken
	heimdalReadFailureRequestTimeout
	heimdalReadFailureTransport
	heimdalReadFailureRedirect
	heimdalReadFailureDenied
	heimdalReadFailureMissing
	heimdalReadFailureResponse
	heimdalReadFailureOversize
)

type heimdalReadError struct {
	failure heimdalReadFailure
	cause   error
}

func (e *heimdalReadError) Error() string {
	switch e.failure {
	case heimdalReadFailureCLIUnavailable:
		return "Azure CLI is unavailable; install it, then retry"
	case heimdalReadFailureTokenTimeout:
		return "Azure Storage token acquisition timed out; check Azure login and connectivity, then retry"
	case heimdalReadFailureToken:
		return "Azure Storage token acquisition failed; run bivrost login or az login, check the subscription, and retry"
	case heimdalReadFailureInvalidToken:
		return "Azure CLI returned an invalid Azure Storage token"
	case heimdalReadFailureRequestTimeout:
		return "the Heimdal Blob request timed out; check the session proxy and private route, then retry"
	case heimdalReadFailureTransport:
		return "the Heimdal Blob request failed; check the session proxy, private route, and TLS trust, then retry"
	case heimdalReadFailureRedirect:
		return "the Heimdal Blob request was redirected and was refused"
	case heimdalReadFailureDenied:
		return "Azure denied access to the Heimdal Blob; check Blob data read permission"
	case heimdalReadFailureMissing:
		return "the requested Heimdal Blob does not exist"
	case heimdalReadFailureResponse:
		return "Azure Blob Storage returned an unsuccessful response"
	case heimdalReadFailureOversize:
		return "the Heimdal Blob exceeds the configured size limit"
	default:
		return "the Heimdal Blob could not be read"
	}
}

func (e *heimdalReadError) Unwrap() error {
	return e.cause
}

// ReadHeimdalBlob reads one pointer or immutable revision through the active
// session's loopback HTTP proxy. It holds the response only in memory and does
// not follow redirects or use proxy settings from the process environment.
func ReadHeimdalBlob(ctx context.Context, target TerraformBackend, endpoint, blobName, proxyURL string, environment []string, maxBytes int) ([]byte, error) {
	proxy, err := parseHeimdalProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxy)
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return readHeimdalBlobWithClient(ctx, target, endpoint, blobName, environment, maxBytes, client)
}

func readHeimdalBlobWithClient(ctx context.Context, target TerraformBackend, endpoint, blobName string, environment []string, maxBytes int, client *http.Client) ([]byte, error) {
	if err := ValidateTerraformBackend(target); err != nil {
		return nil, err
	}
	if !validTerraformBlobEndpoint(target.Account, endpoint) {
		return nil, errors.New("invalid Azure Blob endpoint")
	}
	if !validHeimdalReadBlobName(blobName) {
		return nil, errors.New("invalid Heimdal Blob name")
	}
	if maxBytes <= 0 || maxBytes > maxHeimdalReadBytes {
		return nil, errors.New("invalid Heimdal Blob size limit")
	}
	if client == nil {
		return nil, errors.New("invalid Heimdal HTTP client")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	token, err := heimdalStorageToken(ctx, target, endpoint, environment)
	if err != nil {
		return nil, err
	}
	requestURL := endpoint + "/" + target.Container + "/" + blobName
	requestCtx, cancel := context.WithTimeout(ctx, heimdalReadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, errors.New("invalid Heimdal Blob request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("x-ms-version", heimdalStorageAPIVersion)

	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &heimdalReadError{failure: heimdalReadFailureRequestTimeout, cause: context.DeadlineExceeded}
		}
		return nil, &heimdalReadError{failure: heimdalReadFailureTransport}
	}
	defer response.Body.Close()

	if response.Request == nil || response.Request.URL.String() != requestURL || response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return nil, &heimdalReadError{failure: heimdalReadFailureRedirect}
	}
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, &heimdalReadError{failure: heimdalReadFailureDenied}
	case http.StatusNotFound:
		return nil, &heimdalReadError{failure: heimdalReadFailureMissing}
	default:
		return nil, &heimdalReadError{failure: heimdalReadFailureResponse}
	}

	limit := int64(maxBytes)
	if response.ContentLength > limit {
		return nil, &heimdalReadError{failure: heimdalReadFailureOversize}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &heimdalReadError{failure: heimdalReadFailureRequestTimeout, cause: context.DeadlineExceeded}
		}
		return nil, &heimdalReadError{failure: heimdalReadFailureTransport}
	}
	if int64(len(data)) > limit {
		return nil, &heimdalReadError{failure: heimdalReadFailureOversize}
	}
	return data, nil
}

func heimdalStorageToken(ctx context.Context, target TerraformBackend, endpoint string, environment []string) (string, error) {
	tokenCtx, cancel := context.WithTimeout(ctx, heimdalTokenTimeout)
	defer cancel()
	args := []string{
		"account", "get-access-token",
		"--subscription", target.Subscription,
		"--resource", endpoint,
		"--query", "accessToken",
		"--output", "tsv",
		"--only-show-errors",
	}
	cmd, err := Command(tokenCtx, args...)
	if err != nil {
		return "", &heimdalReadError{failure: heimdalReadFailureCLIUnavailable}
	}
	var output boundedBuffer
	output.limit = maxHeimdalTokenOutput
	cmd.Env = withoutStorageCredentials(environment)
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = subscriptionWaitDelay
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(tokenCtx.Err(), context.DeadlineExceeded) {
			return "", &heimdalReadError{failure: heimdalReadFailureTokenTimeout, cause: context.DeadlineExceeded}
		}
		var executableError *exec.Error
		if errors.As(err, &executableError) {
			return "", &heimdalReadError{failure: heimdalReadFailureCLIUnavailable}
		}
		return "", &heimdalReadError{failure: heimdalReadFailureToken}
	}
	if output.exceeded {
		return "", &heimdalReadError{failure: heimdalReadFailureInvalidToken}
	}
	token := strings.TrimSuffix(output.data.String(), "\n")
	token = strings.TrimSuffix(token, "\r")
	if !validHeimdalBearerToken(token) {
		return "", &heimdalReadError{failure: heimdalReadFailureInvalidToken}
	}
	return token, nil
}

func validHeimdalBearerToken(token string) bool {
	if token == "" {
		return false
	}
	for index := 0; index < len(token); index++ {
		character := token[index]
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		switch character {
		case '-', '.', '_', '~', '+', '/', '=':
			continue
		default:
			return false
		}
	}
	return true
}

func validHeimdalReadBlobName(blobName string) bool {
	prefix, found := strings.CutSuffix(blobName, "/current.json")
	if found {
		return validHeimdalPrefix(prefix)
	}
	index := strings.LastIndex(blobName, "/revisions/")
	if index < 0 {
		return false
	}
	prefix, revisionFile := blobName[:index], blobName[index+len("/revisions/"):]
	if !validHeimdalPrefix(prefix) || !strings.HasSuffix(revisionFile, ".json") {
		return false
	}
	revision := strings.TrimSuffix(revisionFile, ".json")
	return heimdalRevisionPattern.MatchString(revision)
}

func parseHeimdalProxyURL(value string) (*url.URL, error) {
	proxy, err := url.Parse(value)
	if err != nil || proxy.Scheme != "http" || proxy.User != nil || proxy.Host == "" || proxy.Port() == "" || proxy.RawQuery != "" || proxy.Fragment != "" || proxy.Path != "" && proxy.Path != "/" {
		return nil, errors.New("invalid Heimdal session proxy URL")
	}
	host := proxy.Hostname()
	if host != "127.0.0.1" && host != "::1" {
		return nil, errors.New("Heimdal session proxy must use a loopback IP address")
	}
	if net.ParseIP(host) == nil {
		return nil, errors.New("invalid Heimdal session proxy URL")
	}
	port, err := strconv.Atoi(proxy.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid Heimdal session proxy URL")
	}
	return proxy, nil
}
