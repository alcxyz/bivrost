//go:build !windows

package azure

import (
	"context"
	"os/exec"
)

// command creates an Azure CLI command without starting it.
func command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "az", args...), nil
}
