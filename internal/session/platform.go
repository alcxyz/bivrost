package session

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
)

const emptyKubeconfig = `apiVersion: v1
kind: Config
clusters: []
users: []
contexts: []
current-context: bivrost-no-aks-configured
`

type platformProxy struct {
	done  <-chan error
	close func()
}

type platformBastion struct {
	port      int
	stateRoot string
	directory string
	sshConfig string
	done      <-chan struct{}
	close     func()
}

type platformProcess struct {
	done <-chan struct{}
	err  func() error
	stop func()
}

type platformServices struct {
	startPodman       func(context.Context, profile.Profile, string) (*podmanSession, error)
	loginPodman       func(context.Context, profile.Profile, *podmanSession) error
	checkRegistry     func(context.Context, profile.Profile) error
	environ           func() []string
	selectShell       func() (string, []string, error)
	reservePort       func(int) (net.Listener, error)
	startProxy        func(context.Context, profile.Profile) (*platformProxy, error)
	openBastion       func(context.Context, profile.Profile) (*platformBastion, error)
	prepareKubeconfig func(context.Context, profile.Profile, string, int) (kubeTarget, error)
	startSSH          func(context.Context, []string) (*platformProcess, error)
	startShell        func(context.Context, string, []string, []string) (*platformProcess, error)
	waitForward       func(context.Context, string, *platformProcess) error
}

func platformConnect(ctx context.Context, c profile.Profile) error {
	settings, err := profile.LoadPromptSettings()
	if err != nil {
		return err
	}
	c.Prompt = &settings
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The CLI deliberately does not turn Ctrl+C into context cancellation for
	// this command. During setup it cancels the connection; after the shell is
	// started, the foreground shell receives and handles it normally.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	var shellRunning atomic.Bool
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-interrupts:
				if !shellRunning.Load() {
					cancel()
					return
				}
			}
		}
	}()

	return platformConnectLoop(ctx, c, &shellRunning, defaultPlatformServices())
}

func platformConnectLoop(ctx context.Context, c profile.Profile, shellRunning *atomic.Bool, services platformServices) error {
	prompt := c.Prompt
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.Prompt = prompt
		err := platformConnectWith(ctx, c, shellRunning, services)
		var reconnect *switchReconnectError
		if !errors.As(err, &reconnect) {
			return err
		}
		c = reconnect.config
		if c.RequiresPIM {
			fmt.Println("This environment requires PIM activation. Activate your eligible access before connecting; this tool does not grant or activate permissions.")
		}
	}
}

