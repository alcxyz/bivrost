//go:build !windows

package azure

import (
	"context"
	"os/exec"
)

// Command creates an Azure CLI command without starting it.
func Command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "az", args...), nil
}
