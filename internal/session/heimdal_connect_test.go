package session

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	profile "github.com/alcxyz/bivrost/internal/config"
)

type heimdalConnectState struct {
	fetch            func(context.Context, profile.Profile) ([]string, error)
	secondProxyError error
	events           []string
	proxyProfiles    []profile.Profile
	proxyCloses      []int
	controller       []profile.Profile
	bastionOpens     int
	bastionCloses    int
	sshStarts        int
	sshStops         int
	shellStarts      int
}

func (s *heimdalConnectState) record(event string) {
	s.events = append(s.events, event)
}

func heimdalConnectServices(t *testing.T, state *heimdalConnectState) platformServices {
	t.Helper()
	return platformServices{
		environ:     func() []string { return []string{"PATH=/custom/bin"} },
		selectShell: func() (string, []string, error) { state.record("select-shell"); return "/bin/sh", []string{"-i"}, nil },
		reservePort: func(port int) (net.Listener, error) { return net.Listen("tcp4", profile.Loopback(port)) },
		startProxy: func(_ context.Context, c profile.Profile) (*platformProxy, error) {
			c.PrivateHosts = append([]string(nil), c.PrivateHosts...)
			state.proxyProfiles = append(state.proxyProfiles, c)
			attempt := len(state.proxyProfiles)
			state.record(fmt.Sprintf("proxy-%d", attempt))
			if attempt == 2 && state.secondProxyError != nil {
				return nil, state.secondProxyError
			}
			state.proxyCloses = append(state.proxyCloses, 0)
			index := len(state.proxyCloses) - 1
			return &platformProxy{done: make(chan error), close: func() {
				state.proxyCloses[index]++
				state.record(fmt.Sprintf("close-proxy-%d", attempt))
			}}, nil
		},
		openBastion: func(context.Context, profile.Profile) (*platformBastion, error) {
			state.bastionOpens++
			state.record("bastion")
			directory := t.TempDir()
			return &platformBastion{
				port:      32022,
				stateRoot: filepath.Dir(directory),
				directory: directory,
				sshConfig: filepath.Join(directory, "ssh_config"),
				done:      make(chan struct{}),
				close: func() {
					state.bastionCloses++
					state.record("close-bastion")
					_ = os.RemoveAll(directory)
				},
			}, nil
		},
		prepareKubeconfig: func(context.Context, profile.Profile, string, int) (kubeTarget, error) {
			t.Fatal("prepareKubeconfig called without AKS configuration")
			return kubeTarget{}, nil
		},
		startSSH: func(context.Context, []string) (*platformProcess, error) {
			state.sshStarts++
			state.record("ssh")
			done := make(chan struct{})
			var once sync.Once
			return &platformProcess{
				done: done,
				err:  func() error { return nil },
				stop: func() {
					once.Do(func() {
						state.sshStops++
						state.record("stop-ssh")
						close(done)
					})
				},
			}, nil
		},
		waitForward: func(context.Context, string, *platformProcess) error {
			state.record("forward-ready")
			return nil
		},
		fetchHeimdal: func(ctx context.Context, c profile.Profile) ([]string, error) {
			state.record("fetch")
			if state.fetch == nil {
				t.Fatal("fetchHeimdal called unexpectedly")
			}
			return state.fetch(ctx, c)
		},
		startPodman: func(_ context.Context, c profile.Profile, directory string) (*podmanSession, error) {
			c.PrivateHosts = append([]string(nil), c.PrivateHosts...)
			state.controller = append(state.controller, c)
			state.record("controller")
			return &podmanSession{configuration: filepath.Join(directory, "podman.conf")}, nil
		},
		checkRegistry: func(context.Context, profile.Profile) error { return nil },
		loginPodman: func(context.Context, profile.Profile, *podmanSession) error {
			t.Fatal("loginPodman called with SkipRegistryLogin")
			return nil
		},
		startShell: func(context.Context, string, []string, []string) (*platformProcess, error) {
			state.shellStarts++
			state.record("shell")
			done := make(chan struct{})
			close(done)
			return &platformProcess{done: done, err: func() error { return nil }, stop: func() {}}, nil
		},
	}
}

func heimdalPlatformConfig(t *testing.T) profile.Profile {
	t.Helper()
	c := platformTestConfig(t)
	c.Environment = "example"
	c.PrivateHosts = []string{"local.private.example"}
	c.Heimdal = &profile.HeimdalSource{
		Subscription: "metadata-subscription",
		Account:      "examplestate",
		Environment:  "example",
	}
	return c
}

