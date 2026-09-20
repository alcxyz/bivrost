package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func activationTestServices(t *testing.T, starts, logins *atomic.Int32) platformServices {
	t.Helper()
	return platformServices{
		startPodman: func(_ context.Context, _ config, directory string) (*podmanSession, error) {
			starts.Add(1)
			path := filepath.Join(directory, "podman-session.conf")
			if err := os.WriteFile(path, []byte("[engine]\nremote = false\n"), 0600); err != nil {
				return nil, err
			}
			return &podmanSession{configuration: path}, nil
		},
		checkRegistry: func(context.Context, config) error { return nil },
		loginPodman: func(context.Context, config, *podmanSession) error {
			logins.Add(1)
			return nil
		},
	}
}

func TestActivationControlRejectsUnauthenticatedRequests(t *testing.T) {
	var starts, logins atomic.Int32
	directory := t.TempDir()
	a := newACRActivation(context.Background(), platformTestConfig(t), activationTestServices(t, &starts, &logins), directory, "bash")
	defer a.close()
	env, err := a.listen([]string{"PATH=/existing"})
	if err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 0 || logins.Load() != 0 {
		t.Fatal("opening an ordinary shell started Podman or registry login")
	}
	path := environmentValue(env, "BIVROST_CONTROL_FILE")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("control file must be private: %v, %v", info, err)
		}
	}
	var control activationControl
	if err := json.Unmarshal(data, &control); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	for _, test := range []struct {
		name, method, path, token, origin string
	}{
		{"missing token", "POST", "/enable", "", ""},
		{"wrong token", "POST", "/enable", strings.Repeat("0", 64), ""},
		{"wrong method", "GET", "/enable", control.Token, ""},
		{"wrong path", "POST", "/other", control.Token, ""},
		{"browser origin", "POST", "/enable", control.Token, "https://untrusted.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest(test.method, "http://"+control.Address+test.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+test.token)
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want forbidden", response.StatusCode)
			}
		})
	}
	if starts.Load() != 0 || logins.Load() != 0 {
		t.Fatal("rejected control request started a capability")
	}
	req, _ := http.NewRequest("POST", "http://"+control.Address+"/enable", nil)
	req.Header.Set("Authorization", "Bearer "+control.Token)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || starts.Load() != 1 || logins.Load() != 1 {
		t.Fatalf("authorized activation status=%d starts=%d logins=%d", response.StatusCode, starts.Load(), logins.Load())
	}
	a.close()
	for _, path := range []string{path, a.script, filepath.Join(directory, "podman-session.conf")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("owned resource remains after close: %s (%v)", path, err)
		}
	}
	if err := a.enable(); err == nil {
		t.Error("closed session accepted activation")
	}
	if response, err := client.Do(req); err == nil {
		response.Body.Close()
		t.Error("control listener remains open after close")
	}
}

func TestActivationConcurrentEnableIsIdempotent(t *testing.T) {
	var starts, logins atomic.Int32
	a := newACRActivation(context.Background(), platformTestConfig(t), activationTestServices(t, &starts, &logins), t.TempDir(), "bash")
	defer a.close()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.enable(); err != nil {
				t.Errorf("enable: %v", err)
			}
		}()
	}
	wg.Wait()
	if starts.Load() != 1 || logins.Load() != 1 {
		t.Fatalf("activation repeated: starts=%d logins=%d", starts.Load(), logins.Load())
	}
}

