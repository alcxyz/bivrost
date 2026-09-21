package session

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestSelectPodmanConnection(t *testing.T) {
	connections := []podmanConnection{
		{Name: "first", Default: true},
		{Name: "selected"},
		{Name: "second-default", Default: true},
	}

	got, err := selectPodmanConnection(connections, "selected")
	if err != nil || got.Name != "selected" {
		t.Fatalf("explicit selection = %+v, %v; want selected", got, err)
	}

	got, err = selectPodmanConnection([]podmanConnection{{Name: "first"}, {Name: "default", Default: true}}, "")
	if err != nil || got.Name != "default" {
		t.Fatalf("default selection = %+v, %v; want default", got, err)
	}

	for name, candidates := range map[string][]podmanConnection{
		"no default":         {{Name: "first"}},
		"ambiguous defaults": connections,
		"duplicate explicit": {{Name: "selected"}, {Name: "selected"}},
		"missing explicit":   {{Name: "first", Default: true}},
	} {
		t.Run(name, func(t *testing.T) {
			selection := ""
			if strings.Contains(name, "explicit") {
				selection = "selected"
			}
			if _, err := selectPodmanConnection(candidates, selection); err == nil {
				t.Fatal("expected ambiguous or missing selection to fail")
			}
		})
	}
}

func TestMatchPodmanMachineValidatesLocalMachine(t *testing.T) {
	identity := filepath.Join(t.TempDir(), "machine identity")
	machine := testPodmanMachine(identity, false, "core")
	connection := testPodmanConnection(identity, "core", "127.0.0.1", machine.SSHConfig.Port)

	if got, err := matchPodmanMachine(connection, []podmanMachine{machine}); err != nil || got == nil || got.Name != machine.Name {
		t.Fatalf("valid rootless machine = %+v, %v", got, err)
	}

	named := testPodmanMachine(identity, false, "Core_user")
	named.Name = "Work_VM.1"
	namedConnection := testPodmanConnection(identity, "Core_user", "127.0.0.1", named.SSHConfig.Port)
	if _, err := matchPodmanMachine(namedConnection, []podmanMachine{named}); err != nil {
		t.Fatal("valid machine and user names rejected:", err)
	}

	rootful := testPodmanMachine(identity, true, "core")
	rootConnection := testPodmanConnectionPath(identity, "root", "localhost", rootful.SSHConfig.Port, "/run/podman/podman.sock")
	if got, err := matchPodmanMachine(rootConnection, []podmanMachine{rootful}); err != nil || got == nil {
		t.Fatalf("valid rootful machine = %+v, %v", got, err)
	}

	tests := []struct {
		name       string
		connection podmanConnection
		machine    podmanMachine
	}{
		{"non-loopback host", testPodmanConnection(identity, "core", "machine.example", machine.SSHConfig.Port), machine},
		{"wrong port", testPodmanConnection(identity, "core", "127.0.0.1", machine.SSHConfig.Port+1), machine},
		{"stopped VM", connection, func() podmanMachine { m := machine; m.State = "stopped"; return m }()},
		{"wrong identity", testPodmanConnection(filepath.Join(t.TempDir(), "other identity"), "core", "127.0.0.1", machine.SSHConfig.Port), machine},
		{"unsafe username", testPodmanConnection(identity, "-unsafe", "127.0.0.1", machine.SSHConfig.Port), func() podmanMachine { m := machine; m.SSHConfig.RemoteUsername = "-unsafe"; return m }()},
		{"root on rootless VM", testPodmanConnection(identity, "root", "127.0.0.1", machine.SSHConfig.Port), machine},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := matchPodmanMachine(tt.connection, []podmanMachine{tt.machine}); err == nil || got != nil {
				t.Fatalf("matchPodmanMachine() = %+v, %v; want rejection", got, err)
			}
		})
	}
}

func TestPodmanBridgeArgumentsSecureLoopbackForward(t *testing.T) {
	identity := filepath.Join(t.TempDir(), "identity with spaces")
	knownHosts := filepath.Join(t.TempDir(), "known hosts", "podman keys")
	machine := testPodmanMachine(identity, false, "core")
	const proxyPort = 28080

	got := podmanBridgeArguments(machine, proxyPort, knownHosts)
	want := []string{
		"-F", os.DevNull, "-T", "-i", identity,
		"-p", strconv.Itoa(machine.SSHConfig.Port), "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=" + strconv.Quote(filepath.ToSlash(knownHosts)),
		"-o", "HostKeyAlias=bivrost-podman-" + machine.Name, "-o", "ExitOnForwardFailure=yes",
		"-o", "ForwardAgent=no", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-R", "127.0.0.1:28080:127.0.0.1:28080",
		"core@127.0.0.1", "printf 'bivrost-podman-ready\\n'; cat",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("podmanBridgeArguments()\n got: %q\nwant: %q", got, want)
	}
	if got[4] != identity {
		t.Fatalf("identity path with spaces changed: %q", got[4])
	}
}

func testPodmanMachine(identity string, rootful bool, username string) podmanMachine {
	m := podmanMachine{Name: "podman-machine-default", State: "running", Rootful: rootful}
	m.SSHConfig.IdentityPath = identity
	m.SSHConfig.Port = 54321
	m.SSHConfig.RemoteUsername = username
	return m
}

func testPodmanConnection(identity, username, host string, port int) podmanConnection {
	return testPodmanConnectionPath(identity, username, host, port, "/run/user/1000/podman/podman.sock")
}

func testPodmanConnectionPath(identity, username, host string, port int, path string) podmanConnection {
	u := url.URL{Scheme: "ssh", User: url.User(username), Host: host + ":" + strconv.Itoa(port), Path: path}
	return podmanConnection{Name: "podman-machine-default", URI: u.String(), Identity: identity, Default: true}
}
