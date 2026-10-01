//go:build !windows

package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alcxyz/bivrost/internal/diagnostics"
)

const testSignInAddress = "https://login.example/authorize?state=SENSITIVE_STATE"

// fakeSSHCertificateAzureCLI mirrors Azure CLI 2.89: report the silent failure,
// open a browser for the SSH certificate scope, then wait for the redirect.
// Each value of $? from BROWSER is recorded, as webbrowser would observe it.
func fakeSSHCertificateAzureCLI(t *testing.T, afterBrowser string) (records string) {
	t.Helper()
	toolDirectory := t.TempDir()
	records = t.TempDir()
	writeTestExecutable(t, filepath.Join(toolDirectory, "az"), "#!/bin/sh\n"+
		"echo $$ > "+filepath.Join(records, "pid")+"\n"+
		"printf '%s' \"$BROWSER\" > "+filepath.Join(records, "browser-variable")+"\n"+
		"echo 'AADSTS50076: Due to a configuration change made by your administrator' >&2\n"+
		"\"${BROWSER%%:*}\" '"+testSignInAddress+"'\n"+
		"echo $? > "+filepath.Join(records, "browser-status")+"\n"+
		afterBrowser)
	t.Setenv("PATH", toolDirectory)
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("HOME", config)
	return records
}

func shortSSHCertificateTimeouts(t *testing.T, setup, signIn time.Duration) {
	t.Helper()
	previousSetup, previousSignIn := sshCertificateTimeout, sshSignInTimeout
	sshCertificateTimeout, sshSignInTimeout = setup, signIn
	t.Cleanup(func() { sshCertificateTimeout, sshSignInTimeout = previousSetup, previousSignIn })
}

func writeUserSettings(t *testing.T, content string) {
	t.Helper()
	directory := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bivrost")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "settings.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRecord(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func runPrepareSSHCertificate(t *testing.T) error {
	t.Helper()
	directory := t.TempDir()
	return prepareSSHCertificate(context.Background(), validSessionConfig(), filepath.Join(directory, "ssh_config"), directory, 2222)
}

func TestPrepareSSHCertificateWaitsForDefaultBrowserSignIn(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is not installed")
	}
	// Sign-in outlasts the setup timeout, so success proves the longer window.
	records := fakeSSHCertificateAzureCLI(t, sleep+" 0.6\nexit 0\n")
	t.Setenv("BROWSER", "/usr/bin/firefox")
	shortSSHCertificateTimeouts(t, 200*time.Millisecond, 10*time.Second)

	if err := runPrepareSSHCertificate(t); err != nil {
		t.Fatalf("prepareSSHCertificate() = %v, want completed sign-in", err)
	}
	// Without authentication_browser, Bivrost declines so webbrowser continues
	// with the user's BROWSER value and then the system default.
	if status := readRecord(t, filepath.Join(records, "browser-status")); status != "1" {
		t.Fatalf("browser request status = %s, want 1 (fall through)", status)
	}
	if browser := readRecord(t, filepath.Join(records, "browser-variable")); !strings.HasSuffix(browser, ":/usr/bin/firefox") {
		t.Fatalf("BROWSER = %q, want previous value preserved after Bivrost", browser)
	}
}

func TestPrepareSSHCertificateOpensConfiguredBrowser(t *testing.T) {
	records := fakeSSHCertificateAzureCLI(t, "exit 0\n")
	browser := filepath.Join(t.TempDir(), "browser")
	// Shell builtins only: the test PATH contains just the fake Azure CLI.
	writeTestExecutable(t, browser, "#!/bin/sh\nfor argument in \"$@\"; do printf '%s\\n' \"$argument\"; done > "+filepath.Join(records, "argv")+" && : > "+filepath.Join(records, "argv-done")+"\n")
	writeUserSettings(t, `{"authentication_browser":{"executable":"`+browser+`","arguments":["-P","work","{url}"]}}`)

	if err := runPrepareSSHCertificate(t); err != nil {
		t.Fatal(err)
	}
	if status := readRecord(t, filepath.Join(records, "browser-status")); status != "0" {
		t.Fatalf("browser request status = %s, want 0 so no other browser opens", status)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(records, "argv-done")); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, want := readRecord(t, filepath.Join(records, "argv")), "-P\nwork\n"+testSignInAddress; got != want {
		t.Fatalf("browser arguments = %q, want %q", got, want)
	}
}

func TestPrepareSSHCertificateStopsUnfinishedSignIn(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is not installed")
	}
	records := fakeSSHCertificateAzureCLI(t, "exec "+sleep+" 60\n")
	shortSSHCertificateTimeouts(t, 5*time.Second, 300*time.Millisecond)

	start := time.Now()
	err = runPrepareSSHCertificate(t)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("prepareSSHCertificate() waited %v", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "not completed") {
		t.Fatalf("prepareSSHCertificate() error = %v, want unfinished sign-in timeout", err)
	}
	assertProcessStopped(t, filepath.Join(records, "pid"))
}

func TestPrepareSSHCertificateRefusesBrowserWhenInteractiveConnectIsOff(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is not installed")
	}
	records := fakeSSHCertificateAzureCLI(t, "exec "+sleep+" 60\n")
	writeUserSettings(t, `{"interactive_connect":false}`)

	start := time.Now()
	err = runPrepareSSHCertificate(t)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("prepareSSHCertificate() waited %v for a refused sign-in", elapsed)
	}
	if !errors.Is(err, diagnostics.ErrInteractionRequired) || !strings.Contains(err.Error(), "multi-factor") || !strings.Contains(err.Error(), "bivrost login --ssh") {
		t.Fatalf("prepareSSHCertificate() error = %v, want MFA interaction error", err)
	}
	if strings.Contains(err.Error(), "SENSITIVE_STATE") || strings.Contains(err.Error(), "login.example") {
		t.Fatalf("error exposed sign-in address: %v", err)
	}
	// Bivrost may stop Azure CLI before it records the browser status;
	// authbrowser tests cover the refused request's exit status.
	assertProcessStopped(t, filepath.Join(records, "pid"))
}

func TestPrepareSSHCertificateSucceedsWithoutBrowser(t *testing.T) {
	toolDirectory := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	writeTestExecutable(t, filepath.Join(toolDirectory, "az"), "#!/bin/sh\necho \"$BIVROST_AUTH_BROWSER $*\" > "+argsFile+"\n")
	t.Setenv("PATH", toolDirectory)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	directory := t.TempDir()
	if err := prepareSSHCertificate(context.Background(), validSessionConfig(), filepath.Join(directory, "ssh_config"), directory, 2222); err != nil {
		t.Fatal(err)
	}
	want := "interactive ssh config --ip 127.0.0.1 --port 2222 --file " + filepath.Join(directory, "ssh_config") + " --keys-destination-folder " + directory + " --subscription subscription"
	if got := readRecord(t, argsFile); got != want {
		t.Fatalf("Azure CLI invocation = %q, want %q", got, want)
	}
}

func assertProcessStopped(t *testing.T, pidFile string) {
	t.Helper()
	pid, _ := strconv.Atoi(readRecord(t, pidFile))
	if pid <= 0 {
		t.Fatalf("invalid fake Azure CLI pid in %s", pidFile)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("fake Azure CLI process %d is still running", pid)
	}
}
