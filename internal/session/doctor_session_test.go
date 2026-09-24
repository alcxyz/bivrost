package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
)

func doctorTestController(t *testing.T, c profile.Profile, services platformServices) (*acrActivation, activationControl) {
	t.Helper()
	a := newACRActivation(context.Background(), c, services, t.TempDir(), "bash")
	t.Cleanup(a.close)
	env, err := a.listen(nil)
	if err != nil {
		t.Fatal(err)
	}
	path := shellinit.EnvironmentValue(env, "BIVROST_CONTROL_FILE")
	t.Setenv("BIVROST_CONTROL_FILE", path)
	t.Setenv("BIVROST_SESSION", "test-session")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var control activationControl
	if err := json.Unmarshal(data, &control); err != nil {
		t.Fatal(err)
	}
	return a, control
}

func doctorTestStatus(t *testing.T, control activationControl) doctorSessionStatus {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest("POST", "http://"+control.Address+"/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+control.Token)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status endpoint returned %d", response.StatusCode)
	}
	var status doctorSessionStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestDoctorSessionStatusRejectsUnauthenticatedRequests(t *testing.T) {
	var starts, logins atomic.Int32
	_, control := doctorTestController(t, platformTestConfig(t), activationTestServices(t, &starts, &logins))
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	for _, test := range []struct{ name, method, token, origin string }{
		{"missing token", "POST", "", ""},
		{"wrong token", "POST", strings.Repeat("0", 64), ""},
		{"wrong method", "GET", control.Token, ""},
		{"browser origin", "POST", control.Token, "https://untrusted.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, _ := http.NewRequest(test.method, "http://"+control.Address+"/status", nil)
			req.Header.Set("Authorization", "Bearer "+test.token)
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status=%d, want forbidden", response.StatusCode)
			}
			if bytes.Contains(body, []byte("Registry")) || bytes.Contains(body, []byte(control.Token)) {
				t.Fatal("rejected status request exposed session data")
			}
		})
	}
	if starts.Load() != 0 || logins.Load() != 0 {
		t.Fatal("status requests activated ACR")
	}
}

func TestDoctorSessionStatusTracksSuccessfulActivation(t *testing.T) {
	for _, skipLogin := range []bool{false, true} {
		t.Run(map[bool]string{false: "login", true: "skip login"}[skipLogin], func(t *testing.T) {
			var starts, logins atomic.Int32
			c := platformTestConfig(t)
			c.SkipRegistryLogin = skipLogin
			a, control := doctorTestController(t, c, activationTestServices(t, &starts, &logins))
			before := doctorTestStatus(t, control)
			if before.Enabled || before.LoginRefreshed || len(before.Environment) != 0 {
				t.Fatalf("ordinary session reported activated: %+v", before)
			}
			if err := a.enable(); err != nil {
				t.Fatal(err)
			}
			after := doctorTestStatus(t, control)
			if !after.Enabled || after.LoginRefreshed == skipLogin || after.Machine {
				t.Fatalf("wrong activation state: %+v", after)
			}
			if after.Environment["CONTAINERS_CONF_OVERRIDE"] != a.session.configuration {
				t.Fatal("status did not provide owned Podman environment")
			}
			if starts.Load() != 1 || logins.Load() != map[bool]int32{false: 1, true: 0}[skipLogin] {
				t.Fatal("status queries changed activation side effects")
			}
		})
	}
}

func TestDoctorSessionStatusPreservesUnavailableKubernetesAfterACRActivation(t *testing.T) {
	var starts, logins atomic.Int32
	c := platformTestConfig(t)
	c.AKS = &profile.AKS{Name: "example", ResourceGroup: "rg", Subscription: "sub"}
	a, control := doctorTestController(t, c, activationTestServices(t, &starts, &logins))
	a.mu.Lock()
	a.kubernetesUnavailable = true
	a.kubeconfig = filepath.Join(a.directory, "kubeconfig-empty")
	a.mu.Unlock()
	for _, enable := range []bool{false, true} {
		if enable {
			if err := a.enable(); err != nil {
				t.Fatal(err)
			}
		}
		status := doctorTestStatus(t, control)
		if !status.KubernetesUnavailable || status.Config.AKS == nil || status.Kubeconfig != a.kubeconfig || status.Enabled != enable {
			t.Fatalf("Kubernetes limitation was lost or misreported: %+v", status)
		}
	}
}