func platformConnectWith(ctx context.Context, c profile.Profile, shellRunning *atomic.Bool, services platformServices) error {
	defer func() { diagnostics.Event(ctx, diagnostics.EventCleanup) }()
	if shellinit.EnvironmentValue(services.environ(), "BIVROST_SESSION") != "" {
		return errors.New("a Bivrost platform session is already active; exit it before starting another")
	}
	if err := c.ValidatePlatform(); err != nil {
		return err
	}

	socksReservation, err := services.reservePort(c.SOCKSPort)
	if err != nil {
		return errors.New("SOCKS port is occupied; another Bivrost connection may already be running")
	}
	defer socksReservation.Close()

	apiPort := 0
	var apiReservation net.Listener
	if c.AKS != nil {
		apiReservation, err = services.reservePort(0)
		if err != nil {
			return errors.New("could not reserve a local port for the Kubernetes API")
		}
		defer apiReservation.Close()
		apiPort = apiReservation.Addr().(*net.TCPAddr).Port
	}

	proxy, err := services.startProxy(ctx, c)
	if err != nil {
		return err
	}
	defer proxy.close()

	bastion, err := services.openBastion(ctx, c)
	if err != nil {
		return err
	}
	defer bastion.close()

	var target *kubeTarget
	var kubeconfigPath string
	var kubeUnavailable *kubeUnavailableError
	if c.AKS != nil {
		prepared, err := services.prepareKubeconfig(ctx, c, bastion.directory, apiPort)
		if err == nil {
			target = &prepared
			kubeconfigPath = prepared.path
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else if !errors.As(err, &kubeUnavailable) {
			return err
		} else {
			kubeconfigPath, err = writeEmptyKubeconfig(bastion.directory)
			if err != nil {
				return err
			}
		}
	} else {
		kubeconfigPath, err = writeEmptyKubeconfig(bastion.directory)
		if err != nil {
			return err
		}
	}

	sshArgs := platformSSHArguments(c, bastion.sshConfig, bastion.stateRoot, bastion.port, apiPort, target)
	// Keep the ports reserved throughout credential preparation, then release
	// them immediately before OpenSSH binds its local forwards.
	if err := socksReservation.Close(); err != nil {
		return errors.New("could not release the reserved SOCKS port")
	}
	if apiReservation != nil {
		if err := apiReservation.Close(); err != nil {
			return errors.New("could not release the reserved Kubernetes API port")
		}
	}

	finishForward := diagnostics.Step(ctx, diagnostics.EventSSHForward)
	ssh, err := services.startSSH(ctx, sshArgs)
	if err != nil {
		finishForward(err)
		return errors.New("could not start SSH forwarding")
	}
	defer ssh.stop()
	if err := services.waitForward(ctx, profile.Loopback(c.SOCKSPort), ssh); err != nil {
		finishForward(err)
		return err
	}
	if target != nil {
		if err := services.waitForward(ctx, profile.Loopback(apiPort), ssh); err != nil {
			finishForward(err)
			return err
		}
	}

	finishForward(nil)
	diagnostics.Event(ctx, diagnostics.EventForwardReady)
	shellName, shellArgs, err := services.selectShell()
	if err != nil {
		return err
	}
	shellEnv := platformEnvironment(services.environ(), c.ProxyURL(), kubeconfigPath)
	activation := newACRActivation(ctx, c, services, bastion.directory, shellName)
	activation.kubeconfig = kubeconfigPath
	activation.kubernetesUnavailable = kubeUnavailable != nil
	defer activation.close()
	if c.ACRSession {
		if err := activation.enable(); err != nil {
			return err
		}
		shellEnv = activation.session.environment(shellEnv)
	}
	shellEnv, err = activation.listen(shellEnv)
	if err != nil {
		return err
	}
	shellArgs, shellEnv, err = shellinit.PreparePrompt(bastion.directory, shellName, shellArgs, shellEnv, c)
	if err != nil {
		return err
	}
	fmt.Println(shellinit.Label(c))

	fmt.Println("Platform connection ready. Commands in the local shell use the session HTTPS proxy.")
	if kubeUnavailable != nil {
		fmt.Println("Kubernetes is unavailable for this session: " + kubeUnavailable.Error() + ". KUBECONFIG points to an isolated empty configuration.")
	} else if target == nil {
		fmt.Println("No AKS cluster is configured; KUBECONFIG points to an empty session configuration.")
	} else {
		fmt.Println("Kubernetes API forwarding is ready at " + profile.Loopback(apiPort) + ".")
	}
	if c.ACRSession {
		fmt.Println("Podman is configured for this local shell. Exit closes the session connection; normal engine settings are preserved.")
		fmt.Println(podmanBuildGuidance)
	} else {
		fmt.Println("Enable Podman registry access with bivrost acr enable in supported shells, or reconnect with --acr. Exit to disconnect.")
	}
	fmt.Println("Azure CLI keeps its selected subscription; use --subscription when a command targets another subscription.")

	shellCtx, cancelShell := context.WithCancel(ctx)
	defer cancelShell()
	shellRunning.Store(true)
	finishShell := diagnostics.Step(ctx, diagnostics.EventShell)
	shell, err := services.startShell(shellCtx, shellName, shellArgs, shellEnv)
	if err != nil {
		shellRunning.Store(false)
		finishShell(err)
		return fmt.Errorf("could not start local shell %q: %w", shellName, err)
	}
	defer func() {
		shell.stop()
		shellErr := shell.err()
		if isSwitchShellExit(shellErr) {
			if _, ok := activation.pendingSwitch(); ok {
				shellErr = nil
			}
		}
		finishShell(shellErr)
	}()

	select {
	case <-activation.done:
		cancelShell()
		shell.stop()
		shellRunning.Store(false)
		return errors.New("Podman session disconnected; start bivrost acr connect again")
	case <-shell.done:
		shellRunning.Store(false)
		err := shell.err()
		if isSwitchShellExit(err) {
			if target, ok := activation.pendingSwitch(); ok {
				return &switchReconnectError{config: target}
			}
		}
		if err != nil {
			return fmt.Errorf("local shell exited: %w", err)
		}
		return nil
	case <-proxy.done:
		diagnostics.Event(ctx, diagnostics.EventProxyStopped)
		cancelShell()
		shell.stop()
		shellRunning.Store(false)
		return errors.New("local HTTPS proxy stopped; start the platform session again")
	case <-ssh.done:
		diagnostics.Event(ctx, diagnostics.EventSSHStopped)
		cancelShell()
		shell.stop()
		shellRunning.Store(false)
		return errors.New("SSH forwarding disconnected; start the platform session again")
	case <-bastion.done:
		diagnostics.Event(ctx, diagnostics.EventBastionStopped)
		cancelShell()
		shell.stop()
		shellRunning.Store(false)
		return errors.New("Bastion disconnected; start the platform session again")
	case <-ctx.Done():
		cancelShell()
		shell.stop()
		shellRunning.Store(false)
		return ctx.Err()
	}
}

func defaultPlatformServices() platformServices {
	return platformServices{
		startPodman:   startPodmanSession,
		loginPodman:   loginPodmanSession,
		checkRegistry: registryCheck,
		environ:       os.Environ,
		selectShell:   nativeShell,
		reservePort: func(port int) (net.Listener, error) {
			return net.Listen("tcp4", profile.Loopback(port))
		},
		startProxy: func(ctx context.Context, c profile.Profile) (*platformProxy, error) {
			proxy, err := startProxy(ctx, c)
			if err != nil {
				return nil, err
			}
			return &platformProxy{done: proxy.done, close: proxy.close}, nil
		},
		openBastion: func(ctx context.Context, c profile.Profile) (*platformBastion, error) {
			session, err := openBastion(ctx, c)
			if err != nil {
				return nil, err
			}
			return &platformBastion{
				port:      session.port,
				stateRoot: session.stateRoot,
				directory: session.directory,
				sshConfig: session.sshConfig,
				done:      session.process.done,
				close:     session.close,
			}, nil
		},
		prepareKubeconfig: prepareKubeconfig,
		startSSH: func(ctx context.Context, args []string) (*platformProcess, error) {
			cmd := exec.CommandContext(ctx, "ssh", args...)
			cmd.Stderr = os.Stderr
			process, err := startChild(cmd)
			if err != nil {
				return nil, err
			}
			return &platformProcess{done: process.done, err: func() error { return process.err }, stop: process.stop}, nil
		},
		startShell:  startPlatformShell,
		waitForward: waitPlatformForward,
	}
}

func platformSSHArguments(c profile.Profile, sshConfig, stateRoot string, bastionPort, apiPort int, target *kubeTarget) []string {
	args := append(baseSSHArguments(c, sshConfig, stateRoot, bastionPort), "-N", "-T", "-D", profile.Loopback(c.SOCKSPort))
	if target != nil {
		host := target.host
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		args = append(args, "-L", profile.Loopback(apiPort)+":"+host+":"+target.port)
	}
	return append(args, "127.0.0.1")
}

func platformEnvironment(env []string, proxy, kubeconfigPath string) []string {
	result := make([]string, 0, len(env)+4)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "BIVROST_SESSION", "KUBECONFIG":
			continue
		}
		result = append(result, entry)
	}
	return append(result,
		"HTTPS_PROXY="+proxy,
		"NO_PROXY=127.0.0.1,localhost",
		"KUBECONFIG="+kubeconfigPath,
		"BIVROST_SESSION=1",
	)
}

