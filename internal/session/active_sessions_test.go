package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
)

// testSessionRegistry keeps each test's sessions in its own registry. A limit
// of one also catches a session that does not release its record.
func testSessionRegistry(t *testing.T) func(context.Context, profile.Profile, string) (*activeSession, error) {
	t.Helper()
	directory := t.TempDir()
	return func(ctx context.Context, c profile.Profile, command string) (*activeSession, error) {
		return registerSession(ctx, directory, c, command, 1)
	}
}

func registerTestSession(t *testing.T, directory, environment string, limit int) *activeSession {
	t.Helper()
	s, err := registerSession(context.Background(), directory, profile.Profile{Environment: environment}, "connect", limit)
	if err != nil {
		t.Fatalf("registerSession(%s) error = %v", environment, err)
	}
	t.Cleanup(s.close)
	return s
}

func activeTestSessions(t *testing.T, directory string) []sessionRecord {
	t.Helper()
	guard, err := lockSessionRegistry(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSessionRegistry(guard)
	active, err := scanSessions(directory)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func TestRegisterSessionAppliesLowestActiveLimit(t *testing.T) {
	directory := t.TempDir()
	registerTestSession(t, directory, "sandbox", 3)
	staging := registerTestSession(t, directory, "staging", 2)
	if _, err := registerSession(context.Background(), directory, profile.Profile{Environment: "infra"}, "run", 3); err == nil || !strings.Contains(err.Error(), "session limit is 2") {
		t.Fatalf("third session error = %v, want the lower limit of an active session", err)
	}
	staging.close()
	registerTestSession(t, directory, "infra", 3)
	if got := len(activeTestSessions(t, directory)); got != 2 {
		t.Fatalf("active sessions = %d, want 2", got)
	}
	// The lower limit ended with the staging session.
	registerTestSession(t, directory, "production", 3)
}

func TestSessionLimitEndsWithItsSession(t *testing.T) {
	directory := t.TempDir()
	staging := registerTestSession(t, directory, "staging", 2)
	registerTestSession(t, directory, "sandbox", 4)
	staging.close()
	registerTestSession(t, directory, "infra", 4)
	registerTestSession(t, directory, "production", 4)
	if got := sessionLimitOf(activeTestSessions(t, directory)); got != 4 {
		t.Fatalf("limit after the restricting session exited = %d, want 4", got)
	}
}

func TestRegisterSessionHonoursExclusiveProfile(t *testing.T) {
	directory := t.TempDir()
	production := registerTestSession(t, directory, "production", 1)
	if _, err := registerSession(context.Background(), directory, profile.Profile{Environment: "sandbox"}, "connect", 4); err == nil {
		t.Fatal("a session joined a session whose profile allows only one")
	}
	production.close()
	registerTestSession(t, directory, "sandbox", 4)
	if _, err := registerSession(context.Background(), directory, profile.Profile{Environment: "production"}, "connect", 1); err == nil {
		t.Fatal("a profile allowing one session started beside another session")
	}
}

func TestRegisterSessionIsAtomicUnderConcurrency(t *testing.T) {
	directory := t.TempDir()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var sessions []*activeSession
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			s, err := registerSession(context.Background(), directory, profile.Profile{Environment: "sandbox"}, "connect", 3)
			if err == nil {
				mu.Lock()
				sessions = append(sessions, s)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	for _, s := range sessions {
		defer s.close()
	}
	if len(sessions) != 3 {
		t.Fatalf("%d concurrent registrations succeeded, want 3", len(sessions))
	}
}

func TestScanSessionsRemovesRecordsOfExitedOwners(t *testing.T) {
	directory := t.TempDir()
	live := registerTestSession(t, directory, "sandbox", 4)
	// A crashed owner leaves its record behind, but no process holds its lock.
	stale := filepath.Join(directory, sessionRecordPrefix+"0123456789abcdef"+sessionRecordSuffix)
	data, _ := json.Marshal(sessionRecord{ID: "0123456789abcdef", Environment: "staging", MaxSessions: 1})
	if err := os.WriteFile(stale, data, 0o600); err != nil {
		t.Fatal(err)
	}
	active := activeTestSessions(t, directory)
	if len(active) != 1 || active[0].ID != live.record.ID {
		t.Fatalf("active sessions = %+v, want only the live session", active)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale record remains: %v", err)
	}
	live.close()
	if entries, _ := os.ReadDir(directory); len(entries) != 1 || entries[0].Name() != sessionRegistryLock {
		t.Fatalf("registry entries after close = %v, want only the lock file", entries)
	}
}

func TestClaimACRAllowsOneSessionAtATime(t *testing.T) {
	directory := t.TempDir()
	sandbox := registerTestSession(t, directory, "sandbox", 4)
	staging := registerTestSession(t, directory, "staging", 4)
	if err := sandbox.claimACR(); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.claimACR(); err != nil {
		t.Fatalf("repeated claim by the same session failed: %v", err)
	}
	if err := staging.claimACR(); err == nil || !strings.Contains(err.Error(), "(sandbox) already has registry access") {
		t.Fatalf("second claim error = %v, want the holding session", err)
	}
	sandbox.releaseACR()
	if err := staging.claimACR(); err != nil {
		t.Fatalf("claim after release failed: %v", err)
	}
	staging.close()
	if err := sandbox.claimACR(); err != nil {
		t.Fatalf("claim after the holder exited failed: %v", err)
	}
}

func TestSessionRecordShowsPortsAndList(t *testing.T) {
	directory := t.TempDir()
	s := registerTestSession(t, directory, "sandbox", 2)
	if err := s.setPorts(40001, 40002); err != nil {
		t.Fatal(err)
	}
	if err := s.claimACR(); err != nil {
		t.Fatal(err)
	}
	active := activeTestSessions(t, directory)
	if len(active) != 1 || active[0].ProxyPort != 40001 || active[0].SOCKSPort != 40002 || !active[0].ACR || active[0].PID != os.Getpid() || active[0].Command != "connect" {
		t.Fatalf("record = %+v", active)
	}
	var out bytes.Buffer
	if err := writeSessionList(&out, active); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ENVIRONMENT", "sandbox", "127.0.0.1:40001", "127.0.0.1:40002", "enabled", "Session limit: 2"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("session list lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := writeSessionList(&out, nil); err != nil || out.String() != "No active Bivrost sessions.\n" {
		t.Fatalf("empty list = %q, %v", out.String(), err)
	}
}

func TestSessionLimitDefaultsToEnvironmentCount(t *testing.T) {
	isolateCatalogueProfiles(t)
	if got := sessionLimit(profile.Profile{}); got != 1 {
		t.Fatalf("limit without environments = %d, want 1", got)
	}
	c := catalogueTestConfig(false)
	writeCatalogue(t, map[string]profile.Profile{"sandbox": c, "staging": c, "production": c, "infra": c})
	if got := sessionLimit(profile.Profile{}); got != 4 {
		t.Fatalf("default limit = %d, want one per catalogue environment", got)
	}
	if got := sessionLimit(profile.Profile{MaxSessions: 2}); got != 2 {
		t.Fatalf("limit with max_sessions = %d, want 2", got)
	}
}

func TestPlatformSessionChoosesPortsPerSession(t *testing.T) {
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	directory := filepath.Join(t.TempDir(), "session-test")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	registry := t.TempDir()
	c := platformTestConfig(t)
	c.ProxyPort, c.SOCKSPort = 0, 0
	proxyPort := availablePort(t)
	sshDone := make(chan struct{})
	var closeSSH sync.Once
	var capturedSSH, capturedEnv, waited []string
	var proxyConfig profile.Profile
	var records []sessionRecord
	var status doctorSessionStatus
	services := platformServices{
		registerSession: func(ctx context.Context, c profile.Profile, command string) (*activeSession, error) {
			return registerSession(ctx, registry, c, command, 1)
		},
		environ:     func() []string { return []string{"PATH=/custom/bin"} },
		selectShell: func() (string, []string, error) { return "/bin/bash", []string{"-i"}, nil },
		reservePort: func(port int) (net.Listener, error) { return net.Listen("tcp4", profile.Loopback(port)) },
		startProxy: func(_ context.Context, got profile.Profile) (*platformProxy, error) {
			proxyConfig = got
			return &platformProxy{port: proxyPort, done: make(chan error), close: func() {}}, nil
		},
		openBastion: func(context.Context, profile.Profile) (*platformBastion, error) {
			return &platformBastion{port: 32022, stateRoot: filepath.Dir(directory), directory: directory, sshConfig: filepath.Join(directory, "ssh_config"), done: make(chan struct{}), close: func() { _ = os.RemoveAll(directory) }}, nil
		},
		startSSH: func(_ context.Context, args []string) (*platformProcess, error) {
			capturedSSH = append([]string(nil), args...)
			return &platformProcess{done: sshDone, err: func() error { return nil }, stop: func() { closeSSH.Do(func() { close(sshDone) }) }}, nil
		},
		startShell: func(_ context.Context, _ string, _ []string, env []string) (*platformProcess, error) {
			capturedEnv = append([]string(nil), env...)
			records = activeTestSessions(t, registry)
			status = doctorTestStatus(t, readActivationControl(t, shellinit.EnvironmentValue(env, "BIVROST_CONTROL_FILE")))
			done := make(chan struct{})
			close(done)
			return &platformProcess{done: done, err: func() error { return nil }, stop: func() {}}, nil
		},
		waitForward: func(_ context.Context, address string, _ *platformProcess) error {
			waited = append(waited, address)
			return nil
		},
	}
	var shellRunning atomic.Bool
	if err := platformConnectWith(context.Background(), c, &shellRunning, services); err != nil {
		t.Fatalf("platformConnectWith() error = %v", err)
	}
	socks := proxyConfig.SOCKSPort
	if socks == 0 || socks == proxyPort {
		t.Fatalf("proxy received SOCKS port %d, want a free port of its own", socks)
	}
	if !containsString(capturedSSH, profile.Loopback(socks)) || len(waited) != 1 || waited[0] != profile.Loopback(socks) {
		t.Errorf("SSH arguments %q and waits %q do not use SOCKS port %d", capturedSSH, waited, socks)
	}
	if got := shellinit.EnvironmentValue(capturedEnv, "HTTPS_PROXY"); got != "http://"+profile.Loopback(proxyPort) {
		t.Errorf("HTTPS_PROXY = %q, want the session proxy port %d", got, proxyPort)
	}
	if len(records) != 1 || records[0].ProxyPort != proxyPort || records[0].SOCKSPort != socks || records[0].Environment != c.Environment {
		t.Errorf("session records = %+v, want this session's ports", records)
	}
	if status.Config.ProxyPort != proxyPort || status.Config.SOCKSPort != socks {
		t.Errorf("session status ports = %d/%d, want %d/%d", status.Config.ProxyPort, status.Config.SOCKSPort, proxyPort, socks)
	}
	selected := c
	selected.ProxyPort, selected.SOCKSPort = 0, 0
	checked, matched := matchSessionProfile(&status, selected)
	if !matched {
		t.Error("doctor did not recognise the session of a profile without fixed ports")
	}
	if checked.ProxyURL() != shellinit.EnvironmentValue(capturedEnv, "HTTPS_PROXY") || checked.SOCKSPort != socks {
		t.Errorf("doctor checks ports %d/%d, want the session's %d/%d", checked.ProxyPort, checked.SOCKSPort, proxyPort, socks)
	}
	if got := activeTestSessions(t, registry); len(got) != 0 {
		t.Errorf("session record remains after exit: %+v", got)
	}
}

func TestStartProxyChoosesFreePort(t *testing.T) {
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	c := platformTestConfig(t)
	c.ProxyPort = 0
	p, err := startProxy(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	_, current := p.currentRoutes()
	if current.ProxyPort == 0 {
		t.Fatal("proxy did not record its chosen port")
	}
	if err := checkProxy(context.Background(), current, ""); err != nil {
		t.Fatalf("proxy is not reachable at its chosen port: %v", err)
	}
	updated := current
	updated.PrivateHosts = []string{"added.private.example"}
	if err := p.replaceRoutes(updated); err != nil {
		t.Fatalf("replaceRoutes() with the chosen port error = %v", err)
	}
}

func TestActivationRejectsSecondRegistrySession(t *testing.T) {
	registry := t.TempDir()
	var starts, logins atomic.Int32
	services := activationTestServices(t, &starts, &logins)
	first := newACRActivation(context.Background(), platformTestConfig(t), services, t.TempDir(), "bash")
	defer first.close()
	first.registration = registerTestSession(t, registry, "sandbox", 4)
	second := newACRActivation(context.Background(), platformTestConfig(t), services, t.TempDir(), "bash")
	defer second.close()
	second.registration = registerTestSession(t, registry, "staging", 4)
	env, err := second.listen(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.enable(); err != nil {
		t.Fatal(err)
	}
	control := readActivationControl(t, shellinit.EnvironmentValue(env, "BIVROST_CONTROL_FILE"))
	req, _ := http.NewRequest("POST", "http://"+control.Address+"/enable", nil)
	req.Header.Set("Authorization", "Bearer "+control.Token)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(body), "already has registry access") {
		t.Fatalf("second enable = %d %q, want a conflict naming the single registry session", response.StatusCode, body)
	}
	if starts.Load() != 1 {
		t.Fatalf("Podman sessions started = %d, want only the first", starts.Load())
	}
	first.close()
	first.registration.close()
	if err := second.enable(); err != nil {
		t.Fatalf("enable after the holder exited error = %v", err)
	}
}

const registryHelperEnvironment = "BIVROST_TEST_SESSION_REGISTRY"

// TestRegistryHelperProcess holds a session for TestSessionRecordOfKilledProcessIsStale.
func TestRegistryHelperProcess(t *testing.T) {
	directory := os.Getenv(registryHelperEnvironment)
	if directory == "" {
		t.Skip("helper process only")
	}
	if _, err := registerSession(context.Background(), directory, profile.Profile{Environment: "helper"}, "connect", 1); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("registered\n")
	io.Copy(io.Discard, os.Stdin)
}

func TestSessionRecordOfKilledProcessIsStale(t *testing.T) {
	directory := t.TempDir()
	helper := exec.Command(os.Args[0], "-test.run=^TestRegistryHelperProcess$")
	helper.Env = append(os.Environ(), registryHelperEnvironment+"="+directory)
	stdin, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if line != "registered\n" {
		helper.Process.Kill()
		helper.Wait()
		t.Fatalf("helper output = %q, want registration", line)
	}
	if _, err := registerSession(context.Background(), directory, profile.Profile{Environment: "sandbox"}, "connect", 4); err == nil {
		helper.Process.Kill()
		helper.Wait()
		t.Fatal("registration ignored a session held by another process")
	}
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	helper.Wait()
	// Windows may release a dead process's locks after a short delay.
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := registerSession(context.Background(), directory, profile.Profile{Environment: "sandbox"}, "connect", 1)
		if err == nil {
			defer s.close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("registration after the owner was killed error = %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := activeTestSessions(t, directory); len(got) != 1 || got[0].Environment != "sandbox" {
		t.Fatalf("active sessions = %+v, want only the new session", got)
	}
}
