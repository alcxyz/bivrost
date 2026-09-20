package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateSettings(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", t.TempDir())
	return root
}

func writeSettings(t *testing.T, root, content string) {
	t.Helper()
	directory := filepath.Join(root, "bivrost")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "settings.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPromptSettingsDefaults(t *testing.T) {
	root := isolateSettings(t)
	for _, content := range []string{"", "{}", `{"prompt":{}}`, `{"prompt":{"colour":"cyan"}}`} {
		if content != "" {
			writeSettings(t, root, content)
		}
		got, err := loadPromptSettings()
		if err != nil || got != defaultPromptSettings() {
			t.Fatalf("defaults: got %+v, error %v", got, err)
		}
	}
}

func TestLoadPromptSettingsOverrides(t *testing.T) {
	root := isolateSettings(t)
	writeSettings(t, root, `{"prompt":{"enabled":false,"label":"[{environment}] {cluster}","colour":"none","placement":"prefix"}}`)
	got, err := loadPromptSettings()
	want := promptSettings{false, "[{environment}] {cluster}", "none", "prefix"}
	if err != nil || got != want {
		t.Fatalf("got %+v, error %v; want %+v", got, err, want)
	}
}

func TestLoadPromptSettingsRejectsInvalid(t *testing.T) {
	cases := []string{
		``, `null`, `[]`, `{"prompt":null}`, `{"prompt":[]}`,
		`{"hook":"SECRET_MARKER"}`, `{"prompt":{"hook":"SECRET_MARKER"}}`,
		`{"prompt":{"enabled":"SECRET_MARKER"}}`,
		`{"prompt":{"colour":"SECRET_MARKER"}}`,
		`{"prompt":{"placement":"SECRET_MARKER"}}`,
		`{"prompt":{"label":"{SECRET_MARKER}"}}`,
		`{"prompt":{"label":"{{environment}}"}}`,
		`{"prompt":{"label":"bad}"}}`,
		`{"prompt":{"label":""}}`,
		`{"prompt":{"label":"   "}}`,
		`{"prompt":{"label":"line\nSECRET_MARKER"}}`,
		`{"prompt":{"label":"$(SECRET_MARKER)"}}`,
		"{\"prompt\":{\"label\":\"`SECRET_MARKER`\"}}",
		`{"prompt":{"label":"\\SECRET_MARKER"}}`,
		`{"prompt":{"label":"%SECRET_MARKER"}}`,
		`{"prompt":{"label":"!SECRET_MARKER"}}`,
		`{"prompt":{"label":"非SECRET_MARKER"}}`,
		`{} {"SECRET_MARKER":true}`, `{} trailing SECRET_MARKER`,
		strings.Repeat(" ", maxSettingsBytes+1),
	}
	for _, content := range cases {
		t.Run(content[:min(len(content), 70)], func(t *testing.T) {
			root := isolateSettings(t)
			writeSettings(t, root, content)
			_, err := loadPromptSettings()
			if err == nil {
				t.Fatal("expected invalid settings to fail")
			}
			if strings.Contains(err.Error(), "SECRET_MARKER") {
				t.Fatal("error exposed configuration contents")
			}
		})
	}
}

func TestLoadPromptSettingsPath(t *testing.T) {
	t.Run("relative XDG rejected", func(t *testing.T) {
		isolateSettings(t)
		t.Setenv("XDG_CONFIG_HOME", "relative")
		if _, err := loadPromptSettings(); err == nil {
			t.Fatal("expected relative XDG path to fail")
		}
	})
	t.Run("native fallback", func(t *testing.T) {
		isolateSettings(t)
		t.Setenv("XDG_CONFIG_HOME", "")
		root, err := os.UserConfigDir()
		if err != nil {
			t.Fatal(err)
		}
		writeSettings(t, root, `{"prompt":{"colour":"red"}}`)
		got, err := loadPromptSettings()
		if err != nil || got.Colour != "red" {
			t.Fatalf("native fallback: got %+v, error %v", got, err)
		}
	})
}

func TestValidatePromptSettingsColoursAndPlacement(t *testing.T) {
	for _, colour := range []string{"cyan", "yellow", "red", "green", "none"} {
		for _, placement := range []string{"prefix", "suffix"} {
			settings := defaultPromptSettings()
			settings.Colour = colour
			settings.Placement = placement
			if err := validatePromptSettings(settings); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestConfigInitCreatesLoadableDefaultsAndPreservesExistingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := run([]string{"config", "init"}); err != nil {
		t.Fatal(err)
	}
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadPromptSettings()
	if err != nil || got != defaultPromptSettings() {
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
	if err := run([]string{"config", "init"}); err == nil {
		t.Fatal("existing file overwritten")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(contents) {
		t.Fatal("existing settings changed")
	}
}

func TestConfigInitRejectsExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"config"}, {"config", "other"}, {"config", "init", "--force"}, {"config", "init", "extra"}} {
		if _, err := parseCommand(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestLoadUserSettingsRejectsLegacyEngineWithoutChangingFile(t *testing.T) {
	for _, value := range []string{`"docker"`, `"podman"`, `"SECRET_MARKER"`, `""`, `null`, `true`} {
		t.Run(value, func(t *testing.T) {
			root := isolateSettings(t)
			content := `{"container_engine":` + value + `,"prompt":{"colour":"red"}}`
			writeSettings(t, root, content)
			_, err := loadUserSettings()
			if err == nil || !strings.Contains(err.Error(), "remove container_engine from Bivrost settings.json") {
				t.Fatalf("loadUserSettings() error = %v; want migration instruction", err)
			}
			if strings.Contains(err.Error(), "SECRET_MARKER") {
				t.Fatal("error exposed setting value")
			}
			after, err := os.ReadFile(filepath.Join(root, "bivrost", "settings.json"))
			if err != nil || string(after) != content {
				t.Fatalf("settings were changed: %v", err)
			}
		})
	}
}
