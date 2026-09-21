package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugCLIRecordsConfigurationFailureWithoutItsPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	missing := filepath.Join(t.TempDir(), "SYNTHETIC-CREDENTIAL-IN-PATH.json")
	if err := Run([]string{"connect", "-c", missing}, "bivrost dev"); err == nil {
		t.Fatal("missing configuration accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "bivrost", "logs")); !os.IsNotExist(err) {
		t.Fatal("non-debug invocation created logs")
	}
	if err := Run([]string{"connect", "-c", missing, "-d"}, "bivrost dev"); err == nil {
		t.Fatal("missing configuration accepted")
	}
	data, err := os.ReadFile(filepath.Join(root, "bivrost", "logs", "log-0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SYNTHETIC") || strings.Contains(string(data), missing) {
		t.Fatal("raw command data leaked into log")
	}
	if !strings.Contains(string(data), `"event":"configuration"`) || !strings.Contains(string(data), `"outcome":"failure"`) {
		t.Fatalf("missing diagnostic stage outcome: %s", data)
	}
}
