package session

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
)

type runTestSession struct {
	services                               platformServices
	directory                              string
	bastionDone, sshDone                   chan struct{}
	proxyClosed, bastionClosed, sshStopped atomic.Bool
	commandStopped                         atomic.Bool
	name                                   string
	args, env                              []string
}

// newRunTestSession fakes every external resource; command controls what the
// started command does and returns.
func newRunTestSession(t *testing.T, command func(s *runTestSession) *platformProcess) *runTestSession {
	t.Helper()
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	s := &runTestSession{
		directory:   filepath.Join(t.TempDir(), "session-test"),
		bastionDone: make(chan struct{}),
		sshDone:     make(chan struct{}),
	}
	if err := os.Mkdir(s.directory, 0o700); err != nil {
		t.Fatal(err)
	}
	var closeSSH sync.Once
	s.services = platformServices{
		environ: func() []string { return []string{"PATH=/custom/bin", "KUBECONFIG=/clusters/ambient"} },
		selectShell: func() (string, []string, error) {
			t.Fatal("selectShell called for a run command")
			return "", nil, nil
		},
		reservePort: func(port int) (net.Listener, error) { return net.Listen("tcp4", profile.Loopback(port)) },
		startProxy: func(context.Context, profile.Profile) (*platformProxy, error) {
			return &platformProxy{done: make(chan error), close: func() { s.proxyClosed.Store(true) }}, nil
		},
		openBastion: func(context.Context, profile.Profile) (*platformBastion, error) {
			return &platformBastion{
				port: 32022, stateRoot: filepath.Dir(s.directory), directory: s.directory,
				sshConfig: filepath.Join(s.directory, "ssh_config"), done: s.bastionDone,
				close: func() { s.bastionClosed.Store(true); _ = os.RemoveAll(s.directory) },
			}, nil
		},
		prepareKubeconfig: func(context.Context, profile.Profile, string, int) (kubeTarget, error) {
			return kubeTarget{}, &kubeUnavailableError{reason: kubeUnavailableAKSCredentials}
		},
		startSSH: func(context.Context, []string) (*platformProcess, error) {
			return &platformProcess{
				done: s.sshDone,
				err:  func() error { return nil },
				stop: func() { s.sshStopped.Store(true); closeSSH.Do(func() { close(s.sshDone) }) },
			}, nil
		},
		startShell: func(context.Context, string, []string, []string) (*platformProcess, error) {
			t.Fatal("startShell called for a run command")
			return nil, nil
		},
		startCommand: func(_ context.Context, started *sessionCommand, env []string) (*platformProcess, error) {
			s.name, s.args, s.env = started.argv[0], append([]string(nil), started.argv[1:]...), append([]string(nil), env...)
			return command(s), nil
		},
		waitForward: func(context.Context, string, *platformProcess) error { return nil },
	}
	return s
}

func (s *runTestSession) assertCleanedUp(t *testing.T) {
	t.Helper()
	for name, stopped := range map[string]bool{
		"proxy": s.proxyClosed.Load(), "Bastion": s.bastionClosed.Load(), "SSH": s.sshStopped.Load(),
	} {
		if !stopped {
			t.Errorf("%s was not stopped", name)
		}
	}
	if _, err := os.Stat(s.directory); !os.IsNotExist(err) {
		t.Errorf("session directory remains after cleanup: %v", err)
	}
}

func finishedProcess(err error) *platformProcess {
	done := make(chan struct{})
	close(done)
	return &platformProcess{done: done, err: func() error { return err }, stop: func() {}}
}

