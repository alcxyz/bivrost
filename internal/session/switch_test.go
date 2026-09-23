package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
)

func TestSwitchControllerAuthenticatesAndValidatesBeforePublishing(t *testing.T) {
	var starts, logins atomic.Int32
	original := platformTestConfig(t)
	original.Environment = "original"
	a := newACRActivation(context.Background(), original, activationTestServices(t, &starts, &logins), t.TempDir(), "bash")
	defer a.close()
	env, err := a.listen(nil)
	if err != nil {
		t.Fatal(err)
	}
	control := readActivationControl(t, shellinit.EnvironmentValue(env, "BIVROST_CONTROL_FILE"))

	validPath := filepath.Join(t.TempDir(), "target.json")
	target := platformTestConfig(t)
	if err := writeConfigFile(validPath, target); err != nil {
		t.Fatal(err)
	}
	validBody, _ := json.Marshal(switchRequest{ConfigPath: validPath})

	response := postSwitchRequest(t, control, "wrong-token", validBody)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	response.Body.Close()
	if _, ok := a.pendingSwitch(); ok {
		t.Fatal("unauthenticated request published a switch")
	}

	for name, body := range map[string][]byte{
		"malformed":     []byte(`{"ConfigPath":`),
		"unknown field": []byte(`{"ConfigPath":"/tmp/example","Other":true}`),
		"relative path": []byte(`{"ConfigPath":"relative.json","ACR":false}`),
		"oversize":      append([]byte(`{"ConfigPath":"/tmp/example"}`), bytes.Repeat([]byte(" "), maxSwitchRequestBytes)...),
	} {
		t.Run(name, func(t *testing.T) {
			response := postSwitchRequest(t, control, control.Token, body)
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
			}
			if _, ok := a.pendingSwitch(); ok {
				t.Fatal("invalid request published a switch")
			}
		})
	}

	response = postSwitchRequest(t, control, control.Token, validBody)
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("valid status = %d, want %d: %s", response.StatusCode, http.StatusAccepted, body)
	}
	pending, ok := a.pendingSwitch()
	if !ok {
		t.Fatal("valid target was not published")
	}
	if pending.Environment != "custom-profile" || pending.ACRSession || pending.SkipRegistryLogin {
		t.Fatalf("pending target flags = %+v", pending)
	}
	if a.config.Environment != "original" {
		t.Fatalf("request changed active target to %q", a.config.Environment)
	}
}

