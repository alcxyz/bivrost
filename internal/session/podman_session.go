package session

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/podman"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
)

const podmanBuildGuidance = "Podman builds default to --http-proxy=false in this session; explicit flags are preserved."

// A session owns its API service and configuration, never the machine's normal
// service. The supervisor retains SSH stdin because podman system service does
// not. EOF or an expired heartbeat lease stops the owned service.
type podmanSession struct {
	configuration    string
	wrapperDirectory string
	host             string
	identity         string
	process          *child
	input            io.WriteCloser
	done             <-chan struct{}
	once             sync.Once
}

var podmanSessionSocketPattern = regexp.MustCompile(`^/run/(user/[0-9]+/)?bivrost-session\.[A-Za-z0-9]{12}/api\.sock$`)

func startPodmanSession(ctx context.Context, c profile.Profile, directory string) (result *podmanSession, resultErr error) {
	if os.Getenv("CONTAINERS_CONF_OVERRIDE") != "" {
		return nil, errors.New("unset CONTAINERS_CONF_OVERRIDE before starting a Bivrost Podman session; its existing override cannot be safely combined")
	}
	machine, err := inspectPodman(ctx)
	if err != nil {
		return nil, err
	}
	session := &podmanSession{configuration: filepath.Join(directory, "podman-session.conf")}
	defer func() {
		if resultErr != nil {
			session.close()
		}
	}()
	configuration := "[containers]\nhttp_proxy = false\n"
	if machine == nil {
		configuration += "[engine]\nremote = false\n"
	}
	if err := os.WriteFile(session.configuration, []byte(configuration), 0600); err != nil {
		return nil, errors.New("cannot write session Podman configuration")
	}
	session.wrapperDirectory, err = podman.PrepareWrapper(directory)
	if err != nil {
		return nil, err
	}
	if machine == nil {
		return session, nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return nil, errors.New("cannot locate Podman session host-key directory")
	}
	root = filepath.Join(root, "bivrost")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.New("cannot create Podman session host-key directory")
	}
	args := podmanBridgeArguments(*machine, c.ProxyPort, filepath.Join(root, "podman_known_hosts"))
	args[len(args)-2] = machine.ConnectionUser + "@127.0.0.1"
	args[len(args)-1] = "bash -c '" + strings.ReplaceAll(podmanSessionSupervisor(c.ProxyPort, 20), "'", "'\"'\"'") + "'"
	cmd := exec.CommandContext(ctx, "ssh", args...)
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("cannot prepare Podman session supervisor")
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, errors.New("cannot prepare Podman session supervisor")
	}
	p, err := startChild(cmd)
	if err != nil {
		input.Close()
		return nil, errors.New("cannot start Podman session supervisor; check OpenSSH and machine SSH access")
	}
	session.process, session.input, session.done = p, input, p.done
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			if _, err := io.WriteString(input, "bivrost-heartbeat\n"); err != nil {
				return
			}
			select {
			case <-p.done:
				return
			case <-ticker.C:
			}
		}
	}()
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(io.LimitReader(output, 256)).ReadString('\n')
		ready <- line
	}()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case line := <-ready:
		path := strings.TrimSuffix(strings.TrimPrefix(line, "bivrost-podman-session "), "\n")
		if strings.HasPrefix(line, "bivrost-podman-session ") && strings.HasSuffix(line, "\n") && podmanSessionSocketPattern.MatchString(path) {
			endpoint := url.URL{Scheme: "ssh", User: url.User(machine.ConnectionUser), Host: "127.0.0.1:" + strconv.Itoa(machine.SSHConfig.Port), Path: path}
			session.host, session.identity = endpoint.String(), machine.SSHConfig.IdentityPath
			if session.checkAPI(ctx) {
				return session, nil
			}
		}
	case <-ctx.Done():
	case <-timer.C:
	}
	session.close()
	return nil, errors.New("Podman session API failed to start; check machine SSH access and its loopback proxy port")
}

