package main

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
	startPodman       func(context.Context, config, string) (*podmanSession, error)
	loginPodman       func(context.Context, config, *podmanSession) error
	checkRegistry     func(context.Context, config) error
	environ           func() []string
	selectShell       func() (string, []string, error)
	reservePort       func(int) (net.Listener, error)
	startProxy        func(context.Context, config) (*platformProxy, error)
	openBastion       func(context.Context, config) (*platformBastion, error)
	prepareKubeconfig func(context.Context, config, string, int) (kubeTarget, error)
	startSSH          func(context.Context, []string) (*platformProcess, error)
	startShell        func(context.Context, string, []string, []string) (*platformProcess, error)
	waitForward       func(context.Context, string, *platformProcess) error
}

func platformConnect(ctx context.Context, c config) error {
	settings, err := loadPromptSettings()
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

	return platformConnectWith(ctx, c, &shellRunning, defaultPlatformServices())
}

func platformConnectWith(ctx context.Context, c config, shellRunning *atomic.Bool, services platformServices) error {
	defer func() { diagnosticEvent(ctx, eventCleanup) }()
	if environmentValue(services.environ(), "BIVROST_SESSION") != "" {
		return errors.New("a Bivrost platform session is already active; exit it before starting another")
	}
	if err := c.validatePlatform(); err != nil {
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
	if c.AKS != nil {
		prepared, err := services.prepareKubeconfig(ctx, c, bastion.directory, apiPort)
		if err != nil {
			return err
		}
		target = &prepared
		kubeconfigPath = prepared.path
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

	finishForward := diagnosticStep(ctx, eventSSHForward)
	ssh, err := services.startSSH(ctx, sshArgs)
	if err != nil {
		finishForward(err)
		return errors.New("could not start SSH forwarding")
	}
	defer ssh.stop()
	if err := services.waitForward(ctx, loopback(c.SOCKSPort), ssh); err != nil {
		finishForward(err)
		return err
	}
	if target != nil {
		if err := services.waitForward(ctx, loopback(apiPort), ssh); err != nil {
			finishForward(err)
			return err
		}
	}

	finishForward(nil)
	diagnosticEvent(ctx, eventForwardReady)
	shellName, shellArgs, err := services.selectShell()
	if err != nil {
		return err
	}
	shellEnv := platformEnvironment(services.environ(), c.proxyURL(), kubeconfigPath)
	activation := newACRActivation(ctx, c, services, bastion.directory, shellName)
	activation.kubeconfig = kubeconfigPath
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
	shellArgs, shellEnv, err = prepareSessionPrompt(bastion.directory, shellName, shellArgs, shellEnv, c)
	if err != nil {
		return err
	}
	fmt.Println(sessionLabel(c))

	fmt.Println("Platform connection ready. Commands in the local shell use the session HTTPS proxy.")
	if target == nil {
		fmt.Println("No AKS cluster is configured; KUBECONFIG points to an empty session configuration.")
	} else {
		fmt.Println("Kubernetes API forwarding is ready at " + loopback(apiPort) + ".")
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
	finishShell := diagnosticStep(ctx, eventShell)
	shell, err := services.startShell(shellCtx, shellName, shellArgs, shellEnv)
	if err != nil {
		shellRunning.Store(false)
		finishShell(err)
		return fmt.Errorf("could not start local shell %q: %w", shellName, err)
	}
	defer func() { shell.stop(); finishShell(shell.err()) }()

	select {
	case <-activation.done:
		cancelShell()
		shell.stop()
		shellRunning.Store(false)
		return errors.New("Podman session disconnected; start bivrost acr connect again")
	case <-shell.done:
		shellRunning.Store(false)
		if err := shell.err(); err != nil {
			return fmt.Errorf("local shell exited: %w", err)
		}
		return nil
	case <-proxy.done:
		diagnosticEvent(ctx, eventProxyStopped)
		cancelShell()
		shell.stop()
		shellRunning.Store(false)
		return errors.New("local HTTPS proxy stopped; start the platform session again")
	case <-ssh.done:
		diagnosticEvent(ctx, eventSSHStopped)
		cancelShell()
		shell.stop()
		shellRunning.Store(false)
		return errors.New("SSH forwarding disconnected; start the platform session again")
	case <-bastion.done:
		diagnosticEvent(ctx, eventBastionStopped)
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
			return net.Listen("tcp4", loopback(port))
		},
		startProxy: func(ctx context.Context, c config) (*platformProxy, error) {
			proxy, err := startProxy(ctx, c)
			if err != nil {
				return nil, err
			}
			return &platformProxy{done: proxy.done, close: proxy.close}, nil
		},
		openBastion: func(ctx context.Context, c config) (*platformBastion, error) {
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

func platformSSHArguments(c config, sshConfig, stateRoot string, bastionPort, apiPort int, target *kubeTarget) []string {
	args := append(baseSSHArguments(c, sshConfig, stateRoot, bastionPort), "-N", "-T", "-D", loopback(c.SOCKSPort))
	if target != nil {
		host := target.host
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		args = append(args, "-L", loopback(apiPort)+":"+host+":"+target.port)
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

func environmentValue(env []string, name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		key, value, found := strings.Cut(env[i], "=")
		if found && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func writeEmptyKubeconfig(directory string) (string, error) {
	path := filepath.Join(directory, "kubeconfig-empty")
	if err := os.WriteFile(path, []byte(emptyKubeconfig), 0o600); err != nil {
		return "", errors.New("could not create the isolated empty kubeconfig")
	}
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
