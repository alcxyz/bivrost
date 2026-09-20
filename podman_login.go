package main

import (
	"context"
	"os/exec"
)

func requirePodmanForACR(ctx context.Context, c config) error {
	_, err := inspectPodman(ctx)
	return err
}

func registryLoginCommand(ctx context.Context, c config) (*exec.Cmd, error) {
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
