package session

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	profile "github.com/alcxyz/bivrost/internal/config"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
)

func TestPlatformSSHArgumentsAddOnlyConfiguredForwards(t *testing.T) {
	t.Parallel()
	c := profile.Profile{
		VMResourceID: "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/jump",
		SOCKSPort:    18081,
		SSHUser:      "azureuser",
		IdentityFile: "/keys/jump_ed25519",
	}
	base := baseSSHArguments(c, "/tmp/session/ssh_config", "/tmp/bivrost", 32022)

	withoutAKS := platformSSHArguments(c, "/tmp/session/ssh_config", "/tmp/bivrost", 32022, 0, nil)
	wantSuffix := []string{"-N", "-T", "-D", "127.0.0.1:18081", "127.0.0.1"}
	if !reflect.DeepEqual(withoutAKS[:len(base)], base) || !reflect.DeepEqual(withoutAKS[len(base):], wantSuffix) {
		t.Fatalf("arguments without AKS = %q, want base plus %q", withoutAKS, wantSuffix)
	}
	if containsString(withoutAKS, "-L") {
		t.Fatalf("arguments without AKS contain a local API forward: %q", withoutAKS)
	}

	target := &kubeTarget{host: "private.cluster.example", port: "443"}
	withAKS := platformSSHArguments(c, "/tmp/session/ssh_config", "/tmp/bivrost", 32022, 28443, target)
	if got := argumentAfter(t, withAKS, "-D"); got != "127.0.0.1:18081" {
		t.Errorf("SOCKS forward = %q", got)
	}
	if got := argumentAfter(t, withAKS, "-L"); got != "127.0.0.1:28443:private.cluster.example:443" {
		t.Errorf("Kubernetes API forward = %q", got)
	}
	if got := withAKS[len(withAKS)-1]; got != "127.0.0.1" {
		t.Errorf("SSH destination = %q", got)
	}
}

func TestPlatformEnvironmentIsIsolatedWithoutMutatingParent(t *testing.T) {
	t.Parallel()
	input := []string{
		"PATH=/custom/bin",
		"HTTPS_PROXY=http://old.example:8080",
		"HTTP_PROXY=http://old-upper.example:8080",
		"http_proxy=http://old-lower.example:8080",
		"ALL_PROXY=socks5://old.example:1080",
		"NO_PROXY=old.example",
		"KUBECONFIG=/clusters/production",
		"kubeconfig=/clusters/duplicate",
		"BIVROST_SESSION=old",
		"AZURE_CONFIG_DIR=/users/me/.azure",
		"DOCKER_CONTEXT=desktop-linux",
	}
	original := append([]string(nil), input...)

	got := platformEnvironment(input, "http://127.0.0.1:18080", "/tmp/session/kubeconfig-empty")
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("platformEnvironment mutated its input: got %q, want %q", input, original)
	}
	wantValues := map[string]string{
		"HTTPS_PROXY":      "http://127.0.0.1:18080",
		"NO_PROXY":         "127.0.0.1,localhost",
		"KUBECONFIG":       "/tmp/session/kubeconfig-empty",
		"BIVROST_SESSION":  "1",
		"AZURE_CONFIG_DIR": "/users/me/.azure",
		"DOCKER_CONTEXT":   "desktop-linux",
	}
	counts := make(map[string]int)
	for _, entry := range got {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		upper := strings.ToUpper(key)
		if upper == "ALL_PROXY" {
			t.Errorf("session environment retained %q", entry)
		}
		if want, ok := wantValues[upper]; ok {
			counts[upper]++
			if key != upper || value != want {
				t.Errorf("environment entry = %q, want %s=%s", entry, upper, want)
			}
		}
	}
	for key := range wantValues {
		if counts[key] != 1 {
			t.Errorf("session environment has %d %s entries, want one: %q", counts[key], key, got)
		}
	}
	for _, inherited := range []string{
		"HTTP_PROXY=http://old-upper.example:8080",
		"http_proxy=http://old-lower.example:8080",
	} {
		if !containsString(got, inherited) {
			t.Errorf("session environment changed or removed inherited plain HTTP proxy %q: %q", inherited, got)
		}
	}
}

