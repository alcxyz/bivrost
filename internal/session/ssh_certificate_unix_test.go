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

func TestPrepareSSHCertificateStopsWhenAzureCLIRequestsBrowser(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is not installed")
	}
	toolDirectory := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "pid")
	// Mirrors Azure CLI 2.89: report the silent failure, then open a browser for
	// the SSH certificate scope and wait for the sign-in redirect.
	writeTestExecutable(t, filepath.Join(toolDirectory, "az"), "#!/bin/sh\n"+
		"echo $$ > "+pidFile+"\n"+
		"echo 'AADSTS50076: Due to a configuration change made by your administrator' >&2\n"+
		"\"$BROWSER\" 'https://login.example/authorize?state=SENSITIVE_STATE' || exit 9\n"+
		"exec "+sleep+" 60\n")
	t.Setenv("PATH", toolDirectory)
	t.Setenv("BROWSER", "/usr/bin/should-not-run")
	directory := t.TempDir()

	start := time.Now()
	err = prepareSSHCertificate(context.Background(), validSessionConfig(), filepath.Join(directory, "ssh_config"), directory, 2222)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("prepareSSHCertificate() waited %v for a refused sign-in", elapsed)
	}
	if !errors.Is(err, diagnostics.ErrInteractionRequired) || !strings.Contains(err.Error(), "multi-factor") {
		t.Fatalf("prepareSSHCertificate() error = %v, want MFA interaction error", err)
	}
	if strings.Contains(err.Error(), "SENSITIVE_STATE") || strings.Contains(err.Error(), "login.example") {
		t.Fatalf("error exposed sign-in address: %v", err)
	}
	data, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid <= 0 {
		t.Fatalf("invalid fake Azure CLI pid %q", data)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("fake Azure CLI process %d is still running", pid)
	}
}

func TestPrepareSSHCertificateSucceedsWithoutBrowser(t *testing.T) {
	toolDirectory := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	writeTestExecutable(t, filepath.Join(toolDirectory, "az"), "#!/bin/sh\necho \"$BIVROST_AUTH_BROWSER $*\" > "+argsFile+"\n")
	t.Setenv("PATH", toolDirectory)
	directory := t.TempDir()
	if err := prepareSSHCertificate(context.Background(), validSessionConfig(), filepath.Join(directory, "ssh_config"), directory, 2222); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "refuse ssh config --ip 127.0.0.1 --port 2222 --file " + filepath.Join(directory, "ssh_config") + " --keys-destination-folder " + directory + " --subscription subscription\n"
	if string(data) != want {
		t.Fatalf("Azure CLI invocation = %q, want %q", data, want)
	}
}
