//go:build windows

package session

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func terminationSignals(includeInterrupt bool) []os.Signal {
	// Go reports console close, logoff, and shutdown events as SIGTERM when
	// signal notification is enabled, giving cleanup a chance to complete.
	signals := []os.Signal{syscall.SIGTERM}
	if includeInterrupt {
		signals = append(signals, os.Interrupt)
	}
	return signals
}

// Keep background tunnels alive when the foreground console command receives Ctrl+C.
func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
func killProcess(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

func prepareInteractive(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}

func prepareInteractiveShell(cmd *exec.Cmd) { prepareInteractive(cmd) }

// Console Ctrl+C reaches a run command directly. Windows has no SIGTERM, so
// cancellation terminates the command before session cleanup proceeds.
func prepareRunCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = 10 * time.Second
}

func signalExitCode(*exec.ExitError) int { return RunFailureExitCode }
