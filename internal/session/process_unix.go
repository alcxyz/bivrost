//go:build !windows

package session

import (
	"os"
	"os/exec"
	"syscall"
	"time"
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
