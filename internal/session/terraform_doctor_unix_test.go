//go:build !windows

package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alcxyz/bivrost/internal/cli"
)

func TestTerraformDoctorAuthenticatesSessionAndForcesItsExactPrivateRoute(t *testing.T) {
	directory := t.TempDir()
	azPath := filepath.Join(directory, "az")
	callLog := filepath.Join(directory, "calls")
	script := `#!/bin/sh
for name in AZURE_STORAGE_KEY AZURE_STORAGE_CONNECTION_STRING AZURE_STORAGE_SAS_TOKEN AZURE_STORAGE_ACCOUNT AZURE_STORAGE_AUTH_MODE AZURE_STORAGE_SERVICE_ENDPOINT; do
  eval '[ "${'"$name"'+x}" != x ]' || exit 41
done
printf '%s|%s|%s|%s\n' "$HTTPS_PROXY" "$HTTP_PROXY" "$NO_PROXY" "$ALL_PROXY" >> "$BIVROST_TEST_AZ_CALLS"
if [ "$1 $2" = "cloud show" ]; then
	printf '%s\n' '["AzureCloud","core.windows.net"]'
  exit 0
fi
[ "$1 $2 $3" = "storage container show" ] || exit 42
`
	if err := os.WriteFile(azPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BIVROST_TEST_AZ_CALLS", callLog)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:2")
	t.Setenv("NO_PROXY", "*")
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:3")
	t.Setenv("AZURE_STORAGE_KEY", "secret")
	t.Setenv("AZURE_STORAGE_SAS_TOKEN", "secret")

	c := platformTestConfig(t)
	c.Environment = "connection-environment"
	c.PrivateHosts = []string{"examplestate.blob.core.windows.net"}
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	proxy, err := startProxy(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.close)
	var starts, logins atomic.Int32
	_, _ = doctorTestController(t, c, activationTestServices(t, &starts, &logins))

	command := cli.Command{
		Kind:         cli.TerraformDoctor,
		Subscription: "explicit-backend-subscription",
		Account:      "examplestate",
		Container:    "tfstate-prod",
	}
	var output bytes.Buffer
	if err := runTerraformDoctor(context.Background(), command, &output); err != nil {
		t.Fatal(err)
	}
	wantEnvironment := c.ProxyURL() + "|" + c.ProxyURL() + "|127.0.0.1,localhost|\n"
	data, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != wantEnvironment+wantEnvironment {
		t.Fatalf("Azure commands did not use the authenticated session proxy:\n%s", data)
	}
	text := output.String()
	for _, want := range []string{
		`subscription "explicit-backend-subscription"`,
		`container "tfstate-prod"`,
		`environment "connection-environment", Bastion subscription "subscription"`,
		"[OK] Terraform backend routing configuration:",
		"exact private route",
		"[OK] Terraform backend metadata:",
		"does not prove blob read, write, lease, lock, init, or plan permissions",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Terraform doctor output missing %q:\n%s", want, text)
		}
	}
	if starts.Load() != 0 || logins.Load() != 0 {
		t.Fatal("Terraform doctor activated ACR or performed a registry login")
	}
}

func TestTerraformDoctorRejectsMissingSessionProxyBeforeAzureProbe(t *testing.T) {
	directory := t.TempDir()
	called := filepath.Join(directory, "called")
	azPath := filepath.Join(directory, "az")
	if err := os.WriteFile(azPath, []byte("#!/bin/sh\nprintf called > \"$BIVROST_TEST_CALLED\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("BIVROST_TEST_CALLED", called)
	c := platformTestConfig(t)
	var starts, logins atomic.Int32
	_, _ = doctorTestController(t, c, activationTestServices(t, &starts, &logins))
	command := cli.Command{Kind: cli.TerraformDoctor, Subscription: "sub", Account: "examplestate", Container: "tfstate"}
	var output bytes.Buffer
	err := runTerraformDoctor(context.Background(), command, &output)
	if err == nil || !strings.Contains(err.Error(), "session proxy is unavailable") {
		t.Fatalf("runTerraformDoctor() error = %v", err)
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatal("missing session proxy invoked Azure CLI")
	}
}

func TestTerraformDoctorFailsClosedForStaleSessionMarkers(t *testing.T) {
	directory := t.TempDir()
	called := filepath.Join(directory, "called")
	azPath := filepath.Join(directory, "az")
	if err := os.WriteFile(azPath, []byte("#!/bin/sh\nprintf called > \"$BIVROST_TEST_CALLED\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("BIVROST_TEST_CALLED", called)
	t.Setenv("BIVROST_SESSION", "stale")
	t.Setenv("BIVROST_CONTROL_FILE", "")
	command := cli.Command{Kind: cli.TerraformDoctor, Subscription: "sub", Account: "examplestate", Container: "tfstate"}
	var output bytes.Buffer
	err := runTerraformDoctor(context.Background(), command, &output)
	if err == nil || !strings.Contains(err.Error(), "session markers are incomplete") {
		t.Fatalf("runTerraformDoctor() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("stale session produced output: %s", output.String())
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatal("stale session invoked Azure CLI")
	}
}
