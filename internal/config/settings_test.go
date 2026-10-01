package config

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
		got, err := LoadPromptSettings()
		if err != nil || got != DefaultPromptSettings() {
			t.Fatalf("defaults: got %+v, error %v", got, err)
		}
	}
}

func TestLoadPromptSettingsOverrides(t *testing.T) {
	root := isolateSettings(t)
	writeSettings(t, root, `{"prompt":{"enabled":false,"label":"[{environment}] {cluster}","colour":"none","placement":"prefix"}}`)
	got, err := LoadPromptSettings()
	want := PromptSettings{false, "[{environment}] {cluster}", "none", "prefix"}
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
			_, err := LoadPromptSettings()
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
		if _, err := LoadPromptSettings(); err == nil {
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
		got, err := LoadPromptSettings()
		if err != nil || got.Colour != "red" {
			t.Fatalf("native fallback: got %+v, error %v", got, err)
		}
	})
}

func TestValidatePromptSettingsColoursAndPlacement(t *testing.T) {
	for _, colour := range []string{"cyan", "yellow", "red", "green", "none"} {
		for _, placement := range []string{"prefix", "suffix"} {
			settings := DefaultPromptSettings()
			settings.Colour = colour
			settings.Placement = placement
			if err := ValidatePromptSettings(settings); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestLoadUserSettingsRejectsLegacyEngineWithoutChangingFile(t *testing.T) {
	for _, value := range []string{`"docker"`, `"podman"`, `"SECRET_MARKER"`, `""`, `null`, `true`} {
		t.Run(value, func(t *testing.T) {
			root := isolateSettings(t)
			content := `{"container_engine":` + value + `,"prompt":{"colour":"red"}}`
			writeSettings(t, root, content)
			_, err := LoadUserSettings()
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

func TestLoadAuthenticationBrowser(t *testing.T) {
	root := isolateSettings(t)
	if got, err := LoadAuthenticationBrowser(); err != nil || got != nil {
		t.Fatalf("LoadAuthenticationBrowser() without settings = %v, %v; want nil", got, err)
	}
	writeSettings(t, root, `{"authentication_browser":{"executable":"/opt/browser/bin/browser","arguments":["--profile","Work Profile","{url}","--new-window"]}}`)
	got, err := LoadAuthenticationBrowser()
	if err != nil || got == nil {
		t.Fatalf("LoadAuthenticationBrowser() = %v, %v", got, err)
	}
	executable, args := got.Command("https://login.example/authorize?a=1&b=$(x)")
	want := []string{"--profile", "Work Profile", "https://login.example/authorize?a=1&b=$(x)", "--new-window"}
	if executable != "/opt/browser/bin/browser" || strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Command() = %q %q, want %q", executable, args, want)
	}
	if prompt, err := LoadPromptSettings(); err != nil || prompt != DefaultPromptSettings() {
		t.Fatalf("prompt settings changed with browser settings: %v, %v", prompt, err)
	}
}

func TestAuthenticationBrowserAppendsAddressWithoutPlaceholder(t *testing.T) {
	executable, args := AuthenticationBrowser{Executable: "browser", Arguments: []string{"-P", "work"}}.Command("https://login.example/")
	if executable != "browser" || strings.Join(args, " ") != "-P work https://login.example/" {
		t.Fatalf("Command() = %q %q", executable, args)
	}
	_, args = AuthenticationBrowser{Executable: "browser"}.Command("https://login.example/")
	if len(args) != 1 || args[0] != "https://login.example/" {
		t.Fatalf("Command() without arguments = %q", args)
	}
}

func TestLoadAuthenticationBrowserRejectsInvalid(t *testing.T) {
	cases := []string{
		`{"authentication_browser":null}`,
		`{"authentication_browser":[]}`,
		`{"authentication_browser":{}}`,
		`{"authentication_browser":{"executable":"   "}}`,
		`{"authentication_browser":{"executable":"browser","SECRET_MARKER":1}}`,
		`{"authentication_browser":{"executable":"browser\nSECRET_MARKER"}}`,
		`{"authentication_browser":{"executable":"C:\\tools\\SECRET_MARKER.cmd"}}`,
		`{"authentication_browser":{"executable":"SECRET_MARKER.BAT"}}`,
		`{"authentication_browser":{"executable":"browser","arguments":"SECRET_MARKER"}}`,
		`{"authentication_browser":{"executable":"browser","arguments":["--app={url}"]}}`,
		`{"authentication_browser":{"executable":"browser","arguments":["{url}","{url}"]}}`,
		`{"authentication_browser":{"executable":"browser","arguments":["SECRET_MARKER\u0000"]}}`,
		`{"authentication_browser":{"executable":"browser","arguments":["` + strings.Repeat("a", 1025) + `"]}}`,
		`{"authentication_browser":{"executable":"browser","arguments":[` + strings.Repeat(`"a",`, 32) + `"a"]}}`,
	}
	for _, content := range cases {
		t.Run(content[:min(len(content), 70)], func(t *testing.T) {
			root := isolateSettings(t)
			writeSettings(t, root, content)
			_, err := LoadAuthenticationBrowser()
			if err == nil {
				t.Fatal("expected invalid settings to fail")
			}
			if strings.Contains(err.Error(), "SECRET_MARKER") {
				t.Fatal("error exposed configuration contents")
			}
		})
	}
}

func TestLoadInteractiveConnect(t *testing.T) {
	root := isolateSettings(t)
	if got, err := LoadInteractiveConnect(); err != nil || !got {
		t.Fatalf("LoadInteractiveConnect() default = %v, %v; want true", got, err)
	}
	for content, want := range map[string]bool{
		`{"interactive_connect":false}`: false,
		`{"interactive_connect":true}`:  true,
		`{"prompt":{"enabled":false}}`:  true,
	} {
		writeSettings(t, root, content)
		if got, err := LoadInteractiveConnect(); err != nil || got != want {
			t.Errorf("%s: LoadInteractiveConnect() = %v, %v; want %v", content, got, err, want)
		}
	}
	writeSettings(t, root, `{"interactive_connect":"SECRET_MARKER"}`)
	if _, err := LoadInteractiveConnect(); err == nil || strings.Contains(err.Error(), "SECRET_MARKER") {
		t.Fatalf("invalid interactive_connect error = %v", err)
	}
}