func TestWriteEmptyKubeconfigPreventsAmbientContextFallback(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path, err := writeEmptyKubeconfig(directory)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(directory, "kubeconfig-empty") {
		t.Fatalf("path = %q", path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != emptyKubeconfig || !strings.Contains(string(contents), "bivrost-no-aks-configured") {
		t.Fatalf("empty kubeconfig contents = %q", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("empty kubeconfig mode = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteEmptyKubeconfigRejectsExistingPath(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "kubeconfig-empty")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeEmptyKubeconfig(directory); err == nil {
		t.Fatal("writeEmptyKubeconfig() replaced an existing session path")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "existing" {
		t.Fatalf("existing path contents = %q, want unchanged", contents)
	}
}

func TestNativeShellRejectsRelativeConfiguredShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SHELL selects the native shell only on Unix")
	}
	t.Setenv("SHELL", "sh")
	if _, _, err := nativeShell(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("nativeShell() error = %v, want absolute-path error", err)
	}
}

func TestNativeShellUsesConfiguredAbsoluteExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SHELL selects the native shell only on Unix")
	}
	shell := filepath.Join(t.TempDir(), "session-shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	name, args, err := nativeShell()
	if err != nil {
		t.Fatal(err)
	}
	if name != shell || !reflect.DeepEqual(args, []string{"-i"}) {
		t.Fatalf("nativeShell() = %q %q, want %q -i", name, args, shell)
	}
}

func TestPlatformConnectRejectsNestedSessionBeforeStartingResources(t *testing.T) {
	t.Parallel()
	services := platformServices{
		environ: func() []string { return []string{"BIVROST_SESSION=1"} },
		startProxy: func(context.Context, profile.Profile) (*platformProxy, error) {
			t.Fatal("startProxy called for nested session")
			return nil, nil
		},
	}
	var shellRunning atomic.Bool
	err := platformConnectWith(context.Background(), profile.Profile{}, &shellRunning, services)
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("platformConnectWith() error = %v, want nested-session error", err)
	}
}

func TestPlatformSessionStopsShellAndOwnedResourcesWhenSSHEnds(t *testing.T) {
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	directory := filepath.Join(t.TempDir(), "session-test")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	c := platformTestConfig(t)
	proxyDone := make(chan error)
	bastionDone := make(chan struct{})
	sshDone := make(chan struct{})
	shellDone := make(chan struct{})
	var proxyClosed, bastionClosed, sshStopped, shellStopped atomic.Bool
	var closeSSH, closeShell sync.Once
	var capturedEnv []string
	var capturedSSH []string

	services := platformServices{
		environ:     func() []string { return []string{"PATH=/custom/bin", "KUBECONFIG=/clusters/production"} },
		selectShell: func() (string, []string, error) { return "/bin/sh", []string{"-i"}, nil },
		reservePort: func(port int) (net.Listener, error) { return net.Listen("tcp4", profile.Loopback(port)) },
		startProxy: func(context.Context, profile.Profile) (*platformProxy, error) {
			return &platformProxy{done: proxyDone, close: func() { proxyClosed.Store(true) }}, nil
		},
		openBastion: func(context.Context, profile.Profile) (*platformBastion, error) {
			return &platformBastion{
				port: 32022, stateRoot: filepath.Dir(directory), directory: directory,
				sshConfig: filepath.Join(directory, "ssh_config"), done: bastionDone,
				close: func() { bastionClosed.Store(true); _ = os.RemoveAll(directory) },
			}, nil
		},
		prepareKubeconfig: func(context.Context, profile.Profile, string, int) (kubeTarget, error) {
			t.Fatal("prepareKubeconfig called without AKS configuration")
			return kubeTarget{}, nil
		},
		startSSH: func(_ context.Context, args []string) (*platformProcess, error) {
			capturedSSH = append([]string(nil), args...)
			return &platformProcess{
				done: sshDone,
				err:  func() error { return nil },
				stop: func() { sshStopped.Store(true); closeSSH.Do(func() { close(sshDone) }) },
			}, nil
		},
		startShell: func(_ context.Context, _ string, _ []string, env []string) (*platformProcess, error) {
			capturedEnv = append([]string(nil), env...)
			closeSSH.Do(func() { close(sshDone) })
			return &platformProcess{
				done: shellDone,
				err:  func() error { return nil },
				stop: func() { shellStopped.Store(true); closeShell.Do(func() { close(shellDone) }) },
			}, nil
		},
		waitForward: func(context.Context, string, *platformProcess) error { return nil },
	}

	var shellRunning atomic.Bool
	err := platformConnectWith(context.Background(), c, &shellRunning, services)
	if err == nil || !strings.Contains(err.Error(), "SSH forwarding disconnected") {
		t.Fatalf("platformConnectWith() error = %v, want SSH disconnect", err)
	}
	if shellRunning.Load() {
		t.Error("shell remains marked active after failure")
	}
	for name, stopped := range map[string]bool{
		"proxy": proxyClosed.Load(), "Bastion": bastionClosed.Load(),
		"SSH": sshStopped.Load(), "shell": shellStopped.Load(),
	} {
		if !stopped {
			t.Errorf("%s was not stopped", name)
		}
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Errorf("session directory remains after cleanup: %v", err)
	}
	if containsString(capturedSSH, "-L") {
		t.Errorf("SSH arguments contain an AKS forward without AKS: %q", capturedSSH)
	}
	kubePath := shellinit.EnvironmentValue(capturedEnv, "KUBECONFIG")
	if kubePath == "" || kubePath == "/clusters/production" {
		t.Errorf("shell KUBECONFIG = %q, want isolated session path", kubePath)
	}
	if got := shellinit.EnvironmentValue(capturedEnv, "BIVROST_SESSION"); got != "1" {
		t.Errorf("shell BIVROST_SESSION = %q, want 1", got)
	}
}

func TestPlatformSessionDegradesOnlyKubernetesWhenAKSPreparationIsUnavailable(t *testing.T) {
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	directory := filepath.Join(t.TempDir(), "session-test")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	c := platformTestConfig(t)
	c.AKS = &profile.AKS{Name: "cluster-one", ResourceGroup: "cluster-rg", Subscription: "cluster-subscription"}
	c.PrivateHosts = []string{"database.private.example", "vault.private.example"}
	proxyDone := make(chan error)
	bastionDone := make(chan struct{})
	sshDone := make(chan struct{})
	var proxyClosed, bastionClosed, sshStopped atomic.Bool
	var closeSSH sync.Once
	var capturedSSH, capturedEnv, waitedAddresses []string
	var status doctorSessionStatus
	var emptyContents []byte

	services := platformServices{
		environ:     func() []string { return []string{"PATH=/custom/bin", "KUBECONFIG=/clusters/ambient"} },
		selectShell: func() (string, []string, error) { return "/bin/bash", []string{"-i"}, nil },
		reservePort: func(port int) (net.Listener, error) { return net.Listen("tcp4", profile.Loopback(port)) },
		startProxy: func(_ context.Context, got profile.Profile) (*platformProxy, error) {
			if !reflect.DeepEqual(got.PrivateHosts, c.PrivateHosts) {
				t.Fatalf("proxy private hosts = %q, want %q", got.PrivateHosts, c.PrivateHosts)
			}
			return &platformProxy{done: proxyDone, close: func() { proxyClosed.Store(true) }}, nil
		},
		openBastion: func(context.Context, profile.Profile) (*platformBastion, error) {
			return &platformBastion{
				port: 32022, stateRoot: filepath.Dir(directory), directory: directory,
				sshConfig: filepath.Join(directory, "ssh_config"), done: bastionDone,
				close: func() { bastionClosed.Store(true); _ = os.RemoveAll(directory) },
			}, nil
		},
		prepareKubeconfig: func(_ context.Context, got profile.Profile, gotDirectory string, localPort int) (kubeTarget, error) {
			if !reflect.DeepEqual(got.AKS, c.AKS) || gotDirectory != directory || localPort == 0 {
				t.Fatalf("prepareKubeconfig() received changed AKS configuration or invalid session target")
			}
			return kubeTarget{}, &kubeUnavailableError{reason: kubeUnavailableAKSCredentials}
		},
		startSSH: func(_ context.Context, args []string) (*platformProcess, error) {
			capturedSSH = append([]string(nil), args...)
			return &platformProcess{
				done: sshDone,
				err:  func() error { return nil },
				stop: func() { sshStopped.Store(true); closeSSH.Do(func() { close(sshDone) }) },
			}, nil
		},
		startShell: func(_ context.Context, _ string, _ []string, env []string) (*platformProcess, error) {
			capturedEnv = append([]string(nil), env...)
			kubeconfig := shellinit.EnvironmentValue(env, "KUBECONFIG")
			var err error
			emptyContents, err = os.ReadFile(kubeconfig)
			if err != nil {
				t.Fatal(err)
			}
			control := readActivationControl(t, shellinit.EnvironmentValue(env, "BIVROST_CONTROL_FILE"))
			status = doctorTestStatus(t, control)
			done := make(chan struct{})
			close(done)
			return &platformProcess{done: done, err: func() error { return nil }, stop: func() {}}, nil
		},
		waitForward: func(_ context.Context, address string, _ *platformProcess) error {
			waitedAddresses = append(waitedAddresses, address)
			return nil
		},
	}

	var shellRunning atomic.Bool
	if err := platformConnectWith(context.Background(), c, &shellRunning, services); err != nil {
		t.Fatalf("platformConnectWith() error = %v", err)
	}
	if containsString(capturedSSH, "-L") {
		t.Errorf("fallback SSH arguments contain a Kubernetes API forward: %q", capturedSSH)
	}
	if len(waitedAddresses) != 1 || waitedAddresses[0] != profile.Loopback(c.SOCKSPort) {
		t.Errorf("waited for %q, want only the shared SOCKS forward", waitedAddresses)
	}
	if kubePath := shellinit.EnvironmentValue(capturedEnv, "KUBECONFIG"); kubePath == "" || kubePath == "/clusters/ambient" {
		t.Errorf("fallback KUBECONFIG = %q, want isolated session path", kubePath)
	}
	if string(emptyContents) != emptyKubeconfig {
		t.Fatalf("fallback kubeconfig contents = %q", emptyContents)
	}
	if !status.KubernetesUnavailable || !reflect.DeepEqual(status.Config.AKS, c.AKS) {
		t.Errorf("session status = %+v, want configured but unavailable Kubernetes", status)
	}
	for name, closed := range map[string]bool{
		"proxy": proxyClosed.Load(), "Bastion": bastionClosed.Load(), "SSH": sshStopped.Load(),
	} {
		if !closed {
			t.Errorf("%s was not cleaned up", name)
		}
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Errorf("session directory remains after cleanup: %v", err)
	}
}

func TestPlatformSessionKeepsHealthyKubernetesForward(t *testing.T) {
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	directory := filepath.Join(t.TempDir(), "session-test")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	c := platformTestConfig(t)
	c.AKS = &profile.AKS{Name: "cluster-one", ResourceGroup: "cluster-rg", Subscription: "cluster-subscription"}
	proxyDone := make(chan error)
	bastionDone := make(chan struct{})
	sshDone := make(chan struct{})
	var closeSSH sync.Once
	var capturedSSH []string
	var waited int
	var status doctorSessionStatus
	preparedPath := filepath.Join(directory, kubeconfigFilename)

	services := platformServices{
		environ:     func() []string { return []string{"PATH=/custom/bin"} },
		selectShell: func() (string, []string, error) { return "/bin/bash", []string{"-i"}, nil },
		reservePort: func(port int) (net.Listener, error) { return net.Listen("tcp4", profile.Loopback(port)) },
		startProxy: func(context.Context, profile.Profile) (*platformProxy, error) {
			return &platformProxy{done: proxyDone, close: func() {}}, nil
		},
		openBastion: func(context.Context, profile.Profile) (*platformBastion, error) {
			return &platformBastion{port: 32022, stateRoot: filepath.Dir(directory), directory: directory, sshConfig: filepath.Join(directory, "ssh_config"), done: bastionDone, close: func() { _ = os.RemoveAll(directory) }}, nil
		},
		prepareKubeconfig: func(context.Context, profile.Profile, string, int) (kubeTarget, error) {
			if err := os.WriteFile(preparedPath, []byte("prepared"), 0o600); err != nil {
				t.Fatal(err)
			}
			return kubeTarget{path: preparedPath, host: "api.private.example", port: "443"}, nil
		},
		startSSH: func(_ context.Context, args []string) (*platformProcess, error) {
			capturedSSH = append([]string(nil), args...)
			return &platformProcess{done: sshDone, err: func() error { return nil }, stop: func() { closeSSH.Do(func() { close(sshDone) }) }}, nil
		},
		startShell: func(_ context.Context, _ string, _ []string, env []string) (*platformProcess, error) {
			if got := shellinit.EnvironmentValue(env, "KUBECONFIG"); got != preparedPath {
				t.Fatalf("healthy KUBECONFIG = %q, want %q", got, preparedPath)
			}
			control := readActivationControl(t, shellinit.EnvironmentValue(env, "BIVROST_CONTROL_FILE"))
			status = doctorTestStatus(t, control)
			done := make(chan struct{})
			close(done)
			return &platformProcess{done: done, err: func() error { return nil }, stop: func() {}}, nil
		},
		waitForward: func(context.Context, string, *platformProcess) error { waited++; return nil },
	}

	var shellRunning atomic.Bool
	if err := platformConnectWith(context.Background(), c, &shellRunning, services); err != nil {
		t.Fatalf("platformConnectWith() error = %v", err)
	}
	if !containsString(capturedSSH, "-L") || waited != 2 {
		t.Errorf("healthy Kubernetes forwarding args=%q waits=%d, want -L and two listeners", capturedSSH, waited)
	}
	if status.KubernetesUnavailable {
		t.Error("healthy Kubernetes session was marked unavailable")
	}
}

func TestPlatformSessionAbortsOnFatalKubeconfigFailures(t *testing.T) {
	tests := []struct {
		name       string
		prepareErr error
		cancelRace bool
	}{
		{name: "unsafe generated kubeconfig", prepareErr: errors.New("kubeconfig API server is unsafe")},
		{name: "direct cancellation", prepareErr: context.Canceled},
		{name: "cancellation wins over fallback", prepareErr: &kubeUnavailableError{reason: kubeUnavailableAKSCredentials}, cancelRace: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "session-test")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			c := platformTestConfig(t)
			c.AKS = &profile.AKS{Name: "cluster-one", ResourceGroup: "cluster-rg", Subscription: "cluster-subscription"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var proxyClosed, bastionClosed atomic.Bool
			var sshStarted atomic.Bool
			services := platformServices{
				environ:     func() []string { return []string{"PATH=/custom/bin"} },
				reservePort: func(port int) (net.Listener, error) { return net.Listen("tcp4", profile.Loopback(port)) },
				startProxy: func(context.Context, profile.Profile) (*platformProxy, error) {
					return &platformProxy{done: make(chan error), close: func() { proxyClosed.Store(true) }}, nil
				},
				openBastion: func(context.Context, profile.Profile) (*platformBastion, error) {
					return &platformBastion{port: 32022, stateRoot: filepath.Dir(directory), directory: directory, sshConfig: filepath.Join(directory, "ssh_config"), done: make(chan struct{}), close: func() { bastionClosed.Store(true); _ = os.RemoveAll(directory) }}, nil
				},
				prepareKubeconfig: func(context.Context, profile.Profile, string, int) (kubeTarget, error) {
					if test.cancelRace {
						cancel()
					}
					return kubeTarget{}, test.prepareErr
				},
				startSSH: func(context.Context, []string) (*platformProcess, error) {
					sshStarted.Store(true)
					return nil, errors.New("unexpected SSH start")
				},
			}
			var shellRunning atomic.Bool
			err := platformConnectWith(ctx, c, &shellRunning, services)
			if test.cancelRace || errors.Is(test.prepareErr, context.Canceled) {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("platformConnectWith() error = %v, want context cancellation", err)
				}
			} else if !errors.Is(err, test.prepareErr) {
				t.Fatalf("platformConnectWith() error = %v, want fatal kubeconfig error", err)
			}
			if sshStarted.Load() {
				t.Error("SSH started after fatal kubeconfig failure")
			}
			if !proxyClosed.Load() || !bastionClosed.Load() {
				t.Error("owned transport setup was not cleaned up after fatal kubeconfig failure")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Errorf("session directory remains after fatal failure: %v", err)
			}
		})
	}
}

func platformTestConfig(t *testing.T) profile.Profile {
	t.Helper()
	proxyPort := availablePort(t)
	socksPort := availablePort(t)
	for socksPort == proxyPort {
		socksPort = availablePort(t)
	}
	return profile.Profile{
		Registry:             "exampleregistry",
		RegistrySubscription: "registry-subscription",
		Subscription:         "subscription",
		BastionName:          "bastion",
		BastionResourceGroup: "network-rg",
		VMResourceID:         "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/jump",
		ProxyPort:            proxyPort,
		SOCKSPort:            socksPort,
	}
}
