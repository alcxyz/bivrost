//go:build windows

package azure

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Command creates an Azure CLI command without starting it.
func Command(ctx context.Context, args ...string) (*exec.Cmd, error) {
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
