//go:build !windows

package session

import (
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

func terminationSignals(includeInterrupt bool) []os.Signal {
	signals := []os.Signal{syscall.SIGTERM, syscall.SIGHUP}
	if includeInterrupt {
		signals = append(signals, os.Interrupt)
	}
	return signals
}
func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
func killProcess(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

func prepareInteractive(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
}

// Session shells ignore SIGTERM when interactive. SIGHUP lets them restore the
// terminal and notify their jobs; WaitDelay still bounds uncooperative shells.
func prepareInteractiveShell(cmd *exec.Cmd) {
	prepareInteractive(cmd)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGHUP) }
}

// prepareRunCommand lets cancellation ask the command to finish before the
// session closes. In its own process group, the request and the final cleanup
// reach its descendants too; the returned function stops any left behind.
func prepareRunCommand(cmd *exec.Cmd, ownGroup bool) func() {
	cmd.WaitDelay = 10 * time.Second
	if !ownGroup {
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		return func() {}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	return func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// terminalForeground reports whether Bivrost is the foreground job of its
// controlling terminal, which then delivers Ctrl+C to Bivrost's process group.
func terminalForeground() bool {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	defer tty.Close()
	var group int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, tty.Fd(), uintptr(syscall.TIOCGPGRP), uintptr(unsafe.Pointer(&group)))
	return errno == 0 && int(group) == syscall.Getpgrp()
}

func signalExitCode(err *exec.ExitError) int {
	if status, ok := err.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return runSignalExitOffset + int(status.Signal())
	}
	return RunFailureExitCode
}
