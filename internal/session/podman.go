package session

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
)

var podmanNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var podmanUserPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

type podmanConnection struct {
	Name     string
	URI      string
	Identity string
	Default  bool
}

type podmanMachine struct {
	ConnectionName string `json:"-"`
	ConnectionUser string `json:"-"`
	Name           string
	State          string
	Rootful        bool
	SSHConfig      struct {
		IdentityPath   string
		Port           int
		RemoteUsername string
	}
}

func podmanOutput(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// These commands return metadata only. Never print it or raw command errors.
	return exec.CommandContext(ctx, "podman", args...).Output()
}

func inspectPodman(ctx context.Context) (*podmanMachine, error) {
	if os.Getenv("CONTAINER_HOST") != "" {
		return nil, errors.New("unset CONTAINER_HOST and select a local Podman Machine with podman system connection default; arbitrary remote endpoints are not supported")
	}
	var output []byte
	var err error
	// Native Linux may use local storage without a system connection. Windows and
	// macOS always resolve the local machine before contacting its API service.
	if runtime.GOOS == "linux" && os.Getenv("CONTAINER_CONNECTION") == "" {
		output, err = podmanOutput(ctx, "--remote=false", "info", "--format", "{{.Host.ServiceIsRemote}}")
		if err != nil {
			return nil, errors.New("Podman is unavailable; install Podman and start its local engine or machine")
		}
		switch strings.TrimSpace(string(output)) {
		case "false":
			return nil, nil
		case "true":
		default:
			return nil, errors.New("cannot determine Podman connection mode; update Podman")
		}
	}
	output, err = podmanOutput(ctx, "system", "connection", "list", "--format", "json")
	var connections []podmanConnection
	if err != nil || json.Unmarshal(output, &connections) != nil {
		return nil, errors.New("cannot inspect Podman connections")
	}
	selected, err := selectPodmanConnection(connections, os.Getenv("CONTAINER_CONNECTION"))
	if err != nil {
		return nil, err
	}
	output, err = podmanOutput(ctx, "machine", "list", "--format", "json")
	var names []struct{ Name string }
	if err != nil || json.Unmarshal(output, &names) != nil {
		return nil, errors.New("cannot list local Podman Machines")
	}
	var machines []podmanMachine
	for _, name := range names {
		if !podmanNamePattern.MatchString(name.Name) {
			continue
		}
		output, err = podmanOutput(ctx, "machine", "inspect", name.Name)
		var inspected []podmanMachine
		if err == nil && json.Unmarshal(output, &inspected) == nil {
			machines = append(machines, inspected...)
		}
	}
	machine, err := matchPodmanMachine(selected, machines)
	if err != nil {
		return nil, err
	}
	output, err = podmanOutput(ctx, "--connection", machine.ConnectionName, "info", "--format", "{{.Host.ServiceIsRemote}}")
	if err != nil || strings.TrimSpace(string(output)) != "true" {
		return nil, errors.New("selected local Podman Machine API is unavailable; start the machine and check its service")
	}
	return machine, nil
}

func selectPodmanConnection(connections []podmanConnection, name string) (podmanConnection, error) {
	var selected []podmanConnection
	for _, c := range connections {
		if (name != "" && c.Name == name) || (name == "" && c.Default) {
			selected = append(selected, c)
		}
	}
	if len(selected) != 1 {
		return podmanConnection{}, errors.New("select one local Podman Machine connection with podman system connection default")
	}
	return selected[0], nil
}

