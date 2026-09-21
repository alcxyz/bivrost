package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunValidatesConfigurationBeforeDispatch(t *testing.T) {
	t.Parallel()
	invalidACR := filepath.Join(t.TempDir(), "invalid-acr.json")
	if err := os.WriteFile(invalidACR, []byte(`{"registry":"not.valid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, subcommand := range []string{"proxy", "connect", "login", "doctor"} {
		err := Run([]string{"acr", subcommand, "--config", invalidACR}, "bivrost dev")
		if err == nil || !strings.Contains(err.Error(), "registry must be") {
			t.Errorf("run(acr %s) error = %v, want ACR validation error", subcommand, err)
		}
	}

	invalidConnection := filepath.Join(t.TempDir(), "invalid-connection.json")
	if err := os.WriteFile(invalidConnection, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"ssh", "--config", invalidConnection}, "bivrost dev"); err == nil || !strings.Contains(err.Error(), "fill in subscription") {
		t.Fatalf("run(ssh) error = %v, want connection validation error", err)
	}
}

func TestSwitchRequiresManagedShell(t *testing.T) {
	t.Setenv("BIVROST_SESSION", "")
	t.Setenv("BIVROST_SWITCH_ALLOWED", "")
	t.Setenv("BIVROST_CONTROL_FILE", "")
	if err := Run([]string{"switch", "-e", "example"}, "bivrost dev"); err == nil || !strings.Contains(err.Error(), "active Bivrost") {
		t.Fatalf("unmanaged switch error = %v", err)
	}
	t.Setenv("BIVROST_SESSION", "1")
	if err := Run([]string{"switch", "-e", "example"}, "bivrost dev"); err == nil || !strings.Contains(err.Error(), "active Bivrost") {
		t.Fatalf("direct binary switch without shell handshake error = %v", err)
	}
}
