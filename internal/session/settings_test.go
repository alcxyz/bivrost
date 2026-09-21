package session

import (
	"os"
	"strings"
	"testing"

	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
)

func isolateSettings(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", t.TempDir())
	return root
}

func TestConfigInitCreatesLoadableDefaultsAndPreservesExistingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Run([]string{"config", "init"}, "bivrost dev"); err != nil {
		t.Fatal(err)
	}
	path, err := profile.SettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	got, err := profile.LoadPromptSettings()
	if err != nil || got != profile.DefaultPromptSettings() {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	initial, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(initial), "container_engine") {
		t.Fatalf("default settings include legacy engine selection: %v", err)
	}
	contents := []byte(`{"prompt":{"colour":"red"}}`)
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"config", "init"}, "bivrost dev"); err == nil {
		t.Fatal("existing file overwritten")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(contents) {
		t.Fatal("existing settings changed")
	}
}

func TestConfigInitRejectsExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"config"}, {"config", "other"}, {"config", "init", "--force"}, {"config", "init", "extra"}} {
		if _, err := cli.Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
