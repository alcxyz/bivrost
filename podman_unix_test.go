//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPodmanBridgeLifecycle(t *testing.T) {
	for _, ready := range []bool{true, false} {
		t.Run(fmt.Sprint(ready), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", root)
			t.Setenv("CONTAINER_HOST", "")
			t.Setenv("CONTAINER_CONNECTION", "test-machine")
			identity := filepath.Join(root, "machine-identity")
			machine := podmanMachine{Name: "test-machine", State: "running"}
			machine.SSHConfig.Port = 32199
			machine.SSHConfig.RemoteUsername = "core"
			machine.SSHConfig.IdentityPath = identity
			connections, _ := json.Marshal([]podmanConnection{{Name: machine.Name, URI: "ssh://core@127.0.0.1:32199/run/user/1000/podman/podman.sock", Identity: identity, Default: true}})
			machines, _ := json.Marshal([]podmanMachine{machine})
			// Static JSON fixtures are emitted through a quoted heredoc. No actual
			// Podman machine, SSH connection, or identity file is used by this test.
			podman := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n*'connection list'*) cat <<'CONNECTIONS'\n%s\nCONNECTIONS\n;;\n*'machine list'*) echo '[{\"Name\":\"test-machine\"}]';;\n*'machine inspect'*) cat <<'MACHINES'\n%s\nMACHINES\n;;\n*info*) echo true;;\n*) exit 2;;\nesac\n", connections, machines)
			writeTestExecutable(t, filepath.Join(root, "podman"), podman)
			ssh := "#!/bin/sh\nexit 7\n"
			if ready {
				ssh = "#!/bin/sh\nprintf 'bivrost-podman-ready\\n'\nread ignored\n"
			}
			writeTestExecutable(t, filepath.Join(root, "ssh"), ssh)
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			bridge, err := startPodmanProxyBridge(ctx, config{ProxyPort: 28080})
			if !ready {
				if err == nil {
					bridge.stop()
					t.Fatal("failed SSH setup must not report a ready bridge")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if bridge == nil {
				t.Fatal("remote machine needs a bridge")
			}
			bridge.stop()
			select {
			case <-bridge.done:
			case <-time.After(time.Second):
				t.Fatal("bridge did not stop")
			}
		})
	}
}

func TestPodmanBridgeReadinessCommand(t *testing.T) {
	args := podmanBridgeArguments(podmanMachine{}, 28080, "/tmp/known_hosts")
	output, err := exec.Command("/bin/sh", "-c", args[len(args)-1]).Output()
	if err != nil || string(output) != "bivrost-podman-ready\n" {
		t.Fatalf("readiness output %q, error %v", output, err)
	}
}

func TestRequireProxyChecksSelectedPodmanBridge(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CONTAINER_HOST", "")
	t.Setenv("CONTAINER_CONNECTION", "test-machine")
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	machine := testPodmanMachine(filepath.Join(root, "identity"), false, "core")
	machine.Name = "test-machine"
	connections, _ := json.Marshal([]podmanConnection{{Name: machine.Name, URI: fmt.Sprintf("ssh://core@127.0.0.1:%d/run/user/1000/podman/podman.sock", machine.SSHConfig.Port), Identity: machine.SSHConfig.IdentityPath, Default: true}})
	machines, _ := json.Marshal([]podmanMachine{machine})
	podman := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n*'connection list'*) cat <<'CONNECTIONS'\n%s\nCONNECTIONS\n;;\n*'machine list'*) echo '[{\"Name\":\"test-machine\"}]';;\n*'machine inspect'*) cat <<'MACHINES'\n%s\nMACHINES\n;;\n*info*) echo true;;\n*) exit 2;;\nesac\n", connections, machines)
	writeTestExecutable(t, filepath.Join(root, "podman"), podman)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := config{Registry: "exampleregistry", ProxyPort: availablePort(t), SOCKSPort: availablePort(t)}
	for c.SOCKSPort == c.ProxyPort {
		c.SOCKSPort = availablePort(t)
	}
	p, err := startProxy(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	assertMissingBridge := func() {
		t.Helper()
		if err := requireProxy(context.Background(), c); err == nil || !strings.Contains(err.Error(), "no ready forward") {
			t.Fatalf("expected actionable missing bridge error, got %v", err)
		}
	}
	// A host listener (including a Docker-mode or older proxy) cannot claim VM readiness.
	assertMissingBridge()
	done := make(chan struct{})
	wrongMachine := machine
	wrongMachine.Name = "other-machine"
	p.setPodmanBridge(&podmanProxyBridge{child: &child{done: done}, machine: wrongMachine})
	assertMissingBridge()
	p.setPodmanBridge(&podmanProxyBridge{child: &child{done: done}, machine: machine})
	if err := requireProxy(context.Background(), c); err != nil {
		t.Fatalf("matching allocated bridge rejected: %v", err)
	}
	close(done)
	assertMissingBridge()
	// Native Linux Podman needs only the host proxy.
	t.Setenv("CONTAINER_CONNECTION", "")
	writeTestExecutable(t, filepath.Join(root, "podman"), "#!/bin/sh\necho false\n")
	if err := requireProxy(context.Background(), c); err != nil {
		t.Fatalf("native Podman required a VM bridge: %v", err)
	}

	// Exercise the actual producer path too: serveProxy must publish the bridge
	// obtained from the SSH readiness handshake, then stop it with the proxy.
	p.close()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("CONTAINER_CONNECTION", "test-machine")
	writeTestExecutable(t, filepath.Join(root, "podman"), podman)
	writeTestExecutable(t, filepath.Join(root, "ssh"), "#!/bin/sh\nprintf 'bivrost-podman-ready\\n'\nread ignored\n")
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- serveProxy(ctx, c) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serverDone:
			if err != nil {
				t.Errorf("serveProxy shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Podman proxy did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := requireProxy(context.Background(), c)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serveProxy did not publish its ready VM forward: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPodmanCommandsIgnoreDockerConfiguration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CONTAINER_HOST", "")
	t.Setenv("CONTAINER_CONNECTION", "")
	t.Setenv("DOCKER_HOST", "tcp://docker.invalid:2376")
	t.Setenv("DOCKER_CONTEXT", "unrelated-docker")
	writeTestExecutable(t, filepath.Join(root, "podman"), "#!/bin/sh\necho false\n")
	writeTestExecutable(t, filepath.Join(root, "docker"), "#!/bin/sh\nexit 99\n")
	t.Setenv("PATH", root)
	c := config{Registry: "registry", ProxyPort: 28080}
	if err := requirePodmanForACR(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	cmd, err := registryLoginCommand(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Args[0] != "podman" || cmd.Args[1] != "--remote=false" || cmd.Args[2] != "login" {
		t.Fatalf("login must use native Podman: %q", cmd.Args)
	}
	if os.Getenv("DOCKER_CONTEXT") != "unrelated-docker" || os.Getenv("DOCKER_HOST") != "tcp://docker.invalid:2376" {
		t.Fatal("Podman setup changed Docker selection")
	}
	if err := os.Remove(filepath.Join(root, "podman")); err != nil {
		t.Fatal(err)
	}
	if err := requirePodmanForACR(context.Background(), c); err == nil {
		t.Fatal("missing Podman must not fall back to Docker")
	}
}
