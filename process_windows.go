//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
func azureCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	path, err := exec.LookPath("az")
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(filepath.Ext(path), ".cmd") || strings.EqualFold(filepath.Ext(path), ".bat") {
		// Standard Azure CLI Windows installation bundles Python next to its wbin
		// directory. Invoke it directly: no cmd.exe interpolation of arguments.
		python := filepath.Join(filepath.Dir(path), "..", "python.exe")
		if _, err := os.Stat(python); err != nil {
			return nil, errors.New("Azure CLI's bundled python.exe was not found; use the standard Windows Azure CLI installer")
		}
		return exec.CommandContext(ctx, python, append([]string{"-IBm", "azure.cli"}, args...)...), nil
	}
	return exec.CommandContext(ctx, path, args...), nil
}

func prepareInteractive(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}

func prepareInteractiveShell(cmd *exec.Cmd) { prepareInteractive(cmd) }
