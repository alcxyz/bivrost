package azure

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCommandRefusesBrowserSignIn(t *testing.T) {
	t.Setenv("BROWSER", "/usr/bin/microsoft-edge")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	guarded, err := Command(context.Background(), "account", "show")
	if err != nil {
		t.Skipf("Azure CLI command unavailable: %v", err)
	}
	if !hasEntry(guarded.Env, "BROWSER="+executable) || !hasEntry(guarded.Env, "BIVROST_AUTH_BROWSER=refuse") || hasEntry(guarded.Env, "BROWSER=/usr/bin/microsoft-edge") {
		t.Fatalf("Command() did not route browser requests to Bivrost")
	}
	interactive, err := InteractiveCommand(context.Background(), "login")
	if err != nil {
		t.Fatal(err)
	}
	if interactive.Env != nil {
		t.Fatalf("InteractiveCommand() changed the inherited environment")
	}
}

func hasEntry(env []string, want string) bool {
	for _, entry := range env {
		if strings.EqualFold(entry, want) {
			return true
		}
	}
	return false
}