func TestDoctorSessionStatusDoesNotPublishFailedActivation(t *testing.T) {
	var starts, logins atomic.Int32
	services := activationTestServices(t, &starts, &logins)
	services.loginPodman = func(context.Context, profile.Profile, *podmanSession) error {
		return errors.New("synthetic login failure")
	}
	a, control := doctorTestController(t, platformTestConfig(t), services)
	if err := a.enable(); err == nil {
		t.Fatal("expected activation failure")
	}
	status := doctorTestStatus(t, control)
	if status.Enabled || status.LoginRefreshed || len(status.Environment) != 0 {
		t.Fatalf("failed activation published capability: %+v", status)
	}
}

func TestDoctorWithoutTargetRequiresLiveSession(t *testing.T) {
	for _, session := range []string{"", "stale-session"} {
		t.Run(session, func(t *testing.T) {
			t.Setenv("BIVROST_SESSION", session)
			t.Setenv("BIVROST_CONTROL_FILE", filepath.Join(t.TempDir(), "missing.json"))
			var out bytes.Buffer
			err := runDoctor(context.Background(), cli.Command{Kind: cli.Doctor}, &out)
			if err == nil {
				t.Fatal("doctor without target accepted absent session")
			}
			if out.Len() != 0 {
				t.Fatal("doctor probed tools before obtaining a target")
			}
		})
	}
}

func TestDoctorSessionKeepsCustomProfileSnapshot(t *testing.T) {
	var starts, logins atomic.Int32
	configured := platformTestConfig(t)
	path := filepath.Join(t.TempDir(), "custom.json")
	configuredJSON, _ := json.Marshal(configured)
	if err := os.WriteFile(path, configuredJSON, 0600); err != nil {
		t.Fatal(err)
	}
	effective, err := withPrivateHosts(configured, []string{"session-only.example"})
	if err != nil {
		t.Fatal(err)
	}
	_, control := doctorTestController(t, effective, activationTestServices(t, &starts, &logins))
	// A later file edit must not change the target already connected by the shell.
	configured.Registry = "differentregistry"
	changed, _ := json.Marshal(configured)
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	status := doctorTestStatus(t, control)
	got, _ := json.Marshal(status.Config)
	want, _ := json.Marshal(effective)
	var wantJSON, gotJSON any
	json.Unmarshal(want, &wantJSON)
	json.Unmarshal(got, &gotJSON)
	if !reflect.DeepEqual(wantJSON, gotJSON) {
		t.Fatal("session configuration no longer matches connected snapshot")
	}
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	err = runDoctor(context.Background(), cli.Command{Kind: cli.Doctor}, &out)
	if err == nil || !strings.Contains(out.String(), "[MISSING] az:") {
		t.Fatalf("no-target doctor did not select live custom profile: %v, %s", err, out.String())
	}
}

func doctorTestActiveEnvironment(t *testing.T, c profile.Profile, status *doctorSessionStatus) {
	t.Helper()
	for _, key := range []string{"CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINER_SSHKEY", "CONTAINERS_CONF_OVERRIDE"} {
		t.Setenv(key, status.Environment[key])
	}
	for key, value := range map[string]string{"HTTPS_PROXY": c.ProxyURL(), "HTTP_PROXY": c.ProxyURL(), "NO_PROXY": "127.0.0.1,localhost", "ALL_PROXY": ""} {
		t.Setenv(key, value)
		if runtime.GOOS != "windows" {
			t.Setenv(strings.ToLower(key), "")
		}
	}
}

