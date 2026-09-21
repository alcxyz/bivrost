//go:build !windows

package podman

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestPodmanWrapperExecHelper(t *testing.T) {
	if os.Getenv("BIVROST_TEST_PODMAN_EXEC") != "1" {
		return
	}
	script := "read line; printf '%s' \"$line\"; printf 'stderr-marker' >&2; exit 37"
	if os.Getenv("BIVROST_TEST_PODMAN_SIGNAL") == "1" {
		script = "kill -TERM $$"
	}
	os.Exit(executeWrappedPodman("/bin/sh", []string{"-c", script}))
}

func TestPodmanWrapperPreservesStreamsAndExit(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestPodmanWrapperExecHelper$")
	command.Env = append(os.Environ(), "BIVROST_TEST_PODMAN_EXEC=1")
	command.Stdin = strings.NewReader("stdin-marker\n")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	failure, ok := err.(*exec.ExitError)
	if !ok || failure.ExitCode() != 37 {
		t.Fatalf("exit = %v", err)
	}
	if stdout.String() != "stdin-marker" || stderr.String() != "stderr-marker" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestPodmanWrapperPreservesSignal(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestPodmanWrapperExecHelper$")
	command.Env = append(os.Environ(), "BIVROST_TEST_PODMAN_EXEC=1", "BIVROST_TEST_PODMAN_SIGNAL=1")
	err := command.Run()
	failure, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("exit=%v", err)
	}
	status := failure.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("status=%v", status)
	}
}
