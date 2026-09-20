package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWindowsPodmanWrapperHelper(t *testing.T) {
	if os.Getenv("BIVROST_TEST_WINDOWS_WRAPPER") == "" {
		return
	}
	os.Exit(executeWrappedPodman(os.Args[0], []string{"-test.run=^TestWindowsPodmanChildHelper$"}))
}

func TestWindowsPodmanChildHelper(t *testing.T) {
	switch os.Getenv("BIVROST_TEST_WINDOWS_WRAPPER") {
	case "streams":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		fmt.Fprint(os.Stderr, "stderr-marker")
		os.Exit(37)
	case "interrupt":
		interrupts := make(chan os.Signal, 2)
		signal.Notify(interrupts, os.Interrupt)
		// The outer helper has its own console. Broadcasting here reaches it
		// and this child without sending any event to the test runner's console.
		generate := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")
		if result, _, err := generate.Call(0, 0); result == 0 {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(90)
		}
		select {
		case <-interrupts:
		case <-time.After(5 * time.Second):
			os.Exit(91)
		}
		// Simulate cleanup after Ctrl+C. A wrapper using the default interrupt
		// action exits before this child returns its deliberate status.
		select {
		case <-interrupts:
			os.Exit(92)
		case <-time.After(200 * time.Millisecond):
		}
		fmt.Fprint(os.Stdout, "cleanup-complete")
		os.Exit(37)
	}
}

func TestWindowsPodmanWrapperPreservesStreamsAndExit(t *testing.T) {
	testWindowsPodmanWrapper(t, "streams", "stdin-marker", "stderr-marker")
}

func TestWindowsPodmanWrapperWaitsAfterConsoleInterrupt(t *testing.T) {
	testWindowsPodmanWrapper(t, "interrupt", "cleanup-complete", "")
}

func testWindowsPodmanWrapper(t *testing.T, mode, wantStdout, wantStderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsPodmanWrapperHelper$")
	command.Env = append(os.Environ(), "BIVROST_TEST_WINDOWS_WRAPPER="+mode)
	command.Stdin = strings.NewReader("stdin-marker")
	if mode == "interrupt" {
		// CREATE_NEW_CONSOLE isolates the generated Ctrl+C from the test runner.
		command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010}
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	failure, ok := err.(*exec.ExitError)
	if !ok || failure.ExitCode() != 37 {
		t.Fatalf("exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.String() != wantStdout || stderr.String() != wantStderr {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
