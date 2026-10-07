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

func TestInteractiveWithoutSignalClearsStaleSignal(t *testing.T) {
	cmd := exec.Command("az")
	cmd.Env = []string{"BROWSER=firefox", signalVariable + "=/stale"}
	if err := Interactive(cmd, ""); err != nil {
		t.Fatal(err)
	}
	got := environment(cmd)
	if len(got[modeVariable]) != 1 || got[modeVariable][0] != modeInteractive {
		t.Fatalf("mode = %q, want interactive", got[modeVariable])
	}
	if _, ok := got[signalVariable]; ok {
		t.Fatalf("interactive route kept a stale signal: %q", cmd.Env)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if want := executable + string(os.PathListSeparator) + "firefox"; len(got["BROWSER"]) != 1 || got["BROWSER"][0] != want {
		t.Fatalf("BROWSER = %q, want %q", got["BROWSER"], want)
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
	t.Setenv(modeVariable, modeInteractive)
	writeBrowserSettings(t, root, `{"authentication_browser":{"executable":"`+filepath.ToSlash(filepath.Join(root, "missing-browser"))+`"}}`)
	var stderr bytes.Buffer
	if status := Run([]string{testAddress}, &stderr); status != 0 {
		t.Fatalf("Run() = %d, want 0", status)
	}
	if !strings.Contains(stderr.String(), "could not open the configured authentication browser") || !strings.Contains(stderr.String(), testAddress) {
		t.Fatalf("manual fallback = %q", stderr.String())
	}
}

func TestRunLaunchRejectsInvalidAddress(t *testing.T) {
	t.Setenv(modeVariable, modeInteractive)
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

func TestInteractiveKeepsUserBrowserAfterBivrost(t *testing.T) {
	separator := string(os.PathListSeparator)
	t.Setenv("BROWSER", "firefox"+separator+"chromium")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// azure.Command refuses first; connection setup then allows interaction.
	cmd := exec.Command("az")
	if err := Refuse(cmd, ""); err != nil {
		t.Fatal(err)
	}
	if got := environment(cmd)["BROWSER"]; len(got) != 1 || got[0] != executable {
		t.Fatalf("refuse BROWSER = %q, want only Bivrost", got)
	}
	if err := Interactive(cmd, ""); err != nil {
		t.Fatal(err)
	}
	want := executable + separator + "firefox" + separator + "chromium"
	if got := environment(cmd)["BROWSER"]; len(got) != 1 || got[0] != want {
		t.Fatalf("interactive BROWSER = %q, want %q", got, want)
	}
}

func TestRunInteractiveWithoutConfigurationDefersToNextBrowser(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	signal := filepath.Join(t.TempDir(), "browser-request")
	t.Setenv(modeVariable, modeInteractive)
	t.Setenv(signalVariable, signal)
	var out bytes.Buffer
	if status := Run([]string{testAddress}, &out); status != 1 {
		t.Fatalf("Run() = %d, want 1 so webbrowser tries the next browser", status)
	}
	if !Requested(signal) || out.Len() != 0 {
		t.Fatalf("signal = %v, output = %q", Requested(signal), out.String())
	}
}

func TestInteractiveDropsEdgeEntriesThatBypassBivrost(t *testing.T) {
	separator := string(os.PathListSeparator)
	t.Setenv("BROWSER", "/usr/bin/microsoft-edge"+separator+"firefox"+separator+"microsoft-edge-stable")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("az")
	if err := Interactive(cmd, ""); err != nil {
		t.Fatal(err)
	}
	got := environment(cmd)["BROWSER"]
	if want := executable + separator + "firefox"; len(got) != 1 || got[0] != want {
		t.Fatalf("BROWSER = %q, want %q", got, want)
	}
}

func TestUserEnvironmentRemovesRouting(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	separator := string(os.PathListSeparator)
	got := userEnvironment([]string{"PATH=/bin", "BROWSER=" + executable + separator + "firefox", modeVariable + "=interactive", signalVariable + "=/session/signal"})
	if want := []string{"PATH=/bin", "BROWSER=firefox"}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("userEnvironment() = %q, want %q", got, want)
	}
	got = userEnvironment([]string{"BROWSER=" + executable, modeVariable + "=refuse"})
	if len(got) != 0 {
		t.Fatalf("userEnvironment() = %q, want empty", got)
	}
}

func TestRunWithoutTerminalNeverShowsAddress(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	t.Setenv(modeVariable, modeInteractive)
	writeBrowserSettings(t, root, `{"authentication_browser":{"executable":"`+filepath.ToSlash(filepath.Join(root, "missing-browser"))+`"}}`)
	// Redirect standard error to a file to observe what a non-terminal receives.
	capture, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = capture
	status := Run([]string{testAddress}, nil)
	os.Stderr = previous
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	if status != 0 {
		t.Fatalf("Run() = %d, want 0", status)
	}
	data, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SENSITIVE_STATE") || !strings.Contains(string(data), "No terminal is available") {
		t.Fatalf("non-terminal output = %q", data)
	}
}

func TestSignInTargetReadsTenantAndAccountDomain(t *testing.T) {
	cases := []struct{ address, tenant, domain string }{
		{"https://login.microsoftonline.com/cccccccc-cccc-4ccc-8ccc-cccccccccccc/oauth2/v2.0/authorize?client_id=x&login_hint=first.last%40Example.com", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "Example.com"},
		{"https://login.microsoftonline.com/example.com/oauth2/v2.0/authorize?client_id=x", "example.com", ""},
		{"https://login.microsoftonline.com/organizations/oauth2/v2.0/authorize?client_id=x", "", ""},
		{"https://login.microsoftonline.com/common/oauth2/v2.0/authorize?login_hint=nobody", "", ""},
	}
	for _, c := range cases {
		tenant, domain := signInTarget(c.address)
		if tenant != c.tenant || domain != c.domain {
			t.Errorf("signInTarget(%q) = %q, %q; want %q, %q", c.address, tenant, domain, c.tenant, c.domain)
		}
	}
}
