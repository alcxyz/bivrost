package azure

import (
	"reflect"
	"strings"
	"testing"
)

func TestTerraformBackendValidation(t *testing.T) {
	t.Parallel()
	valid := TerraformBackend{Subscription: "Example Platform", Account: "examplestate123", Container: "tfstate-prod"}
	if err := ValidateTerraformBackend(valid); err != nil {
		t.Fatal(err)
	}
	for _, target := range []TerraformBackend{
		{Account: valid.Account, Container: valid.Container},
		{Subscription: "-other", Account: valid.Account, Container: valid.Container},
		{Subscription: "sub\nother", Account: valid.Account, Container: valid.Container},
		{Subscription: "sub\u202eother", Account: valid.Account, Container: valid.Container},
		{Subscription: valid.Subscription, Account: "ExampleState", Container: valid.Container},
		{Subscription: valid.Subscription, Account: "ab", Container: valid.Container},
		{Subscription: valid.Subscription, Account: valid.Account, Container: "ab"},
		{Subscription: valid.Subscription, Account: valid.Account, Container: "bad--name"},
		{Subscription: valid.Subscription, Account: valid.Account, Container: strings.Repeat("a", 64)},
	} {
		if err := ValidateTerraformBackend(target); err == nil {
			t.Errorf("ValidateTerraformBackend(%+v) accepted invalid target", target)
		}
	}
}

func TestTerraformBackendArgumentsAreMetadataOnlyAndUseLogin(t *testing.T) {
	t.Parallel()
	target := TerraformBackend{Subscription: "sub", Account: "examplestate", Container: "tfstate"}
	endpoint := "https://examplestate.blob.core.windows.net"
	want := []string{
		"storage", "container", "show",
		"--subscription", "sub",
		"--account-name", "examplestate",
		"--name", "tfstate",
		"--blob-endpoint", endpoint,
		"--auth-mode", "login",
		"--output", "none",
		"--only-show-errors",
	}
	if got := TerraformBackendProbeArguments(target, endpoint); !reflect.DeepEqual(got, want) {
		t.Fatalf("TerraformBackendProbeArguments() = %#v, want %#v", got, want)
	}
	joined := " " + strings.Join(want, " ") + " "
	for _, forbidden := range []string{" blob list ", " blob show ", " container list ", " account keys ", " download ", " lease ", " terraform ", " init ", " lock ", "--account-key", "--sas-token", "--connection-string"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("metadata probe contains forbidden operation %q: %s", forbidden, joined)
		}
	}
	if got, want := TerraformCloudArguments(), []string{"cloud", "show", "--query", "[name,suffixes.storageEndpoint]", "--output", "json", "--only-show-errors"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("TerraformCloudArguments() = %#v, want %#v", got, want)
	}
}

func TestTerraformProbeEnvironmentRemovesStorageCredentialOverrides(t *testing.T) {
	t.Parallel()
	environment := []string{
		"PATH=/bin", "HTTPS_PROXY=http://127.0.0.1:18080",
		"AZURE_STORAGE_KEY=secret", "azure_storage_sas_token=secret",
		"AZURE_STORAGE_CONNECTION_STRING=secret", "AZURE_STORAGE_ACCOUNT=other",
		"AZURE_STORAGE_AUTH_MODE=key", "AZURE_STORAGE_SERVICE_ENDPOINT=https://untrusted.example",
		"AZURE_STORAGE_FUTURE_CREDENTIAL=secret",
	}
	want := []string{"PATH=/bin", "HTTPS_PROXY=http://127.0.0.1:18080"}
	if got := withoutStorageCredentials(environment); !reflect.DeepEqual(got, want) {
		t.Fatalf("withoutStorageCredentials() = %q, want %q", got, want)
	}
}

func TestTerraformProbeAcceptsOnlyDerivedSupportedEndpoints(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{
		"https://examplestate.blob.core.windows.net",
		"https://examplestate.blob.core.usgovcloudapi.net",
		"https://examplestate.blob.core.chinacloudapi.cn",
	} {
		if !validTerraformBlobEndpoint("examplestate", endpoint) {
			t.Errorf("supported endpoint rejected: %s", endpoint)
		}
	}
	for _, endpoint := range []string{
		"http://examplestate.blob.core.windows.net",
		"https://other.blob.core.windows.net",
		"https://examplestate.blob.untrusted.example",
		"https://examplestate.blob.core.windows.net/path",
	} {
		if validTerraformBlobEndpoint("examplestate", endpoint) {
			t.Errorf("unsafe endpoint accepted: %s", endpoint)
		}
	}
}