func writeEmptyKubeconfig(directory string) (string, error) {
	path := filepath.Join(directory, "kubeconfig-empty")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", errors.New("could not create the isolated empty kubeconfig")
	}
	complete := false
	defer func() {
		_ = f.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return "", errors.New("could not secure the isolated empty kubeconfig")
	}
	if _, err := f.Write([]byte(emptyKubeconfig)); err != nil {
		return "", errors.New("could not write the isolated empty kubeconfig")
	}
	if err := f.Close(); err != nil {
		return "", errors.New("could not finish the isolated empty kubeconfig")
	}
	complete = true
	return path, nil
}

func nativeShell() (string, []string, error) {
	if runtime.GOOS == "windows" {
		for _, candidate := range []string{"pwsh", "powershell.exe"} {
			path, err := exec.LookPath(candidate)
			if err == nil {
				return path, []string{"-NoLogo", "-NoExit"}, nil
			}
		}
		return "", nil, errors.New("neither pwsh nor powershell.exe is available")
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if !filepath.IsAbs(shell) {
		return "", nil, errors.New("SHELL must be an absolute executable path")
	}
	path, err := exec.LookPath(shell)
	if err != nil {
		return "", nil, fmt.Errorf("configured shell %q is unavailable", shell)
	}
	return path, []string{"-i"}, nil
}

type interactivePlatformProcess struct {
	done    chan struct{}
	waitErr error
	cancel  context.CancelFunc
}

func startPlatformShell(ctx context.Context, name string, args, env []string) (*platformProcess, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	prepareInteractiveShell(cmd)
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

func waitPlatformForward(ctx context.Context, address string, process *platformProcess) error {
	timeout := time.NewTimer(90 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-process.done:
			return errors.New("SSH exited before its local forwarding ports became ready")
		case <-timeout.C:
			return errors.New("timed out waiting for SSH forwarding; check Azure login, VM access, and SSH authentication")
		case <-ticker.C:
			connection, err := net.DialTimeout("tcp4", address, 200*time.Millisecond)
			if err == nil {
				_ = connection.Close()
				return nil
			}
		}
	}
}
