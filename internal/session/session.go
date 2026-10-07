package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/alcxyz/bivrost/internal/azure"
	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
)

// bastionSession owns only local tunnel processes and temporary SSH credentials.
// Both ACR forwarding and interactive shells use the same connection setup.
type bastionSession struct {
	port      int
	stateRoot string
	directory string
	sshConfig string
	process   *child
	cancel    context.CancelFunc
}

func (s *bastionSession) close() {
	if s.process != nil {
		s.process.stop()
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.directory != "" {
		_ = os.RemoveAll(s.directory)
	}
}

func openBastion(ctx context.Context, c profile.Profile) (_ *bastionSession, resultErr error) {
	finish := diagnostics.Step(ctx, diagnostics.EventBastion)
	defer func() { finish(resultErr) }()
	if err := c.ValidateConnection(); err != nil {
		return nil, err
	}
	for _, tool := range []string{"az", "ssh"} {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("%s is missing from PATH", tool)
		}
	}
	stateRoot, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	stateRoot = filepath.Join(stateRoot, "bivrost")
	if err := os.MkdirAll(stateRoot, 0700); err != nil {
		return nil, err
	}
	s := &bastionSession{stateRoot: stateRoot}
	complete := false
	defer func() {
		if !complete {
			s.close()
		}
	}()
	s.directory, err = os.MkdirTemp(stateRoot, "session-")
	if err != nil {
		return nil, err
	}
	s.port, err = freePort()
	if err != nil {
		return nil, err
	}
	s.sshConfig = filepath.Join(s.directory, "ssh_config")
	if c.SSHUser == "" {
		fmt.Fprintln(os.Stderr, "Preparing a short-lived Entra SSH certificate using your local Azure login...")
		finishCredentials := diagnostics.Step(ctx, diagnostics.EventSSHCredentials)
		e := prepareSSHCertificate(ctx, c, s.sshConfig, s.directory, s.port)
		finishCredentials(e)
		if e != nil {
			return nil, e
		}
	} else {
		if err := os.WriteFile(s.sshConfig, nil, 0600); err != nil {
			return nil, err
		}
	}
	tunnelCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	cmd, err := azure.Command(tunnelCtx, "network", "bastion", "tunnel", "--name", c.BastionName, "--resource-group", c.BastionResourceGroup, "--subscription", c.Subscription, "--target-resource-id", c.VMResourceID, "--resource-port", "22", "--port", strconv.Itoa(s.port), "--only-show-errors")
	if err != nil {
		return nil, err
	}
	tunnelErrors := azure.NewTunnelErrors()
	cmd.Stderr = tunnelErrors
	// Stderr is a pipe, so a descendant holding it open must not block Wait.
	cmd.WaitDelay = 5 * time.Second
	fmt.Fprintln(os.Stderr, "Opening the Bastion tunnel...")
	s.process, err = startChild(cmd)
	if err != nil {
		return nil, errors.New("could not start Azure Bastion tunnel")
	}
	if err := waitPort(ctx, profile.Loopback(s.port), s.process); err != nil {
		// Cancellation also ends the tunnel; report it, not the tunnel's exit.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		select {
		case <-s.process.done:
			// Wait has returned, so Azure CLI's stderr is fully copied.
			return nil, tunnelErrors.Err()
		default:
		}
		return nil, err
	}
	complete = true
	return s, nil
}

func interactiveSSH(ctx context.Context, c profile.Profile) (resultErr error) {
	finish := diagnostics.Step(ctx, diagnostics.EventShell)
	defer func() { finish(resultErr) }()
	s, err := openBastion(ctx, c)
	if err != nil {
		return err
	}
	defer s.close()
	shellCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(shellCtx, "ssh", interactiveSSHArguments(c, s.sshConfig, s.stateRoot, s.port)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Interactive SSH must stay in the terminal's foreground process group. Its
	// ordinary signal handling restores terminal settings before exiting on Unix.
	prepareInteractive(cmd)
	fmt.Println("Opening the management shell. Azure CLI inside the VM has its own login session.")
	if err := cmd.Start(); err != nil {
		return errors.New("could not start interactive SSH")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-s.process.done:
		cancel()
		<-done
		return errors.New("Bastion disconnected; start the management shell again")
	case <-ctx.Done():
		cancel()
		<-done
		return ctx.Err()
	}
}