func matchPodmanMachine(c podmanConnection, machines []podmanMachine) (*podmanMachine, error) {
	u, err := url.Parse(c.URI)
	if err != nil || u.Scheme != "ssh" || u.User == nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		return nil, errors.New("Podman must target a local Podman Machine; remote image storage is outside Bivrost's scope")
	}
	if _, password := u.User.Password(); password {
		return nil, errors.New("Podman connection must use its machine SSH identity")
	}
	for _, m := range machines {
		ssh := m.SSHConfig
		userMatches := u.User.Username() == ssh.RemoteUsername || (u.User.Username() == "root" && u.Path == "/run/podman/podman.sock")
		if m.State == "running" && podmanNamePattern.MatchString(m.Name) && userMatches &&
			podmanUserPattern.MatchString(ssh.RemoteUsername) && ssh.Port > 0 && ssh.Port <= 65535 && u.Port() == strconv.Itoa(ssh.Port) &&
			(u.Path == "/run/podman/podman.sock" || strings.HasPrefix(u.Path, "/run/user/") && strings.HasSuffix(u.Path, "/podman/podman.sock")) &&
			filepath.IsAbs(ssh.IdentityPath) && filepath.Clean(c.Identity) == filepath.Clean(ssh.IdentityPath) {
			m.ConnectionName = c.Name
			m.ConnectionUser = u.User.Username()
			return &m, nil
		}
	}
	return nil, errors.New("selected Podman connection does not match a running local Podman Machine; start it and select its connection")
}

type podmanProxyBridge struct {
	*child
	machine podmanMachine
}

// Hash only the metadata that identifies the VM SSH destination. Rootful and
// rootless API connections to the same VM can share its loopback forward.
// This is a compatibility check, not authentication of the local proxy.
func (m podmanMachine) proxyBridgeID() string {
	metadata, _ := json.Marshal(struct {
		Name     string
		Port     int
		User     string
		Identity string
	}{m.Name, m.SSHConfig.Port, m.SSHConfig.RemoteUsername, filepath.Clean(m.SSHConfig.IdentityPath)})
	sum := sha256.Sum256(metadata)
	return hex.EncodeToString(sum[:])
}

// The reverse forward lives with acr proxy, not the Bastion session, so public
// registry traffic keeps working after Bastion disconnects. Both ends bind to
// loopback. Podman's own SSH identity is consumed by ssh, never read by Bivrost.
func startPodmanProxyBridge(ctx context.Context, c profile.Profile) (bridge *podmanProxyBridge, resultErr error) {
	finish := diagnostics.Step(ctx, diagnostics.EventPodmanBridge)
	defer func() { finish(resultErr) }()
	machine, err := inspectPodman(ctx)
	if err != nil || machine == nil {
		return nil, err
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, errors.New("cannot locate Podman bridge host-key directory")
	}
	root = filepath.Join(root, "bivrost")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.New("cannot create Podman bridge host-key directory")
	}
	args := podmanBridgeArguments(*machine, c.ProxyPort, filepath.Join(root, "podman_known_hosts"))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("cannot prepare Podman proxy bridge")
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, errors.New("cannot prepare Podman proxy bridge")
	}
	p, err := startChild(cmd)
	if err != nil {
		input.Close()
		return nil, errors.New("cannot start Podman proxy bridge; verify OpenSSH and the machine SSH identity")
	}
	ready := make(chan bool, 1)
	go func() {
		line, _ := bufio.NewReader(io.LimitReader(output, 128)).ReadString('\n')
		ready <- line == "bivrost-podman-ready\n"
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case ok := <-ready:
		if ok {
			return &podmanProxyBridge{child: p, machine: *machine}, nil
		}
	case <-ctx.Done():
	case <-timer.C:
	}
	input.Close()
	p.stop()
	return nil, errors.New("Podman proxy bridge failed; check the machine SSH host key and that its loopback proxy port is free")
}

func podmanBridgeArguments(m podmanMachine, port int, knownHosts string) []string {
	return []string{"-F", os.DevNull, "-T", "-i", m.SSHConfig.IdentityPath,
		"-p", strconv.Itoa(m.SSHConfig.Port), "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=" + strconv.Quote(filepath.ToSlash(knownHosts)),
		"-o", "HostKeyAlias=bivrost-podman-" + m.Name, "-o", "ExitOnForwardFailure=yes",
		"-o", "ForwardAgent=no", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port),
		m.SSHConfig.RemoteUsername + "@127.0.0.1", "printf 'bivrost-podman-ready\\n'; cat"}
}
