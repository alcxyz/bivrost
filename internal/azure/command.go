package azure

import (
	"context"
	"os/exec"

	"github.com/alcxyz/bivrost/internal/authbrowser"
)

// Command creates a non-interactive Azure CLI command without starting it.
// Browser sign-in requests from Azure CLI are refused, so connection and
// diagnostic paths fail instead of opening an unexpected browser.
func Command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	cmd, err := command(ctx, args...)
	if err != nil {
		return nil, err
	}
	if err := authbrowser.Refuse(cmd, ""); err != nil {
		return nil, err
	}
	return cmd, nil
}

// InteractiveCommand creates an Azure CLI command that may open a browser for
// sign-in. Callers choose the browser with authbrowser.Interactive.
func InteractiveCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	return command(ctx, args...)
}
