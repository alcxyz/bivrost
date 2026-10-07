//go:build windows

package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// Console Ctrl+C reaches a run command directly. Windows has no SIGTERM and
// does not end children with their parent, so cancellation ends the command's
// whole process tree, such as the session Podman wrapper and Podman itself.
func prepareRunCommand(cmd *exec.Cmd, _ bool) func() {
	cmd.WaitDelay = 10 * time.Second
	cmd.Cancel = func() error {
		tree := exec.Command(systemTool("taskkill.exe"), "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		if tree.Run() != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	return func() {}
}

// Every process attached to the console receives Ctrl+C.
func terminalForeground() bool { return true }

func signalExitCode(*exec.ExitError) int { return RunFailureExitCode }

// systemTool locates a Windows system utility without depending on PATH.
func systemTool(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" || !filepath.IsAbs(root) {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", name)
}
