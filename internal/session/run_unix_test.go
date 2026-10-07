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
	"syscall"
	"testing"
	"time"
)

func runRealCommand(t *testing.T, ctx context.Context, script string) (error, *platformProcess) {
	t.Helper()
	process, err := startPlatformCommand(ctx, &sessionCommand{argv: []string{"/bin/sh", "-c", script}}, os.Environ())
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
	process, err := startPlatformCommand(ctx, &sessionCommand{argv: []string{"/bin/sh", "-c", "trap 'echo term > " + marker + "; exit 0' TERM; while :; do sleep 0.05; done"}}, os.Environ())
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

func TestRunCommandInOwnGroupStopsDescendants(t *testing.T) {
	directory := t.TempDir()
	pidFile := filepath.Join(directory, "descendant")
	ctx, cancel := context.WithCancel(context.Background())
	// Signalling only the shell would leave its background child running.
	script := "sleep 300 & printf %s $! > " + pidFile + "; wait"
	process, err := startPlatformCommand(ctx, &sessionCommand{argv: []string{"/bin/sh", "-c", script}, ownGroup: true}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		pid, _ = readSignalHelperPID(pidFile)
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		cancel()
		process.stop()
		t.Fatal("descendant did not start")
	}
	cancel()
	process.stop()
	deadline = time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("descendant survived the command")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestResolveCommandUsesSessionPathAndClassifiesFailures(t *testing.T) {
	session := t.TempDir()
	ambient := t.TempDir()
	writeTestExecutable(t, filepath.Join(session, "podman"), "#!/bin/sh\n")
	writeTestExecutable(t, filepath.Join(ambient, "podman"), "#!/bin/sh\n")
	if err := os.WriteFile(filepath.Join(session, "plain"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=relative" + string(os.PathListSeparator) + session + string(os.PathListSeparator) + ambient}
	if got, err := resolveCommand("podman", env); err != nil || got != filepath.Join(session, "podman") {
		t.Errorf("resolveCommand(podman) = %q, %v; want session wrapper", got, err)
	}
	for name, want := range map[string]int{
		"missing-tool":                   RunCommandNotFound,
		filepath.Join(session, "absent"): RunCommandNotFound,
		"plain":                          RunCannotExecute,
		filepath.Join(session, "plain"):  RunCannotExecute,
	} {
		_, err := resolveCommand(name, env)
		var exit *ExitError
		if err == nil || !errors.As(commandStartError(name, err), &exit) || exit.Code != want {
			t.Errorf("resolveCommand(%q) error = %v, want status %d", name, err, want)
		}
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

func TestRunCommandEndToEndTerminationKeepsTunnelsForShutdown(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is unavailable")
	}
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

	// On SIGTERM the command records whether both tunnel helpers still run.
	m := markerDirectory
	script := `trap 'read a < ` + m + `/az.pid; read s < ` + m + `/ssh.pid; kill -0 $a && kill -0 $s && echo alive > ` + m + `/shutdown; exit 0' TERM; echo started > ` + m + `/started; while :; do ` + sleep + ` 0.05; done`
	cmd := exec.Command(testExecutable, "-test.run=^TestSignalCleanupHelper$", "--", "run", configPath, "/bin/sh", "-c", script)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(m, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("command did not start; stderr:\n%s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	childPIDs := make(map[string]int)
	for _, name := range []string{"az", "ssh"} {
		pid, err := readSignalHelperPID(filepath.Join(m, name+".pid"))
		if err != nil {
			t.Fatalf("read %s helper PID: %v", name, err)
		}
		childPIDs[name] = pid
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("bivrost run did not finish after SIGTERM")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != RunFailureExitCode || !strings.Contains(stderr.String(), "terminated") {
		t.Fatalf("exit = %v, want %d after termination; stderr:\n%s", err, RunFailureExitCode, stderr.String())
	}
	if contents, err := os.ReadFile(filepath.Join(m, "shutdown")); err != nil || strings.TrimSpace(string(contents)) != "alive" {
		t.Errorf("tunnels were not running during command shutdown: %q, %v", contents, err)
	}
	waitForSignalCleanup(t, filepath.Join(userConfigDirectory, "bivrost", "session-*"), childPIDs)
}
