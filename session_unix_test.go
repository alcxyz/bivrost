//go:build !windows

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInteractiveShellCancellationExitsBashWithoutForcedKill(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-i")
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", "TERM=dumb"}
	prepareInteractiveShell(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	if _, err := stdin.Write([]byte("printf 'ready\\n'\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatalf("unexpected readiness output: %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interactive bash did not become ready")
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("interactive bash required forced termination")
	}
	status := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGHUP {
		t.Fatalf("shell cancellation status = %v, want SIGHUP", status)
	}
}

func TestOpenBastionCleansSessionAfterAuthenticationFailure(t *testing.T) {
	toolDirectory := t.TempDir()
	writeTestExecutable(t, filepath.Join(toolDirectory, "az"), "#!/bin/sh\nexit 17\n")
	writeTestExecutable(t, filepath.Join(toolDirectory, "ssh"), "#!/bin/sh\nexit 0\n")
	stateRoot := prepareOpenBastionTest(t, toolDirectory)

	_, err := openBastion(context.Background(), validSessionConfig())
	if err == nil || !strings.Contains(err.Error(), "Entra SSH setup failed") {
		t.Fatalf("openBastion() error = %v, want Entra setup error", err)
	}
	assertNoSessionDirectories(t, stateRoot)
}

func TestOpenBastionCleansSessionAfterTunnelStartFailure(t *testing.T) {
	toolDirectory := t.TempDir()
	// An executable with no recognized format passes LookPath but fails at Start.
	writeTestExecutable(t, filepath.Join(toolDirectory, "az"), "not an executable format\n")
	writeTestExecutable(t, filepath.Join(toolDirectory, "ssh"), "#!/bin/sh\nexit 0\n")
	stateRoot := prepareOpenBastionTest(t, toolDirectory)
	c := validSessionConfig()
	c.SSHUser = "azureuser"
	c.IdentityFile = filepath.Join(t.TempDir(), "id_ed25519")

	_, err := openBastion(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "could not start Azure Bastion tunnel") {
		t.Fatalf("openBastion() error = %v, want tunnel start error", err)
	}
	assertNoSessionDirectories(t, stateRoot)
}

func TestRunConnectCleansDetachedProcessesAndSessionOnHangup(t *testing.T) {
	toolDirectory := t.TempDir()
	markerDirectory := t.TempDir()
	configDirectory := t.TempDir()
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_SESSION", "")
	t.Setenv("BIVROST_SIGNAL_MARKER_DIR", markerDirectory)
	t.Setenv("GO_WANT_BIVROST_SIGNAL_HELPER", "1")
	t.Setenv("HOME", configDirectory)
	t.Setenv("PATH", toolDirectory)
	t.Setenv("SHELL", filepath.Join(toolDirectory, "shell"))
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
	for _, name := range []string{"az", "ssh", "shell"} {
		writeTestExecutable(t, filepath.Join(toolDirectory, name), helper)
	}

	proxyPort := availablePort(t)
	socksPort := availablePort(t)
	for socksPort == proxyPort {
		socksPort = availablePort(t)
	}
	c := validSessionConfig()
	c.Registry = "registry"
	c.RegistrySubscription = "registry-subscription"
	c.ProxyPort = proxyPort
	c.SOCKSPort = socksPort
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

	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalCleanupHelper$", "--", "cli", configPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	processDone := make(chan struct{})
	var processErr error
	go func() {
		processErr = cmd.Wait()
		close(processDone)
	}()
	cleanupComplete := false
	defer func() {
		_ = cmd.Process.Kill()
		<-processDone
		if !cleanupComplete {
			for _, name := range []string{"az", "ssh", "shell"} {
				if pid, err := readSignalHelperPID(filepath.Join(markerDirectory, name+".pid")); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
	}()

	waitForSignalMarker(t, filepath.Join(markerDirectory, "shell.pid"), processDone, &processErr)
	sessionPattern := filepath.Join(userConfigDirectory, "bivrost", "session-*")
	sessions, err := filepath.Glob(sessionPattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("active CLI session directories = %v, want one", sessions)
	}
	childPIDs := make(map[string]int)
	for _, name := range []string{"az", "ssh", "shell"} {
		pid, err := readSignalHelperPID(filepath.Join(markerDirectory, name+".pid"))
		if err != nil {
			t.Fatalf("read %s helper PID: %v", name, err)
		}
		childPIDs[name] = pid
	}

	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP to CLI: %v", err)
	}
	select {
	case <-processDone:
		if processErr != nil {
			t.Fatalf("CLI exit after SIGHUP: %v", processErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not exit after SIGHUP")
	}

	waitForSignalCleanup(t, sessionPattern, childPIDs)
	cleanupComplete = true
}

func TestTerminationSignalsIncludeHangupAndPreserveConnectInterruptHandling(t *testing.T) {
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !containsSignal(terminationSignals(true), signal) {
			t.Errorf("ordinary command signals lack %v", signal)
		}
	}
	connectSignals := terminationSignals(false)
	if containsSignal(connectSignals, os.Interrupt) {
		t.Error("connect signals include Ctrl+C")
	}
	for _, signal := range []os.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		if !containsSignal(connectSignals, signal) {
			t.Errorf("connect signals lack %v", signal)
		}
	}
}

func containsSignal(signals []os.Signal, want os.Signal) bool {
	for _, signal := range signals {
		if signal == want {
			return true
		}
	}
	return false
}

func TestSignalCleanupHelper(t *testing.T) {
	if os.Getenv("GO_WANT_BIVROST_SIGNAL_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		t.Fatal("missing helper mode")
	}
	args := os.Args[separator+1:]
	switch args[0] {
	case "cli":
		if len(args) != 2 {
			t.Fatal("missing CLI configuration")
		}
		// Cancellation also stops the shell and tunnels. Their completion can
		// win the select over ctx.Done, producing a disconnect error instead.
		// The parent asserts orderly return, child termination, and file cleanup;
		// it must not depend on which ready channel wins that race.
		_ = run([]string{"connect", "--config", args[1]})
	case "tool":
		if len(args) < 2 {
			t.Fatal("missing synthetic tool name")
		}
		name := filepath.Base(args[1])
		toolArgs := args[2:]
		switch name {
		case "az":
			serveSignalHelper(t, name, loopback(mustSignalHelperPort(t, toolArgs, "--port")))
		case "ssh":
			serveSignalHelper(t, name, mustSignalHelperValue(t, toolArgs, "-D"))
		case "shell":
			writeSignalHelperPID(t, name)
			select {}
		default:
			t.Fatalf("unknown synthetic tool %q", name)
		}
	default:
		t.Fatalf("unknown helper mode %q", args[0])
	}
}

func serveSignalHelper(t *testing.T, name, address string) {
	t.Helper()
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	writeSignalHelperPID(t, name)
	for {
		connection, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.Close()
	}
}

func writeSignalHelperPID(t *testing.T, name string) {
	t.Helper()
	path := filepath.Join(os.Getenv("BIVROST_SIGNAL_MARKER_DIR"), name+".pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustSignalHelperPort(t *testing.T, args []string, option string) int {
	t.Helper()
	value := mustSignalHelperValue(t, args, option)
	port, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("invalid %s value %q", option, value)
	}
	return port
}

func mustSignalHelperValue(t *testing.T, args []string, option string) string {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == option {
			return args[i+1]
		}
	}
	t.Fatalf("synthetic tool arguments lack %s: %q", option, args)
	return ""
}

func waitForSignalMarker(t *testing.T, path string, processDone <-chan struct{}, processErr *error) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-processDone:
			t.Fatalf("CLI exited before the session shell started: %v", *processErr)
		case <-deadline.C:
			t.Fatal("timed out waiting for the session shell")
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				return
			}
		}
	}
}

func readSignalHelperPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(string(data))
}

func waitForSignalCleanup(t *testing.T, sessionPattern string, childPIDs map[string]int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		sessions, err := filepath.Glob(sessionPattern)
		if err != nil {
			t.Fatal(err)
		}
		alive := make([]string, 0, len(childPIDs))
		for name, pid := range childPIDs {
			if err := syscall.Kill(pid, 0); err == nil {
				alive = append(alive, name)
			}
		}
		if len(sessions) == 0 && len(alive) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cleanup after SIGHUP left sessions %v and helper processes %v", sessions, alive)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPrepareInteractiveUsesForegroundProcessAndGracefulCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := helperCommandContext(t, ctx, "wait")
	prepareInteractive(cmd)

	if cmd.SysProcAttr != nil {
		t.Fatalf("prepareInteractive() set process-group attributes: %#v", cmd.SysProcAttr)
	}
	if cmd.WaitDelay != 5*time.Second {
		t.Errorf("prepareInteractive() WaitDelay = %v, want 5s", cmd.WaitDelay)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("interactive helper start error = %v", err)
	}

	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("interactive helper exited successfully after cancellation")
		}
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("interactive helper did not exit after context cancellation")
	}

	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("interactive cancellation status = %#v, want SIGTERM", cmd.ProcessState.Sys())
	}
}

func validSessionConfig() config {
	return config{
		Subscription:         "subscription",
		BastionName:          "bastion",
		BastionResourceGroup: "network-rg",
		VMResourceID:         "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/jump",
	}
}

func prepareOpenBastionTest(t *testing.T, toolDirectory string) string {
	t.Helper()
	configDirectory := t.TempDir()
	t.Setenv("PATH", toolDirectory)
	t.Setenv("XDG_CONFIG_HOME", configDirectory)
	t.Setenv("HOME", configDirectory)
	userConfigDirectory, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(userConfigDirectory, "bivrost")
}

func writeTestExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertNoSessionDirectories(t *testing.T, stateRoot string) {
	t.Helper()
	entries, err := os.ReadDir(stateRoot)
	if err != nil {
		t.Fatalf("read state root after failed open: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("failed open left temporary state behind: %v", entries)
	}
}
