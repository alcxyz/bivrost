package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync/atomic"

	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
)

// Exit statuses for bivrost run follow env(1) and timeout(1): the command's own
// status passes through, so Bivrost's failures use values commands rarely return.
const (
	RunFailureExitCode  = 125
	RunCannotExecute    = 126
	RunCommandNotFound  = 127
	runSignalExitOffset = 128
)

// ExitError carries the process exit status for bivrost run. A nil Err means the
// command reported its own status and Bivrost has nothing to add.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("command exited with status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// runFailure labels every Bivrost-side error from bivrost run with the reserved status.
func runFailure(err error) error {
	if err == nil {
		return nil
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		return err
	}
	return &ExitError{Code: RunFailureExitCode, Err: err}
}

func runPlatformCommand(ctx context.Context, c profile.Profile, argv []string) error {
	// Resolve before opening any tunnel so a typo costs nothing.
	if _, err := exec.LookPath(argv[0]); err != nil {
		code := RunCommandNotFound
		if !errors.Is(err, exec.ErrNotFound) {
			code = RunCannotExecute
		}
		return &ExitError{Code: code, Err: fmt.Errorf("cannot run %q: %w", argv[0], err)}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// SIGTERM and SIGHUP already cancel ctx. Ctrl+C cancels setup; once the
	// command runs, the terminal delivers it to the command directly and a
	// second copy from Bivrost could escalate tools such as Terraform.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	var commandRunning atomic.Bool
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-interrupts:
				if !commandRunning.Load() {
					cancel()
					return
				}
			}
		}
	}()
	return runFailure(platformSession(ctx, c, &commandRunning, defaultPlatformServices(), false, argv))
}

func runSessionCommand(ctx context.Context, running *atomic.Bool, services platformServices, name string, args, env []string, kubeUnavailable *kubeUnavailableError, activation *acrActivation, proxy *platformProxy, ssh *platformProcess, bastion *platformBastion) error {
	if kubeUnavailable != nil {
		fmt.Fprintln(os.Stderr, "Kubernetes is unavailable for this session: "+kubeUnavailable.Error()+". KUBECONFIG points to an isolated empty configuration.")
	}
	commandCtx, cancelCommand := context.WithCancel(ctx)
	defer cancelCommand()
	running.Store(true)
	defer running.Store(false)
	finish := diagnostics.Step(ctx, diagnostics.EventShell)
	command, err := services.startCommand(commandCtx, name, args, env)
	if err != nil {
		finish(err)
		code := RunCannotExecute
		if errors.Is(err, exec.ErrNotFound) {
			code = RunCommandNotFound
		}
		return &ExitError{Code: code, Err: fmt.Errorf("cannot run %q: %w", name, err)}
	}
	// The command's status is its own result, so the diagnostic step records only
	// whether Bivrost kept the session until the command finished.
	var lost error
	defer func() { finish(lost) }()
	stop := func(reason error) error {
		lost = reason
		cancelCommand()
		command.stop()
		return reason
	}

	select {
	case <-command.done:
		return commandExit(command.err())
	case <-activation.done:
		return stop(errors.New("Podman session disconnected while the command was running"))
	case <-proxy.done:
		diagnostics.Event(ctx, diagnostics.EventProxyStopped)
		return stop(errors.New("local HTTPS proxy stopped while the command was running"))
	case <-ssh.done:
		diagnostics.Event(ctx, diagnostics.EventSSHStopped)
		return stop(errors.New("SSH forwarding disconnected while the command was running"))
	case <-bastion.done:
		diagnostics.Event(ctx, diagnostics.EventBastionStopped)
		return stop(errors.New("Bastion disconnected while the command was running"))
	case <-ctx.Done():
		return stop(fmt.Errorf("terminated before the command finished: %w", ctx.Err()))
	}
}

func commandExit(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return fmt.Errorf("command did not complete: %w", err)
	}
	code := exitErr.ExitCode()
	if code < 0 {
		code = signalExitCode(exitErr)
	}
	return &ExitError{Code: code}
}

func startPlatformCommand(ctx context.Context, name string, args, env []string) (*platformProcess, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	prepareRunCommand(cmd)
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	process := &interactivePlatformProcess{done: make(chan struct{}), cancel: cancel}
	go func() {
		process.waitErr = cmd.Wait()
		close(process.done)
	}()
	return &platformProcess{
		done: process.done,
		err:  func() error { return process.waitErr },
		stop: func() {
			select {
			case <-process.done:
				return
			default:
			}
			process.cancel()
			<-process.done
		},
	}, nil
}