func TestPlatformConnectInstallsHeimdalRoutesBeforeControllerAndShell(t *testing.T) {
	c := heimdalPlatformConfig(t)
	c.ACRSession = true
	c.SkipRegistryLogin = true
	state := &heimdalConnectState{fetch: func(context.Context, profile.Profile) ([]string, error) {
		return []string{"remote.private.example", "local.private.example"}, nil
	}}
	services := heimdalConnectServices(t, state)

	var shellRunning atomic.Bool
	if err := platformConnectWith(context.Background(), c, &shellRunning, services); err != nil {
		t.Fatalf("platformConnectWith() error = %v", err)
	}
	if got, want := len(state.proxyProfiles), 2; got != want {
		t.Fatalf("proxy starts = %d, want %d", got, want)
	}
	if got, want := state.proxyProfiles[0].PrivateHosts, []string{"local.private.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bootstrap proxy routes = %q, want %q", got, want)
	}
	if got, want := state.proxyProfiles[1].PrivateHosts, []string{"local.private.example", "remote.private.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement proxy routes = %q, want %q", got, want)
	}
	if len(state.controller) != 1 || !reflect.DeepEqual(state.controller[0], state.proxyProfiles[1]) {
		t.Fatalf("controller profile = %+v, want replacement proxy profile %+v", state.controller, state.proxyProfiles[1])
	}
	assertHeimdalEventOrder(t, state.events, "forward-ready", "fetch", "close-proxy-1", "proxy-2", "controller", "shell")
	if state.proxyCloses[0] == 0 || state.proxyCloses[1] == 0 {
		t.Fatalf("proxy close counts = %v, want bootstrap and replacement closed", state.proxyCloses)
	}
	assertHeimdalTransportCleanup(t, state)
}

func TestPlatformConnectFetchesFreshHeimdalRoutesForEveryConnection(t *testing.T) {
	c := heimdalPlatformConfig(t)
	c.Heimdal.AllowLocalFallback = true
	fetches := 0
	state := &heimdalConnectState{fetch: func(context.Context, profile.Profile) ([]string, error) {
		fetches++
		if fetches == 1 {
			return []string{"first.remote.example"}, nil
		}
		return nil, errors.New("metadata unavailable")
	}}
	services := heimdalConnectServices(t, state)
	var shellRunning atomic.Bool

	if err := platformConnectWith(context.Background(), c, &shellRunning, services); err != nil {
		t.Fatalf("first platformConnectWith() error = %v", err)
	}
	if err := platformConnectWith(context.Background(), c, &shellRunning, services); err != nil {
		t.Fatalf("fallback platformConnectWith() error = %v", err)
	}
	if fetches != 2 {
		t.Fatalf("Heimdal fetches = %d, want one for each connection", fetches)
	}
	if got, want := len(state.proxyProfiles), 3; got != want {
		t.Fatalf("proxy starts = %d, want bootstrap/replacement then fresh bootstrap", got)
	}
	if got, want := state.proxyProfiles[1].PrivateHosts, []string{"local.private.example", "first.remote.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first replacement routes = %q, want %q", got, want)
	}
	if got, want := state.proxyProfiles[2].PrivateHosts, []string{"local.private.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback routes = %q, want only fresh local routes %q", got, want)
	}
	if state.shellStarts != 2 {
		t.Fatalf("shell starts = %d, want one per connection", state.shellStarts)
	}
	assertHeimdalTransportCleanup(t, state)
}

func TestPlatformConnectWithoutHeimdalDoesNotFetchOrReplaceProxy(t *testing.T) {
	c := platformTestConfig(t)
	c.PrivateHosts = []string{"local.private.example"}
	state := &heimdalConnectState{}
	services := heimdalConnectServices(t, state)

	var shellRunning atomic.Bool
	if err := platformConnectWith(context.Background(), c, &shellRunning, services); err != nil {
		t.Fatalf("platformConnectWith() error = %v", err)
	}
	if len(state.proxyProfiles) != 1 || state.shellStarts != 1 {
		t.Fatalf("proxy starts = %d, shell starts = %d; want one each", len(state.proxyProfiles), state.shellStarts)
	}
	if containsString(state.events, "fetch") {
		t.Fatalf("nil Heimdal source fetched metadata: %v", state.events)
	}
	assertHeimdalTransportCleanup(t, state)
}

