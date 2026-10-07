package session

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"

	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
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

// runMarker tells Bivrost commands started by bivrost run that the session has
// no controller, so they can say so instead of reporting a broken session.
const runMarker = "BIVROST_RUN"

func insideRun() bool { return os.Getenv(runMarker) == "1" }

// sessionCommand is the command bivrost run executes in place of the shell.
type sessionCommand struct {
	argv []string
	// ownGroup places the command in its own process group, so stopping it also
	// stops its descendants. It is false while Bivrost is the terminal's
	// foreground job, where the command must share the group to read the
	// terminal and receive Ctrl+C directly.
	ownGroup bool
	// terminate closes when Bivrost is asked to stop while the command runs.
	// The session stays up until the command has exited or been stopped.
	terminate <-chan struct{}
}

func runPlatformCommand(ctx context.Context, c profile.Profile, argv []string) error {
	// Resolve before opening any tunnel so a typo costs nothing.
	if _, err := resolveCommand(argv[0], os.Environ()); err != nil {
		return commandStartError(argv[0], err)
	}
	// ctx ends on SIGTERM or SIGHUP. Tunnels use a separate context so that a
	// running command keeps its connectivity while it shuts down; only setup is
	// cancelled directly.
	sessionCtx, cancelSession := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelSession()
	command := &sessionCommand{argv: argv, ownGroup: !terminalForeground()}
	terminate := make(chan struct{})
	command.terminate = terminate

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	var running atomic.Bool
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		for {
			select {
			case <-finished:
				return
			case <-ctx.Done():
			case <-interrupts:
				// The terminal already delivered Ctrl+C to a command in our group;
				// a second copy could escalate tools such as Terraform.
				if running.Load() && !command.ownGroup {
					continue
				}
			}
			close(terminate)
			if !running.Load() {
				cancelSession()
			}
			return
		}
	}()
	return runFailure(platformSession(sessionCtx, c, &running, defaultPlatformServices(), false, command))
}

func commandStartError(name string, err error) error {
	code := RunCannotExecute
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		code = RunCommandNotFound
	}
	return &ExitError{Code: code, Err: fmt.Errorf("cannot run %q: %w", name, err)}
}

// resolveCommand searches the PATH that the command will receive, so session
// additions such as the Podman wrapper take effect as they do in a shell.
func resolveCommand(name string, env []string) (string, error) {
	if strings.ContainsAny(name, `/\`) {
		return exec.LookPath(name)
	}
	var denied error
	for _, directory := range filepath.SplitList(shellinit.EnvironmentValue(env, "PATH")) {
		// Relative entries would resolve against the working directory.
		if !filepath.IsAbs(directory) {
			continue
		}
		candidate := filepath.Join(directory, name)
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path, nil
		}
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && denied == nil {
			denied = err
		}
	}
	if denied != nil {
		return "", denied
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func runSessionCommand(ctx context.Context, running *atomic.Bool, services platformServices, command *sessionCommand, env []string, kubeUnavailable *kubeUnavailableError, activation *acrActivation, proxy *platformProxy, ssh *platformProcess, bastion *platformBastion) error {
	if kubeUnavailable != nil {
		fmt.Fprintln(os.Stderr, "Kubernetes is unavailable for this session: "+kubeUnavailable.Error()+". KUBECONFIG points to an isolated empty configuration.")
	}
	env = shellinit.ReplaceEnvironment(env, runMarker, "1")
	commandCtx, cancelCommand := context.WithCancel(ctx)
	defer cancelCommand()
	running.Store(true)
	defer running.Store(false)
	// A stop request that raced with setup cancelled ctx instead of terminate.
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-command.terminate:
		return errors.New("terminated before the command started")
	default:
	}
	finish := diagnostics.Step(ctx, diagnostics.EventShell)
	process, err := services.startCommand(commandCtx, command, env)
	if err != nil {
		finish(err)
		return commandStartError(command.argv[0], err)
	}
	// The command's status is its own result, so the diagnostic step records only
	// whether Bivrost kept the session until the command finished.
	var lost error
	defer func() { finish(lost) }()
	// Stopping waits for the command before the deferred session cleanup runs,
	// so a terminated command keeps its tunnels while it shuts down.
	stop := func(reason error) error {
		lost = reason
		cancelCommand()
		process.stop()
		return reason
	}

	select {
	case <-process.done:
		return commandExit(process.err())
	case <-command.terminate:
		return stop(errors.New("terminated before the command finished"))
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
		return stop(fmt.Errorf("session ended before the command finished: %w", ctx.Err()))
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

func startPlatformCommand(ctx context.Context, command *sessionCommand, env []string) (*platformProcess, error) {
	path, err := resolveCommand(command.argv[0], env)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, path, command.argv[1:]...)
	cmd.Args[0] = command.argv[0]
	cmd.Env = env
	// Passing the files directly lets Wait return when the command exits, even
	// if a descendant still holds a stream open.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	stopGroup := prepareRunCommand(cmd, command.ownGroup)
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	process := &interactivePlatformProcess{done: make(chan struct{}), cancel: cancel}
	go func() {
		process.waitErr = cmd.Wait()
		// Descendants lose the session when it closes; do not leave them running.
		stopGroup()
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