func (s *podmanSession) environment(env []string) []string {
	result := make([]string, 0, len(env)+3)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINER_SSHKEY", "CONTAINERS_CONF_OVERRIDE":
			continue
		}
		result = append(result, entry)
	}
	result = append(result, "CONTAINERS_CONF_OVERRIDE="+s.configuration)
	if s.wrapperDirectory != "" {
		result = shellinit.ReplaceEnvironment(result, "BIVROST_PODMAN_BIN", s.wrapperDirectory)
		if env != nil {
			result = shellinit.ReplaceEnvironment(result, "PATH", s.wrapperDirectory+string(os.PathListSeparator)+shellinit.EnvironmentValue(env, "PATH"))
		}
	}
	if s.host != "" {
		result = append(result, "CONTAINER_HOST="+s.host, "CONTAINER_SSHKEY="+s.identity)
	}
	return result
}

// Socket creation alone does not establish that the API can serve requests.
func (s *podmanSession) checkAPI(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "podman", "info", "--format", "{{.Host.ServiceIsRemote}}")
	cmd.Env = s.environment(os.Environ())
	output, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}

func (s *podmanSession) loginCommand(ctx context.Context, c profile.Profile) (*exec.Cmd, error) {
	args := []string{"login", c.Registry + ".azurecr.io", "--username", "00000000-0000-0000-0000-000000000000", "--password-stdin"}
	if s.host == "" {
		args = append([]string{"--remote=false"}, args...)
	}
	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Env = s.environment(proxyEnvironment(os.Environ(), c.ProxyURL()))
	return cmd, nil
}

func (s *podmanSession) close() {
	s.once.Do(func() {
		if s.input != nil {
			s.input.Close()
		}
		if s.process != nil {
			select {
			case <-s.done:
			case <-time.After(3 * time.Second):
				s.process.stop()
			}
		}
		if s.wrapperDirectory != "" {
			podman.CleanupWrapper(s.wrapperDirectory, os.Stderr)
		}
		if s.configuration != "" {
			os.Remove(s.configuration)
		}
	})
}

func podmanSessionSupervisor(port, leaseSeconds int) string {
	return fmt.Sprintf(`set -eu
[ -z "${CONTAINERS_CONF_OVERRIDE:-}" ] || exit 1
umask 077
uid=$(id -u)
base=/run/user/$uid
if [ "$uid" = 0 ]; then base=/run; fi
owned=$(mktemp -d "$base/bivrost-session.XXXXXXXXXXXX")
service=
cleanup() {
 trap - EXIT HUP INT TERM
 if [ -n "$service" ]; then
  kill "$service" 2>/dev/null || true
  for ((attempt=0; attempt<20; attempt++)); do
   kill -0 "$service" 2>/dev/null || break
   sleep 0.1
  done
  if [ "$attempt" = 20 ]; then kill -KILL "$service" 2>/dev/null || true; fi
  wait "$service" 2>/dev/null || true
 fi
 rm -rf -- "$owned"
}
trap cleanup EXIT
trap 'exit 0' HUP INT TERM
printf '[containers]\nhttp_proxy = false\n' > "$owned/containers.conf"
env -u CONTAINER_HOST -u CONTAINER_CONNECTION -u ALL_PROXY -u all_proxy -u http_proxy -u https_proxy -u no_proxy HTTP_PROXY=http://127.0.0.1:%d HTTPS_PROXY=http://127.0.0.1:%d NO_PROXY=127.0.0.1,localhost podman --remote=false --module "$owned/containers.conf" system service --time 0 "unix://$owned/api.sock" </dev/null >/dev/null 2>&1 &
service=$!
for ((attempt=0; attempt<100; attempt++)); do
 if [ -S "$owned/api.sock" ]; then break; fi
 kill -0 "$service" 2>/dev/null || exit 1
 sleep 0.1
done
[ -S "$owned/api.sock" ] || exit 1
printf 'bivrost-podman-session %%s/api.sock\n' "$owned"
while IFS= read -r -t %d heartbeat; do
 [ "$heartbeat" = bivrost-heartbeat ] || exit 1
 kill -0 "$service" 2>/dev/null || exit 1
done
`, port, port, leaseSeconds)
}
