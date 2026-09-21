package session

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
)

func TestACRSessionShellLifecycle(t *testing.T) {
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	setupFailure := errors.New("registry setup failed")
	for _, test := range []struct {
		name       string
		remote     bool
		NoLogin    bool
		failAt     string
		disconnect bool
		wantCalls  []string
	}{
		{name: "native shell exit", wantCalls: []string{"podman", "check", "login", "shell"}},
		{name: "machine shell exit", remote: true, wantCalls: []string{"podman", "check", "login", "shell"}},
		{name: "no login still checks registry", remote: true, NoLogin: true, wantCalls: []string{"podman", "check", "shell"}},
		{name: "registry check failure", failAt: "check", wantCalls: []string{"podman", "check"}},
		{name: "registry login failure", failAt: "login", wantCalls: []string{"podman", "check", "login"}},
		{name: "machine disconnect", remote: true, disconnect: true, wantCalls: []string{"podman", "check", "login", "shell"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			configuration := filepath.Join(directory, "podman-session.conf")
			if err := os.WriteFile(configuration, []byte("[engine]\nremote = false\n"), 0600); err != nil {
				t.Fatal(err)
			}
			podmanDone := make(chan struct{})
			session := &podmanSession{configuration: configuration, done: podmanDone}
			if test.remote {
				session.host = "ssh://user@127.0.0.1:32022/run/user/1000/bivrost-session.abcdefghijkl/api.sock"
				session.identity = filepath.Join(directory, "machine-key")
			}
			c := platformTestConfig(t)
			c.ACRSession = true
			c.SkipRegistryLogin = test.NoLogin
			ambient := []string{
				"PATH=/custom/bin", "KUBECONFIG=/clusters/production",
				"CONTAINER_HOST=ssh://ambient", "CONTAINER_CONNECTION=normal-machine",
				"CONTAINER_SSHKEY=/ambient/key", "CONTAINERS_CONF_OVERRIDE=/ambient/config",
			}
			original := append([]string(nil), ambient...)
			var calls, shellEnv []string
			var shellCtx context.Context
			var proxyClosed, bastionClosed, sshStopped, shellStopped bool
			services := platformServices{
				environ:     func() []string { return ambient },
				selectShell: func() (string, []string, error) { return "/bin/sh", []string{"-i"}, nil },
				reservePort: func(port int) (net.Listener, error) { return net.Listen("tcp4", profile.Loopback(port)) },
				startProxy: func(context.Context, profile.Profile) (*platformProxy, error) {
					return &platformProxy{close: func() { proxyClosed = true }}, nil
				},
				openBastion: func(context.Context, profile.Profile) (*platformBastion, error) {
					return &platformBastion{
						port: 32022, stateRoot: filepath.Dir(directory), directory: directory,
						sshConfig: filepath.Join(directory, "ssh_config"),
						close: func() {
							bastionClosed = true
							if _, err := os.Stat(configuration); !os.IsNotExist(err) {
								t.Errorf("Podman configuration was not removed before Bastion cleanup: %v", err)
							}
						},
					}, nil
				},
				startSSH: func(context.Context, []string) (*platformProcess, error) {
					return &platformProcess{err: func() error { return nil }, stop: func() { sshStopped = true }}, nil
				},
				waitForward: func(context.Context, string, *platformProcess) error { return nil },
				startPodman: func(_ context.Context, _ profile.Profile, gotDirectory string) (*podmanSession, error) {
					calls = append(calls, "podman")
					if gotDirectory != directory {
						t.Errorf("Podman session directory = %q, want %q", gotDirectory, directory)
					}
					return session, nil
				},
				checkRegistry: func(context.Context, profile.Profile) error {
					calls = append(calls, "check")
					if test.failAt == "check" {
						return setupFailure
					}
					return nil
				},
				loginPodman: func(_ context.Context, _ profile.Profile, got *podmanSession) error {
					calls = append(calls, "login")
					if got != session {
						t.Error("login did not receive the owned Podman session")
					}
					if test.failAt == "login" {
						return setupFailure
					}
					return nil
				},
				startShell: func(ctx context.Context, _ string, _ []string, env []string) (*platformProcess, error) {
					calls = append(calls, "shell")
					shellCtx, shellEnv = ctx, append([]string(nil), env...)
					shellDone := make(chan struct{})
					if test.disconnect {
						close(podmanDone)
					} else {
						close(shellDone)
					}
					return &platformProcess{
						done: shellDone, err: func() error { return nil },
						stop: func() {
							shellStopped = true
							if test.disconnect && ctx.Err() != context.Canceled {
								t.Error("Podman disconnect stopped the shell before canceling its context")
							}
						},
					}, nil
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var shellRunning atomic.Bool
			err := platformConnectWith(ctx, c, &shellRunning, services)
			switch {
			case test.failAt != "":
				if !errors.Is(err, setupFailure) {
					t.Errorf("session error = %v, want registry setup failure", err)
				}
			case test.disconnect:
				if err == nil || !strings.Contains(err.Error(), "Podman session disconnected") {
					t.Errorf("session error = %v, want Podman disconnect", err)
				}
				if shellCtx == nil || shellCtx.Err() != context.Canceled {
					t.Error("Podman disconnect did not cancel the shell context")
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(calls, test.wantCalls) {
				t.Errorf("session calls = %v, want %v", calls, test.wantCalls)
			}
			if !proxyClosed || !bastionClosed || !sshStopped || shellRunning.Load() {
				t.Errorf("cleanup: proxy=%v Bastion=%v SSH=%v shellRunning=%v", proxyClosed, bastionClosed, sshStopped, shellRunning.Load())
			}
			if !reflect.DeepEqual(ambient, original) {
				t.Error("session changed the parent environment")
			}
			if test.failAt != "" {
				return
			}
			if !shellStopped {
				t.Error("shell was not stopped")
			}
			wantEnv := map[string]string{
				"PATH": "/custom/bin", "BIVROST_SESSION": "1", "HTTPS_PROXY": c.ProxyURL(),
				"KUBECONFIG":           filepath.Join(directory, "kubeconfig-empty"),
				"CONTAINER_CONNECTION": "", "CONTAINER_HOST": "", "CONTAINER_SSHKEY": "",
				"CONTAINERS_CONF_OVERRIDE": configuration,
			}
			if test.remote {
				wantEnv["CONTAINER_HOST"] = session.host
				wantEnv["CONTAINER_SSHKEY"] = session.identity
			}
			for key, want := range wantEnv {
				if got := shellinit.EnvironmentValue(shellEnv, key); got != want {
					t.Errorf("shell %s = %q, want %q", key, got, want)
				}
			}
		})
	}
}
