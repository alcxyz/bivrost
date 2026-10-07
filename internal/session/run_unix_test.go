//go:build !windows

package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runRealCommand(t *testing.T, ctx context.Context, script string) (error, *platformProcess) {
	t.Helper()
	process, err := startPlatformCommand(ctx, "/bin/sh", []string{"-c", script}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(20 * time.Second):
		process.stop()
		t.Fatal("command did not finish")
	}
	return commandExit(process.err()), process
}

func TestRunCommandExitStatusPassesThrough(t *testing.T) {
	err, _ := runRealCommand(t, context.Background(), "exit 3")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 || exit.Err != nil {
		t.Fatalf("error = %v, want own status 3", err)
	}
	if err, _ := runRealCommand(t, context.Background(), "exit 0"); err != nil {
		t.Fatalf("successful command error = %v", err)
	}
}

func TestRunCommandSignalBecomesConventionalStatus(t *testing.T) {
	err, _ := runRealCommand(t, context.Background(), "kill -KILL $$")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 128+9 {
		t.Fatalf("error = %v, want status 137", err)
	}
}

func TestRunCommandCancellationSendsTerminateFirst(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "terminated")
	ctx, cancel := context.WithCancel(context.Background())
	process, err := startPlatformCommand(ctx, "/bin/sh", []string{"-c", "trap 'echo term > " + marker + "; exit 0' TERM; while :; do sleep 0.05; done"}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	process.stop()
	contents, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(contents)) != "term" {
		t.Fatalf("command did not handle SIGTERM before stopping: %q, %v", contents, err)
	}
}

func TestRunCommandEndToEndKeepsStdoutAndStatusAndCleansUp(t *testing.T) {
	toolDirectory := t.TempDir()
	markerDirectory := t.TempDir()
	configDirectory := t.TempDir()
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_SESSION", "")
	t.Setenv("BIVROST_SIGNAL_MARKER_DIR", markerDirectory)
	t.Setenv("GO_WANT_BIVROST_SIGNAL_HELPER", "1")
	t.Setenv("HOME", configDirectory)
	t.Setenv("PATH", toolDirectory)
	t.Setenv("XDG_CONFIG_HOME", configDirectory)
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("KUBECONFIG", "/clusters/ambient")
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BIVROST_SIGNAL_TEST_BINARY", testExecutable)
	userConfigDirectory, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	helper := "#!/bin/sh\nexec \"$BIVROST_SIGNAL_TEST_BINARY\" -test.run=^TestSignalCleanupHelper$ -- tool \"$0\" \"$@\"\n"
	for _, name := range []string{"az", "ssh"} {
		writeTestExecutable(t, filepath.Join(toolDirectory, name), helper)
	}

	c := validSessionConfig()
	c.ProxyPort = availablePort(t)
	c.SOCKSPort = availablePort(t)
	for c.SOCKSPort == c.ProxyPort {
		c.SOCKSPort = availablePort(t)
	}
	c.SSHUser = "azureuser"
	c.IdentityFile = filepath.Join(configDirectory, "unused-identity")
	configData, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDirectory, "environment.json")
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}

	script := `printf 'kubeconfig=%s\nproxy=%s\n' "$KUBECONFIG" "$HTTPS_PROXY"; echo diagnostic >&2; exit 3`
	cmd := exec.Command(testExecutable, "-test.run=^TestSignalCleanupHelper$", "--", "run", configPath, "/bin/sh", "-c", script)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("bivrost run did not finish")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("exit = %v, want command status 3; stderr:\n%s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "kubeconfig="+filepath.Join(userConfigDirectory, "bivrost", "session-")) || lines[1] != "proxy="+c.ProxyURL() {
		t.Fatalf("stdout must contain only the command's output, got:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "diagnostic") || !strings.Contains(stderr.String(), "Opening the Bastion tunnel") {
		t.Errorf("stderr lacks command or progress output:\n%s", stderr.String())
	}
	childPIDs := make(map[string]int)
	for _, name := range []string{"az", "ssh"} {
		pid, err := readSignalHelperPID(filepath.Join(markerDirectory, name+".pid"))
		if err != nil {
			t.Fatalf("read %s helper PID: %v", name, err)
		}
		childPIDs[name] = pid
	}
	waitForSignalCleanup(t, filepath.Join(userConfigDirectory, "bivrost", "session-*"), childPIDs)
}