func TestPlatformConnectRejectsFailedOrPartialHeimdalRefresh(t *testing.T) {
	for _, test := range []struct {
		name      string
		fetch     func(context.Context, profile.Profile) ([]string, error)
		wantError string
	}{
		{
			name: "required fetch failure",
			fetch: func(context.Context, profile.Profile) ([]string, error) {
				return nil, errors.New("metadata unavailable")
			},
			wantError: "Heimdal metadata required",
		},
		{
			name: "one invalid route rejects entire refresh",
			fetch: func(context.Context, profile.Profile) ([]string, error) {
				return []string{"valid.remote.example", "*.invalid.example"}, nil
			},
			wantError: "invalid private routes",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := heimdalPlatformConfig(t)
			state := &heimdalConnectState{fetch: test.fetch}
			services := heimdalConnectServices(t, state)
			var shellRunning atomic.Bool

			err := platformConnectWith(context.Background(), c, &shellRunning, services)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("platformConnectWith() error = %v, want %q", err, test.wantError)
			}
			if len(state.proxyProfiles) != 1 || !reflect.DeepEqual(state.proxyProfiles[0].PrivateHosts, c.PrivateHosts) {
				t.Fatalf("failed refresh installed partial routes: proxy profiles = %+v", state.proxyProfiles)
			}
			if state.shellStarts != 0 {
				t.Fatal("shell started after failed Heimdal refresh")
			}
			assertHeimdalTransportCleanup(t, state)
		})
	}
}

func TestPlatformConnectCancellationOverridesHeimdalFallbackAndCleansUp(t *testing.T) {
	c := heimdalPlatformConfig(t)
	c.Heimdal.AllowLocalFallback = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &heimdalConnectState{fetch: func(context.Context, profile.Profile) ([]string, error) {
		cancel()
		return nil, errors.New("metadata unavailable")
	}}
	services := heimdalConnectServices(t, state)
	var shellRunning atomic.Bool

	err := platformConnectWith(ctx, c, &shellRunning, services)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("platformConnectWith() error = %v, want context cancellation", err)
	}
	if state.shellStarts != 0 {
		t.Fatal("shell started after parent cancellation")
	}
	assertHeimdalTransportCleanup(t, state)
}

func TestPlatformConnectStopsBeforeShellWhenHeimdalReplacementProxyFails(t *testing.T) {
	c := heimdalPlatformConfig(t)
	bindError := errors.New("synthetic replacement bind failure")
	state := &heimdalConnectState{
		fetch: func(context.Context, profile.Profile) ([]string, error) {
			return []string{"remote.private.example"}, nil
		},
		secondProxyError: bindError,
	}
	services := heimdalConnectServices(t, state)
	var shellRunning atomic.Bool

	err := platformConnectWith(context.Background(), c, &shellRunning, services)
	if err == nil || !strings.Contains(err.Error(), "could not start the validated Heimdal session proxy") {
		t.Fatalf("platformConnectWith() error = %v, want replacement proxy failure", err)
	}
	if len(state.proxyProfiles) != 2 {
		t.Fatalf("proxy attempts = %d, want bootstrap and replacement", len(state.proxyProfiles))
	}
	if state.shellStarts != 0 {
		t.Fatal("shell started without the replacement Heimdal proxy")
	}
	if len(state.proxyCloses) != 1 || state.proxyCloses[0] == 0 {
		t.Fatalf("bootstrap proxy close counts = %v, want closed", state.proxyCloses)
	}
	assertHeimdalTransportCleanup(t, state)
}

func assertHeimdalEventOrder(t *testing.T, events []string, expected ...string) {
	t.Helper()
	position := -1
	for _, want := range expected {
		found := -1
		for i := position + 1; i < len(events); i++ {
			if events[i] == want {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("event %q did not occur after index %d in %v", want, position, events)
		}
		position = found
	}
}

func assertHeimdalTransportCleanup(t *testing.T, state *heimdalConnectState) {
	t.Helper()
	if state.bastionCloses != state.bastionOpens {
		t.Errorf("Bastion opens/closes = %d/%d", state.bastionOpens, state.bastionCloses)
	}
	if state.sshStops != state.sshStarts {
		t.Errorf("SSH starts/stops = %d/%d", state.sshStarts, state.sshStops)
	}
	for i, closes := range state.proxyCloses {
		if closes == 0 {
			t.Errorf("proxy %d was not closed", i+1)
		}
	}
}