func TestRunSwitchReturnsAcceptedSentinel(t *testing.T) {
	var starts, logins atomic.Int32
	a := newACRActivation(context.Background(), platformTestConfig(t), activationTestServices(t, &starts, &logins), t.TempDir(), "bash")
	defer a.close()
	env, err := a.listen(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BIVROST_SESSION", "1")
	t.Setenv("BIVROST_SWITCH_ALLOWED", "1")
	t.Setenv("BIVROST_CONTROL_FILE", shellinit.EnvironmentValue(env, "BIVROST_CONTROL_FILE"))
	targetPath := filepath.Join(t.TempDir(), "target.json")
	if err := writeConfigFile(targetPath, platformTestConfig(t)); err != nil {
		t.Fatal(err)
	}

	err = runSwitch(context.Background(), cli.Command{ConfigPath: targetPath, PrivateHosts: []string{"switch-only.example"}})
	if !errors.Is(err, ErrSwitchAccepted) {
		t.Fatalf("runSwitch error = %v, want accepted sentinel", err)
	}
	pending, ok := a.pendingSwitch()
	if !ok {
		t.Fatal("accepted client response did not leave a pending target")
	}
	if len(pending.PrivateHosts) != 1 || pending.PrivateHosts[0] != "switch-only.example" {
		t.Fatalf("accepted switch private hosts = %v", pending.PrivateHosts)
	}
}

func TestSwitchPrivateHostsApplyOnlyWhenExplicit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target.json")
	target := platformTestConfig(t)
	target.PrivateHosts = []string{"configured.example"}
	if err := writeConfigFile(path, target); err != nil {
		t.Fatal(err)
	}

	withAddition, err := loadSwitchTarget(switchRequest{ConfigPath: path, PrivateHosts: []string{"switch-only.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(withAddition.PrivateHosts) != 2 || withAddition.PrivateHosts[1] != "switch-only.example" {
		t.Fatalf("explicit switch private hosts = %v", withAddition.PrivateHosts)
	}

	withoutAddition, err := loadSwitchTarget(switchRequest{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutAddition.PrivateHosts) != 1 || withoutAddition.PrivateHosts[0] != "configured.example" {
		t.Fatalf("switch inherited an unrequested private host: %v", withoutAddition.PrivateHosts)
	}
}

func TestPlatformSwitchCleansUpBeforeOpeningNextTarget(t *testing.T) {
	for _, test := range []struct {
		name                string
		firstShellErr       error
		failNextConnection  bool
		wantSessions        int32
		wantConnectionError bool
	}{
		{name: "accepted exit reconnects", firstShellErr: exitCodeError(SwitchShellExitCode), wantSessions: 2},
		{name: "next setup failure returns", firstShellErr: exitCodeError(SwitchShellExitCode), failNextConnection: true, wantSessions: 2, wantConnectionError: true},
		{name: "ordinary exit ignores pending target", wantSessions: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			testPlatformSwitchLifecycle(t, test.firstShellErr, test.failNextConnection, test.wantSessions, test.wantConnectionError)
		})
	}
}

func testPlatformSwitchLifecycle(t *testing.T, firstShellErr error, failNextConnection bool, wantSessions int32, wantConnectionError bool) {
	t.Helper()
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")

	first := platformTestConfig(t)
	first.Environment = "first"
	second := platformTestConfig(t)
	second.Environment = "second"
	targetPath := filepath.Join(t.TempDir(), "second.json")
	if err := writeConfigFile(targetPath, second); err != nil {
		t.Fatal(err)
	}

	var session atomic.Int32
	var firstProxyClosed, firstBastionClosed, firstSSHStopped atomic.Bool
	nextConnectionFailure := errors.New("synthetic next connection failure")
	services := platformServices{
		environ:     func() []string { return []string{"PATH=/custom/bin"} },
		selectShell: func() (string, []string, error) { return "/bin/bash", []string{"-i"}, nil },
		reservePort: func(port int) (net.Listener, error) { return net.Listen("tcp4", profile.Loopback(port)) },
		startProxy: func(_ context.Context, c profile.Profile) (*platformProxy, error) {
			current := session.Add(1)
			if current == 2 && (!firstProxyClosed.Load() || !firstBastionClosed.Load() || !firstSSHStopped.Load()) {
				t.Fatal("next target started before the previous target was cleaned up")
			}
			if current == 2 && failNextConnection {
				return nil, nextConnectionFailure
			}
			done := make(chan error)
			return &platformProxy{done: done, close: func() {
				if current == 1 {
					firstProxyClosed.Store(true)
				}
			}}, nil
		},
		openBastion: func(context.Context, profile.Profile) (*platformBastion, error) {
			current := session.Load()
			directory := filepath.Join(t.TempDir(), "session")
			if err := os.Mkdir(directory, 0o700); err != nil {
				return nil, err
			}
			done := make(chan struct{})
			return &platformBastion{
				port: 32022, stateRoot: filepath.Dir(directory), directory: directory,
				sshConfig: filepath.Join(directory, "ssh_config"), done: done,
				close: func() {
					if current == 1 {
						firstBastionClosed.Store(true)
					}
					_ = os.RemoveAll(directory)
				},
			}, nil
		},
		prepareKubeconfig: func(context.Context, profile.Profile, string, int) (kubeTarget, error) {
			t.Fatal("prepareKubeconfig called without AKS")
			return kubeTarget{}, nil
		},
		startSSH: func(context.Context, []string) (*platformProcess, error) {
			current := session.Load()
			done := make(chan struct{})
			var once sync.Once
			stop := func() {
				once.Do(func() { close(done) })
				if current == 1 {
					firstSSHStopped.Store(true)
				}
			}
			return &platformProcess{done: done, err: func() error { return nil }, stop: stop}, nil
		},
		waitForward: func(context.Context, string, *platformProcess) error { return nil },
	}
	services.startShell = func(_ context.Context, _ string, _ []string, env []string) (*platformProcess, error) {
		current := session.Load()
		done := make(chan struct{})
		var once sync.Once
		stop := func() { once.Do(func() { close(done) }) }
		if current == 1 {
			control := readActivationControl(t, shellinit.EnvironmentValue(env, "BIVROST_CONTROL_FILE"))
			body, _ := json.Marshal(switchRequest{ConfigPath: targetPath})
			response := postSwitchRequest(t, control, control.Token, body)
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				t.Fatalf("switch status = %d, want %d", response.StatusCode, http.StatusAccepted)
			}
			if session.Load() != 1 {
				t.Fatal("accepted request changed target before the shell exited")
			}
			close(done)
			return &platformProcess{done: done, err: func() error { return firstShellErr }, stop: func() {}}, nil
		}
		stop()
		return &platformProcess{done: done, err: func() error { return nil }, stop: stop}, nil
	}

	var shellRunning atomic.Bool
	err := platformConnectLoop(context.Background(), first, &shellRunning, services)
	if wantConnectionError {
		if !errors.Is(err, nextConnectionFailure) {
			t.Fatalf("platformConnectLoop error = %v, want next connection failure", err)
		}
	} else if err != nil {
		t.Fatalf("platformConnectLoop: %v", err)
	}
	if session.Load() != wantSessions {
		t.Fatalf("opened %d sessions, want %d", session.Load(), wantSessions)
	}
}

type exitCodeError int

func (e exitCodeError) Error() string { return "shell exited" }
func (e exitCodeError) ExitCode() int { return int(e) }

func readActivationControl(t *testing.T, path string) activationControl {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var control activationControl
	if err := json.Unmarshal(data, &control); err != nil {
		t.Fatal(err)
	}
	return control
}

func postSwitchRequest(t *testing.T, control activationControl, token string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest("POST", "http://"+control.Address+"/switch", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func writeConfigFile(path string, c profile.Profile) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
