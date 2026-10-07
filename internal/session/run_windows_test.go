//go:build windows

package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestRunTreeHelper starts a child and waits, standing in for the Podman wrapper.
func TestRunTreeHelper(t *testing.T) {
	switch os.Getenv("GO_WANT_BIVROST_RUN_TREE") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestRunTreeHelper$")
		child.Env = append(os.Environ(), "GO_WANT_BIVROST_RUN_TREE=child")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("BIVROST_RUN_TREE_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(2)
		}
		_ = child.Wait()
		os.Exit(0)
	case "child":
		time.Sleep(5 * time.Minute)
		os.Exit(0)
	}
}

func TestRunCommandCancellationEndsProcessTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child")
	t.Setenv("GO_WANT_BIVROST_RUN_TREE", "parent")
	t.Setenv("BIVROST_RUN_TREE_PID", pidFile)
	ctx, cancel := context.WithCancel(context.Background())
	process, err := startPlatformCommand(ctx, &sessionCommand{argv: []string{os.Args[0], "-test.run=^TestRunTreeHelper$"}}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(string(data))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		cancel()
		process.stop()
		t.Fatal("child did not start")
	}
	cancel()
	process.stop()
	deadline = time.Now().Add(10 * time.Second)
	for {
		child, err := os.FindProcess(pid)
		if err != nil {
			return
		}
		_ = child.Release()
		if time.Now().After(deadline) {
			if child, err := os.FindProcess(pid); err == nil {
				_ = child.Kill()
			}
			t.Fatal("child survived cancellation of its parent command")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
