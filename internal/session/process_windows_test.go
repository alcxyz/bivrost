//go:build windows

package session

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
)

func TestBackgroundTunnelsUseSeparateConsoleProcessGroup(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "ssh.exe")
	prepareProcess(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatal("background tunnel would receive foreground console Ctrl+C")
	}
	for _, prepare := range []func(*exec.Cmd){prepareInteractive, prepareInteractiveShell} {
		cmd := exec.CommandContext(context.Background(), "powershell.exe")
		prepare(cmd)
		if cmd.SysProcAttr != nil && cmd.SysProcAttr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP != 0 {
			t.Fatal("interactive process must still receive foreground Ctrl+C")
		}
	}
}
