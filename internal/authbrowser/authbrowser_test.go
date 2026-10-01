package authbrowser

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testAddress = "https://login.example/authorize?client_id=x&state=SENSITIVE_STATE"

func environment(cmd *exec.Cmd) map[string][]string {
	values := map[string][]string{}
	for _, entry := range cmd.Env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = append(values[key], value)
	}
	return values
}

func TestRefuseRoutesBrowserToBivrost(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("az")
	cmd.Env = []string{"PATH=/bin", "BROWSER=/usr/bin/microsoft-edge", modeVariable + "=launch", signalVariable + "=/stale"}
	if err := Refuse(cmd, "/session/browser-request"); err != nil {
		t.Fatal(err)
	}
	got := environment(cmd)
	for key, want := range map[string]string{"PATH": "/bin", "BROWSER": executable, modeVariable: modeRefuse, signalVariable: "/session/browser-request"} {
		if len(got[key]) != 1 || got[key][0] != want {
			t.Errorf("%s = %q, want exactly %q", key, got[key], want)
		}
	}
}

func TestLaunchClearsRefusalSignal(t *testing.T) {
	cmd := exec.Command("az")
	cmd.Env = []string{"BROWSER=firefox", signalVariable + "=/stale"}
	if err := Launch(cmd); err != nil {
		t.Fatal(err)
	}
	got := environment(cmd)
	if len(got[modeVariable]) != 1 || got[modeVariable][0] != modeLaunch {
		t.Fatalf("mode = %q, want launch", got[modeVariable])
	}
	if _, ok := got[signalVariable]; ok {
		t.Fatalf("launch kept refusal signal: %q", cmd.Env)
	}
	if len(got["BROWSER"]) != 1 || got["BROWSER"][0] == "firefox" {
		t.Fatalf("BROWSER = %q, want Bivrost executable", got["BROWSER"])
	}
}

func TestRouteInheritsProcessEnvironment(t *testing.T) {
	t.Setenv("BIVROST_ROUTE_TEST", "kept")
	cmd := exec.Command("az")
	if err := Refuse(cmd, ""); err != nil {
		t.Fatal(err)
	}
	got := environment(cmd)
	if len(got["BIVROST_ROUTE_TEST"]) != 1 || got["BIVROST_ROUTE_TEST"][0] != "kept" {
		t.Fatalf("inherited environment was not preserved")
	}
	if _, ok := got[signalVariable]; ok {
		t.Fatal("empty signal must not be exported")
	}
}

func TestActiveRequiresBrowserModeAndOneAddress(t *testing.T) {
	t.Setenv(modeVariable, "")
	if Active([]string{testAddress}) {
		t.Fatal("Active() without browser mode")
	}
	t.Setenv(modeVariable, modeRefuse)
	for _, args := range [][]string{{}, {"connect"}, {"-test.run=Helper"}, {testAddress, "extra"}, {"file:///etc/passwd"}} {
		if Active(args) {
			t.Errorf("Active(%q) = true", args)
		}
	}
	if !Active([]string{testAddress}) {
		t.Fatal("Active() rejected a sign-in address")
	}
}

func TestRunRefusesQuietlyAndSignals(t *testing.T) {
	signal := filepath.Join(t.TempDir(), "browser-request")
	t.Setenv(modeVariable, modeRefuse)
	t.Setenv(signalVariable, signal)
	var stderr bytes.Buffer
	if status := Run([]string{testAddress}, &stderr); status != 0 {
		t.Fatalf("Run() = %d, want 0 so webbrowser does not try another browser", status)
	}
	if !Requested(signal) {
		t.Fatal("refused request did not create its signal")
	}
	if stderr.Len() != 0 {
		t.Fatalf("refusal wrote output: %q", stderr.String())
	}
	// A second request from the same command still succeeds.
	if status := Run([]string{testAddress}, &stderr); status != 0 || stderr.Len() != 0 {
		t.Fatalf("repeated Run() = %d, %q", status, stderr.String())
	}
}

func TestRunLaunchReportsManualFallback(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Setenv(modeVariable, modeLaunch)
	writeBrowserSettings(t, root, `{"authentication_browser":{"executable":"`+filepath.ToSlash(filepath.Join(root, "missing-browser"))+`"}}`)
	var stderr bytes.Buffer
	if status := Run([]string{testAddress}, &stderr); status != 0 {
		t.Fatalf("Run() = %d, want 0", status)
	}
	if !strings.Contains(stderr.String(), "could not open the configured authentication browser") || !strings.Contains(stderr.String(), testAddress) || !strings.Contains(stderr.String(), "--device-code") {
		t.Fatalf("manual fallback = %q", stderr.String())
	}
}

func TestRunLaunchRejectsInvalidAddress(t *testing.T) {
	t.Setenv(modeVariable, modeLaunch)
	for _, address := range []string{"file:///tmp/x", "https://", "https://login.example/\x1b[31m", "javascript:alert(1)"} {
		var stderr bytes.Buffer
		if status := Run([]string{address}, &stderr); status != 0 {
			t.Fatalf("Run(%q) = %d", address, status)
		}
		if !strings.Contains(stderr.String(), "refused an invalid") || strings.Contains(stderr.String(), address) {
			t.Fatalf("Run(%q) output = %q", address, stderr.String())
		}
	}
}

func writeBrowserSettings(t *testing.T, root, content string) {
	t.Helper()
	directory := filepath.Join(root, "bivrost")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "settings.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