func TestActivationFailureCleansUpAndAllowsRetry(t *testing.T) {
	for _, failAt := range []string{"registry", "login", "script"} {
		t.Run(failAt, func(t *testing.T) {
			var starts, logins atomic.Int32
			directory := t.TempDir()
			services := activationTestServices(t, &starts, &logins)
			fail := true
			failure := errors.New("synthetic failure")
			services.checkRegistry = func(context.Context, config) error {
				if fail && failAt == "registry" {
					return failure
				}
				return nil
			}
			services.loginPodman = func(context.Context, config, *podmanSession) error {
				logins.Add(1)
				if fail && failAt == "login" {
					return failure
				}
				return nil
			}
			a := newACRActivation(context.Background(), platformTestConfig(t), services, directory, "bash")
			defer a.close()
			if failAt == "script" {
				if err := os.Mkdir(a.script, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.enable(); err == nil {
				t.Fatal("failed setup accepted")
			}
			if a.session != nil {
				t.Fatal("failed setup published a session")
			}
			if _, err := os.Stat(filepath.Join(directory, "podman-session.conf")); !os.IsNotExist(err) {
				t.Fatalf("failed setup leaked Podman configuration: %v", err)
			}
			if failAt == "script" {
				if err := os.Remove(a.script); err != nil {
					t.Fatal(err)
				}
			}
			fail = false
			if err := a.enable(); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if starts.Load() != 2 || a.session == nil {
				t.Fatal("retry did not create a fresh successful session")
			}
		})
	}
}

func TestActivationScriptQuotesShellMetacharacters(t *testing.T) {
	s := &podmanSession{
		configuration: "/tmp/it's $(printf injected); `printf injected`\nconfig-‘’“”-æøå",
		host:          "ssh://user@localhost/path'$HOME",
		identity:      "/tmp/key' ; echo injected #",
	}
	script := activationScript("bash", s)
	if bash, err := exec.LookPath("bash"); err == nil && runtime.GOOS != "windows" {
		cmd := exec.Command(bash, "--noprofile", "--norc", "-c", script+"printf '%s\\0%s\\0%s\\0%s' \"$CONTAINERS_CONF_OVERRIDE\" \"$CONTAINER_HOST\" \"$CONTAINER_SSHKEY\" \"${CONTAINER_CONNECTION-unset}\"")
		cmd.Env = []string{"CONTAINER_CONNECTION=old"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("source quoted script: %v (%s)", err, out)
		}
		want := s.configuration + "\x00" + s.host + "\x00" + s.identity + "\x00unset"
		if string(out) != want {
			t.Fatalf("shell expanded literal values: got %q, want %q", out, want)
		}
	}
	ps := activationScript("pwsh.exe", s)
	wantValues := map[string]string{
		"CONTAINERS_CONF_OVERRIDE": s.configuration,
		"CONTAINER_HOST":           s.host,
		"CONTAINER_SSHKEY":         s.identity,
	}
	var gotValues map[string]string
	if err := json.Unmarshal([]byte(ps), &gotValues); err != nil {
		t.Fatalf("PowerShell activation is not JSON: %v", err)
	}
	if !reflect.DeepEqual(gotValues, wantValues) {
		t.Fatalf("PowerShell data did not roundtrip: got %q, want %q", gotValues, wantValues)
	}
	var nativeValues map[string]string
	if err := json.Unmarshal([]byte(activationScript("powershell.exe", &podmanSession{configuration: s.configuration})), &nativeValues); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(nativeValues, map[string]string{"CONTAINERS_CONF_OVERRIDE": s.configuration}) {
		t.Fatalf("native PowerShell data sets a remote connection: %q", nativeValues)
	}
	for _, binary := range []string{"pwsh", "powershell"} {
		executable, err := exec.LookPath(binary)
		if err != nil {
			continue
		}
		t.Run(binary, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "activation.json")
			if err := os.WriteFile(path, []byte(ps), 0600); err != nil {
				t.Fatal(err)
			}
			command := `$value = Get-Content -LiteralPath $env:BIVROST_TEST_DATA -Raw -Encoding UTF8 | ConvertFrom-Json; [Console]::OutputEncoding = [Text.Encoding]::UTF8; [Console]::Write(($value | ConvertTo-Json -Compress))`
			cmd := exec.Command(executable, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
			cmd.Env = replaceEnvironment(os.Environ(), "BIVROST_TEST_DATA", path)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("parse PowerShell data: %v (%s)", err, out)
			}
			var roundtrip map[string]string
			if err := json.Unmarshal(out, &roundtrip); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(roundtrip, wantValues) {
				t.Fatalf("PowerShell expanded values: got %q, want %q", roundtrip, wantValues)
			}
		})
	}

}

func TestUnifiedACRCommands(t *testing.T) {
	for _, test := range []struct {
		args         []string
		kind         commandKind
		acr, noLogin bool
		fail         bool
	}{
		{args: []string{"connect", "-e", "staging", "--acr"}, kind: commandConnect, acr: true},
		{args: []string{"connect", "-e", "staging", "--acr", "-n"}, kind: commandConnect, acr: true, noLogin: true},
		{args: []string{"connect", "-e", "staging", "-n"}, fail: true},
		{args: []string{"acr", "enable"}, kind: commandACREnable},
		{args: []string{"acr", "enable", "-e", "prod"}, fail: true},
		{args: []string{"acr", "connect", "-e", "staging", "-n"}, kind: commandACRConnect, noLogin: true},
	} {
		got, err := parseCommand(test.args)
		if (err != nil) != test.fail {
			t.Fatalf("%v: %v", test.args, err)
		}
		if !test.fail && (got.kind != test.kind || got.acr != test.acr || got.noLogin != test.noLogin) {
			t.Fatalf("%v: unexpected parsed command %+v", test.args, got)
		}
	}
}

func TestActivationValidatesRegistrySubscriptionBeforeSideEffects(t *testing.T) {
	var starts, logins atomic.Int32
	c := platformTestConfig(t)
	c.RegistrySubscription = ""
	a := newACRActivation(context.Background(), c, activationTestServices(t, &starts, &logins), t.TempDir(), "bash")
	defer a.close()
	if err := a.enable(); err == nil || !strings.Contains(err.Error(), "registry_subscription") {
		t.Fatalf("missing subscription: %v", err)
	}
	if starts.Load() != 0 || logins.Load() != 0 {
		t.Fatal("invalid subscription caused activation side effects")
	}
	a.config.SkipRegistryLogin = true
	if err := a.enable(); err != nil {
		t.Fatalf("skip login should allow missing registry subscription: %v", err)
	}
	if starts.Load() != 1 || logins.Load() != 0 {
		t.Fatal("skip login activation ran unexpected operations")
	}
}