func TestDoctorSessionEnvironmentRejectsDrift(t *testing.T) {
	c := platformTestConfig(t)
	path := filepath.Join(t.TempDir(), "containers.conf")
	if err := os.WriteFile(path, []byte("[containers]\nhttp_proxy=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status := &doctorSessionStatus{Enabled: true, Environment: map[string]string{"CONTAINERS_CONF_OVERRIDE": path}}
	doctorTestActiveEnvironment(t, c, status)
	if !doctorSessionEnvironment(c, status) {
		t.Fatal("matching native environment rejected")
	}
	for _, test := range []struct{ key, value string }{
		{"CONTAINER_HOST", "ssh://wrong.example/api.sock"},
		{"CONTAINER_CONNECTION", "other-engine"},
		{"CONTAINERS_CONF_OVERRIDE", ""},
		{"CONTAINER_SSHKEY", "other-key"},
		{"HTTPS_PROXY", "http://127.0.0.1:1"},
		{"https_proxy", "http://127.0.0.1:1"},
		{"NO_PROXY", "*"},
		{"ALL_PROXY", "socks5://127.0.0.1:1"},
	} {
		t.Run(test.key, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if doctorSessionEnvironment(c, status) {
				t.Fatal("changed environment accepted as session configuration")
			}
		})
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if doctorSessionEnvironment(c, status) {
		t.Fatal("deleted session configuration accepted")
	}
}

func TestDoctorSessionReportsActivationAndEngineSeparately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable uses POSIX shell")
	}
	for _, test := range []struct {
		name                                 string
		machine, login, drift, engineFailure bool
	}{
		{name: "native", login: true},
		{name: "machine", machine: true, login: true},
		{name: "skipped login"},
		{name: "shell environment drift", login: true, drift: true},
		{name: "engine failed", login: true, engineFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			answer := "false"
			if test.machine {
				answer = "true"
			}
			script := "#!/bin/sh\nprintf '%s\\n' '" + answer + "'\n"
			if test.engineFailure {
				script = "#!/bin/sh\nexit 19\n"
			}
			if err := os.WriteFile(filepath.Join(root, "podman"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root) // Missing az/ssh keep this strictly local and avoid network probes.
			c := platformTestConfig(t)
			conf := filepath.Join(root, "containers.conf")
			if err := os.WriteFile(conf, []byte("[containers]\nhttp_proxy=false\n"), 0600); err != nil {
				t.Fatal(err)
			}
			status := &doctorSessionStatus{Config: c, Enabled: true, Machine: test.machine, LoginRefreshed: test.login, Environment: map[string]string{"CONTAINERS_CONF_OVERRIDE": conf}}
			if test.machine {
				status.Environment["CONTAINER_HOST"] = "ssh://core@localhost:2222/run/user/1000/session/api.sock"
				status.Environment["CONTAINER_SSHKEY"] = filepath.Join(root, "key")
			}
			doctorTestActiveEnvironment(t, c, status)
			if test.drift {
				t.Setenv("HTTPS_PROXY", "")
			}
			var out bytes.Buffer
			_ = platformDoctorWithSession(context.Background(), c, &out, status)
			text := out.String()
			want := "[OK] Podman command environment:"
			if test.drift {
				want = "[ACTION NEEDED] Podman command environment:"
			}
			if test.engineFailure {
				want = "[ACTION NEEDED] Podman session:"
			}
			if !strings.Contains(text, want) {
				t.Fatalf("missing %q: %s", want, text)
			}
			auth := "[NOT VERIFIED] ACR authentication:"
			if test.login {
				auth = "[OK] ACR authentication:"
			}
			if !strings.Contains(text, auth) {
				t.Fatalf("missing independent login result: %s", text)
			}
			if !strings.Contains(text, "[NOT VERIFIED] ACR image push:") {
				t.Fatal("doctor claimed image authorization without live operation")
			}
		})
	}
}

func TestDoctorExplicitTargetUsesOnlyMatchingSessionState(t *testing.T) {
	var starts, logins atomic.Int32
	c := platformTestConfig(t)
	_, _ = doctorTestController(t, c, activationTestServices(t, &starts, &logins))
	t.Setenv("PATH", t.TempDir())
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching", true: "same registry different VM"}[changed], func(t *testing.T) {
			selected := c
			if changed {
				selected.VMResourceID = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/other"
			}
			path := filepath.Join(t.TempDir(), "selected.json")
			data, _ := json.Marshal(selected)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			_ = runDoctor(context.Background(), cli.Command{Kind: cli.Doctor, ConfigPath: path}, &out)
			hasSession := strings.Contains(out.String(), "[NOT VERIFIED] ACR activation:")
			if hasSession == changed {
				t.Fatalf("session matching=%v, changed profile=%v: %s", hasSession, changed, out.String())
			}
		})
	}
}
