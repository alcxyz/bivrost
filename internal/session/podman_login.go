package session

import (
	"context"
	"os/exec"

	profile "github.com/alcxyz/bivrost/internal/config"
)

func requirePodmanForACR(ctx context.Context, c profile.Profile) error {
	_, err := inspectPodman(ctx)
	return err
}

func registryLoginCommand(ctx context.Context, c profile.Profile) (*exec.Cmd, error) {
	args := []string{"login", c.Registry + ".azurecr.io", "--username", "00000000-0000-0000-0000-000000000000", "--password-stdin"}
	machine, err := inspectPodman(ctx)
	if err != nil {
		return nil, err
	}
	if machine != nil {
		args = append([]string{"--connection", machine.ConnectionName}, args...)
	} else {
		args = append([]string{"--remote=false"}, args...)
	}
	return exec.CommandContext(ctx, "podman", args...), nil
}