func TestRunCommandUsesIsolatedSessionWithoutShellOrController(t *testing.T) {
	s := newRunTestSession(t, func(*runTestSession) *platformProcess { return finishedProcess(nil) })
	c := platformTestConfig(t)
	c.AKS = &profile.AKS{Name: "cluster", ResourceGroup: "rg", Subscription: "sub"}
	var running atomic.Bool
	argv := []string{"kubectl", "get", "pods", "-n", "app"}
	if err := platformSession(context.Background(), c, &running, s.services, false, &sessionCommand{argv: argv}); err != nil {
		t.Fatalf("platformSession() error = %v", err)
	}
	if s.name != "kubectl" || !reflect.DeepEqual(s.args, argv[1:]) {
		t.Errorf("command = %q %q, want %q", s.name, s.args, argv)
	}
	kubeconfig := shellinit.EnvironmentValue(s.env, "KUBECONFIG")
	if kubeconfig == "" || kubeconfig == "/clusters/ambient" || !strings.HasPrefix(kubeconfig, s.directory) {
		t.Errorf("command KUBECONFIG = %q, want isolated session path", kubeconfig)
	}
	if got := shellinit.EnvironmentValue(s.env, "HTTPS_PROXY"); got != c.ProxyURL() {
		t.Errorf("command HTTPS_PROXY = %q, want %q", got, c.ProxyURL())
	}
	if got := shellinit.EnvironmentValue(s.env, "BIVROST_SESSION"); got != "1" {
		t.Errorf("command BIVROST_SESSION = %q, want 1", got)
	}
	if got := shellinit.EnvironmentValue(s.env, "BIVROST_CONTROL_FILE"); got != "" {
		t.Errorf("command received a session controller %q", got)
	}
	if got := shellinit.EnvironmentValue(s.env, "BIVROST_RUN"); got != "1" {
		t.Errorf("command BIVROST_RUN = %q, want 1", got)
	}
	if running.Load() {
		t.Error("command remains marked running")
	}
	s.assertCleanedUp(t)
}

func TestSessionCommandsExplainTheyAreUnavailableInsideRun(t *testing.T) {
	t.Setenv("BIVROST_SESSION", "1")
	t.Setenv("BIVROST_CONTROL_FILE", "")
	t.Setenv("BIVROST_RUN", "1")
	// An empty PATH keeps Azure CLI out of reach if a check were skipped.
	t.Setenv("PATH", t.TempDir())
	for name, run := range map[string]func() error{
		"doctor terraform": func() error {
			command := cli.Command{Kind: cli.TerraformDoctor, Subscription: "sub", Account: "examplestate", Container: "tfstate"}
			return runTerraformDoctor(context.Background(), command, io.Discard)
		},
		"session publish": func() error { return runSessionPublication(context.Background(), "publish") },
		"acr enable":      func() error { return enableSessionACR(context.Background()) },
		"switch": func() error {
			return runSwitch(context.Background(), cli.Command{Kind: cli.Switch, Environment: "example"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil || !strings.Contains(err.Error(), "not available inside bivrost run") {
				t.Fatalf("error = %v, want an explanation that run has no session controller", err)
			}
		})
	}
}

func TestRunCommandReportsCommandFailureAsItsOwnStatus(t *testing.T) {
	commandErr := errors.New("not an exit status")
	s := newRunTestSession(t, func(*runTestSession) *platformProcess { return finishedProcess(commandErr) })
	var running atomic.Bool
	err := runFailure(platformSession(context.Background(), platformTestConfig(t), &running, s.services, false, &sessionCommand{argv: []string{"tool"}}))
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != RunFailureExitCode || !errors.Is(err, commandErr) {
		t.Fatalf("error = %v, want Bivrost failure wrapping the wait error", err)
	}
	s.assertCleanedUp(t)
}

func TestRunCommandStopsCommandWhenBastionDisconnects(t *testing.T) {
	commandDone := make(chan struct{})
	var stopOnce sync.Once
	s := newRunTestSession(t, func(s *runTestSession) *platformProcess {
		close(s.bastionDone)
		return &platformProcess{
			done: commandDone,
			err:  func() error { return nil },
			stop: func() { s.commandStopped.Store(true); stopOnce.Do(func() { close(commandDone) }) },
		}
	})
	var running atomic.Bool
	err := runFailure(platformSession(context.Background(), platformTestConfig(t), &running, s.services, false, &sessionCommand{argv: []string{"tool"}}))
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != RunFailureExitCode || !strings.Contains(err.Error(), "Bastion disconnected") {
		t.Fatalf("error = %v, want Bastion disconnect with status %d", err, RunFailureExitCode)
	}
	if !s.commandStopped.Load() {
		t.Error("command was not stopped")
	}
	s.assertCleanedUp(t)
}

func TestRunCommandCancellationStopsCommandAndCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commandDone := make(chan struct{})
	var stopOnce sync.Once
	s := newRunTestSession(t, func(s *runTestSession) *platformProcess {
		cancel()
		return &platformProcess{
			done: commandDone,
			err:  func() error { return nil },
			stop: func() { s.commandStopped.Store(true); stopOnce.Do(func() { close(commandDone) }) },
		}
	})
	var running atomic.Bool
	err := runFailure(platformSession(ctx, platformTestConfig(t), &running, s.services, false, &sessionCommand{argv: []string{"tool"}}))
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != RunFailureExitCode || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation with status %d", err, RunFailureExitCode)
	}
	if !s.commandStopped.Load() {
		t.Error("command was not stopped")
	}
	s.assertCleanedUp(t)
}

