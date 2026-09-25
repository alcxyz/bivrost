//go:build !windows

package azure

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerraformBackendProbeRunsBoundedCredentialFreeCommands(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "calls")
	azPath := filepath.Join(directory, "az")
	script := `#!/bin/sh
{
  printf '%s\n' CALL
  printf '%s\n' "$@"
} >> "$BIVROST_TEST_AZ_CALLS"
for name in AZURE_STORAGE_KEY AZURE_STORAGE_CONNECTION_STRING AZURE_STORAGE_SAS_TOKEN AZURE_STORAGE_ACCOUNT AZURE_STORAGE_AUTH_MODE AZURE_STORAGE_SERVICE_ENDPOINT; do
  eval '[ "${'"$name"'+x}" != x ]' || exit 41
done
if [ "$1 $2" = "cloud show" ]; then
	printf '%s\n' '["AzureCloud","core.windows.net"]'
  exit 0
fi
[ "$1 $2 $3" = "storage container show" ] || exit 42
printf '%s\n' PRIVATE-OUTPUT-MARKER
printf '%s\n' PRIVATE-STDERR-MARKER >&2
`
	if err := os.WriteFile(azPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	environment := append(os.Environ(),
		"BIVROST_TEST_AZ_CALLS="+logPath,
		"AZURE_STORAGE_KEY=secret",
		"AZURE_STORAGE_CONNECTION_STRING=secret",
		"AZURE_STORAGE_SAS_TOKEN=secret",
		"AZURE_STORAGE_ACCOUNT=other",
		"AZURE_STORAGE_AUTH_MODE=key",
		"AZURE_STORAGE_SERVICE_ENDPOINT=https://untrusted.example",
	)
	target := TerraformBackend{Subscription: "sub", Account: "examplestate", Container: "tfstate"}
	endpoint, err := TerraformBlobEndpoint(context.Background(), target.Account, environment)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://examplestate.blob.core.windows.net" {
		t.Fatalf("TerraformBlobEndpoint() = %q", endpoint)
	}
	if err := ProbeTerraformBackend(context.Background(), target, endpoint, environment); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range append(TerraformCloudArguments(), TerraformBackendProbeArguments(target, endpoint)...) {
		if !strings.Contains(text, want+"\n") {
			t.Errorf("Azure call log missing argument %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"secret", "untrusted.example", "PRIVATE"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Azure call log contains forbidden value %q", forbidden)
		}
	}
}

func TestTerraformBackendProbeRejectsUnsupportedCloudAndHidesSubprocessErrors(t *testing.T) {
	directory := t.TempDir()
	azPath := filepath.Join(directory, "az")
	if err := os.WriteFile(azPath, []byte("#!/bin/sh\nprintf '%s\\n' PRIVATE-STDERR-MARKER >&2\nif [ \"$1 $2\" = \"cloud show\" ]; then printf '%s\\n' '[\"CustomCloud\",\"core.windows.net\"]'; exit 0; fi\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	_, err := TerraformBlobEndpoint(context.Background(), "examplestate", os.Environ())
	if err == nil || !strings.Contains(err.Error(), "unsupported storage endpoint") || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("TerraformBlobEndpoint() error = %v", err)
	}

	if err := os.WriteFile(azPath, []byte("#!/bin/sh\nprintf '%s\\n' PRIVATE-STDERR-MARKER >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := TerraformBackend{Subscription: "sub", Account: "examplestate", Container: "tfstate"}
	err = ProbeTerraformBackend(context.Background(), target, "https://examplestate.blob.core.windows.net", os.Environ())
	if err == nil || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("ProbeTerraformBackend() exposed subprocess error: %v", err)
	}
}