func TestRunCommandTerminationKeepsTunnelsUntilCommandStops(t *testing.T) {
	terminate := make(chan struct{})
	commandDone := make(chan struct{})
	var stopOnce sync.Once
	var tunnelsUpAtStop atomic.Bool
	s := newRunTestSession(t, nil)
	s.services.startCommand = func(_ context.Context, _ *sessionCommand, _ []string) (*platformProcess, error) {
		close(terminate)
		return &platformProcess{
			done: commandDone,
			err:  func() error { return nil },
			stop: func() {
				tunnelsUpAtStop.Store(!s.sshStopped.Load() && !s.bastionClosed.Load() && !s.proxyClosed.Load())
				s.commandStopped.Store(true)
				stopOnce.Do(func() { close(commandDone) })
			},
		}, nil
	}
	var running atomic.Bool
	err := runFailure(platformSession(context.Background(), platformTestConfig(t), &running, s.services, false, &sessionCommand{argv: []string{"tool"}, terminate: terminate}))
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != RunFailureExitCode || !strings.Contains(err.Error(), "terminated") {
		t.Fatalf("error = %v, want termination with status %d", err, RunFailureExitCode)
	}
	if !s.commandStopped.Load() || !tunnelsUpAtStop.Load() {
		t.Errorf("command stopped = %v, tunnels up while stopping = %v; want both", s.commandStopped.Load(), tunnelsUpAtStop.Load())
	}
	s.assertCleanedUp(t)
}

func TestRunCommandTerminatedDuringSetupDoesNotStart(t *testing.T) {
	terminate := make(chan struct{})
	close(terminate)
	s := newRunTestSession(t, func(*runTestSession) *platformProcess {
		t.Fatal("command started after termination")
		return nil
	})
	var running atomic.Bool
	err := runFailure(platformSession(context.Background(), platformTestConfig(t), &running, s.services, false, &sessionCommand{argv: []string{"tool"}, terminate: terminate}))
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != RunFailureExitCode {
		t.Fatalf("error = %v, want status %d", err, RunFailureExitCode)
	}
	s.assertCleanedUp(t)
}

func TestRunCommandRejectsNestedSession(t *testing.T) {
	t.Parallel()
	services := platformServices{
		environ: func() []string { return []string{"BIVROST_SESSION=1"} },
		startProxy: func(context.Context, profile.Profile) (*platformProxy, error) {
			t.Fatal("startProxy called for nested session")
			return nil, nil
		},
	}
	var running atomic.Bool
	err := runFailure(platformSession(context.Background(), profile.Profile{}, &running, services, false, &sessionCommand{argv: []string{"tool"}}))
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != RunFailureExitCode || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("error = %v, want nested-session failure", err)
	}
}

func TestRunPlatformCommandRejectsMissingCommandBeforeSetup(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := runPlatformCommand(context.Background(), profile.Profile{}, []string{"bivrost-test-missing-command"})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != RunCommandNotFound {
		t.Fatalf("error = %v, want status %d", err, RunCommandNotFound)
	}
}

func TestRunFailureKeepsExistingStatus(t *testing.T) {
	t.Parallel()
	if runFailure(nil) != nil {
		t.Error("runFailure(nil) != nil")
	}
	own := &ExitError{Code: 3}
	if got := runFailure(own); got != own {
		t.Errorf("runFailure() replaced command status: %v", got)
	}
	var exit *ExitError
	if err := runFailure(errors.New("setup")); !errors.As(err, &exit) || exit.Code != RunFailureExitCode {
		t.Errorf("runFailure() = %v, want status %d", err, RunFailureExitCode)
	}
}
