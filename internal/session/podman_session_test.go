package session

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
)

func TestPodmanSessionEnvironment(t *testing.T) {
	original := []string{"PATH=/bin", "CONTAINER_HOST=old", "container_connection=old", "CONTAINER_SSHKEY=old", "CONTAINERS_CONF_OVERRIDE=old", "HTTPS_PROXY=keep"}
	for _, s := range []*podmanSession{{configuration: "/owned/config"}, {configuration: "/owned/config", host: "ssh://root@127.0.0.1:2222/run/bivrost-session.ABC123xyz456/api.sock", identity: "/key"}} {
		result := s.environment(original)
		if shellinit.EnvironmentValue(result, "CONTAINER_CONNECTION") != "" {
			t.Fatal("old connection retained")
		}
		if shellinit.EnvironmentValue(result, "PATH") != "/bin" || shellinit.EnvironmentValue(result, "HTTPS_PROXY") != "keep" {
			t.Fatal("unrelated environment lost")
		}
		if s.host != "" {
			if shellinit.EnvironmentValue(result, "CONTAINER_HOST") != s.host || shellinit.EnvironmentValue(result, "CONTAINER_SSHKEY") != "/key" || shellinit.EnvironmentValue(result, "CONTAINERS_CONF_OVERRIDE") != "/owned/config" {
				t.Fatal("machine environment incorrect")
			}
		} else if shellinit.EnvironmentValue(result, "CONTAINERS_CONF_OVERRIDE") != "/owned/config" || shellinit.EnvironmentValue(result, "CONTAINER_HOST") != "" {
			t.Fatal("native environment incorrect")
		}
	}
	if original[1] != "CONTAINER_HOST=old" {
		t.Fatal("input mutated")
	}
}

func TestPodmanSessionRejectsExistingOverride(t *testing.T) {
	t.Setenv("CONTAINERS_CONF_OVERRIDE", "/existing/config")
	if _, err := startPodmanSession(context.Background(), profile.Profile{}, t.TempDir()); err == nil {
		t.Fatal("accepted conflicting override")
	}
}

func TestPodmanSessionSocketValidation(t *testing.T) {
	for _, path := range []string{"/run/bivrost-session.ABC123xyz456/api.sock", "/run/user/1000/bivrost-session.ABC123xyz456/api.sock"} {
		if !podmanSessionSocketPattern.MatchString(path) {
			t.Fatalf("rejected owned path %q", path)
		}
	}
	for _, path := range []string{"/run/podman/podman.sock", "/run/user/1000/../bivrost-session.ABC123xyz456/api.sock", "/run/bivrost-session.ABC123xyz456/api.sock\nextra", "/tmp/bivrost-session.ABC123xyz456/api.sock"} {
		if podmanSessionSocketPattern.MatchString(path) {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
}

// A real child and Unix socket exercise the supervisor's stdin ownership, lease,
// signal handling, and cleanup without contacting any Podman engine.
func TestPodmanSessionSupervisorCleanup(t *testing.T) {
	t.Setenv("CONTAINERS_CONF_OVERRIDE", "")
	if runtime.GOOS == "windows" {
		t.Skip("machine supervisor executes on Linux")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	for _, mode := range []string{"eof", "lease", "term"} {
		t.Run(mode, func(t *testing.T) {
			// macOS's test temp directory can exceed the Unix socket path limit
			// once the owned session directory and socket name are appended.
			root, err := os.MkdirTemp("/tmp", "bivrost-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(root) })
			wrapper := filepath.Join(root, "podman")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
			if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quote(executable)+" -test.run=^TestPodmanSessionServiceHelper$ -- \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			script := podmanSessionSupervisor(32123, 1)
			script = strings.Replace(script, "base=/run/user/$uid\nif [ \"$uid\" = 0 ]; then base=/run; fi", "base="+quote(root), 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bash, "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "BIVROST_TEST_SERVICE=1", "BIVROST_TEST_STOPPED="+filepath.Join(root, "stopped"))
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil {
				t.Fatalf("supervisor readiness: %v", err)
			}
			owned := filepath.Dir(strings.TrimSpace(strings.TrimPrefix(line, "bivrost-podman-session ")))
			if !strings.HasPrefix(owned, root+"/bivrost-session.") {
				t.Fatalf("unexpected readiness %q", line)
			}
			switch mode {
			case "eof":
				stdin.Close()
			case "term":
				cmd.Process.Signal(syscall.SIGTERM)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("supervisor exit: %v", err)
			}
			if _, err := os.Stat(owned); !os.IsNotExist(err) {
				t.Fatal("owned session directory retained")
			}
			if _, err := os.Stat(filepath.Join(root, "stopped")); err != nil {
				t.Fatal("owned API child did not stop")
			}
			if _, err := os.Stat(wrapper); err != nil {
				t.Fatal("cleanup removed unrelated file")
			}
		})
	}
}

func TestPodmanSessionServiceHelper(t *testing.T) {
	if os.Getenv("BIVROST_TEST_SERVICE") != "1" {
		return
	}
	if os.Getenv("HTTP_PROXY") != "http://127.0.0.1:32123" || os.Getenv("HTTPS_PROXY") != "http://127.0.0.1:32123" || os.Getenv("ALL_PROXY") != "" || os.Getenv("CONTAINERS_CONF_OVERRIDE") != "" {
		os.Exit(5)
	}
	module := ""
	for i, arg := range os.Args {
		if arg == "--module" && i+1 < len(os.Args) {
			module = os.Args[i+1]
		}
	}
	configuration, err := os.ReadFile(module)
	if err != nil || string(configuration) != "[containers]\nhttp_proxy = false\n" {
		os.Exit(6)
	}
	endpoint := os.Args[len(os.Args)-1]
	if !strings.HasPrefix(endpoint, "unix://") {
		os.Exit(2)
	}
	listener, err := net.Listen("unix", strings.TrimPrefix(endpoint, "unix://"))
	if err != nil {
		os.Exit(3)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	<-signals
	listener.Close()
	if err := os.WriteFile(os.Getenv("BIVROST_TEST_STOPPED"), []byte("stopped"), 0600); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

func TestPodmanSessionSupervisorRejectsExistingOverride(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	t.Setenv("CONTAINERS_CONF_OVERRIDE", "/user/existing.conf")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, "-c", podmanSessionSupervisor(32123, 1))
	if err := cmd.Run(); err == nil {
		t.Fatal("supervisor accepted existing override")
	}
	if ctx.Err() != nil {
		t.Fatal("supervisor did not reject override immediately")
	}
}

func TestPodmanSessionChecksOwnedAPI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake requires Unix")
	}
	directory := t.TempDir()
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	session := &podmanSession{configuration: "/owned/config", host: "ssh://root@127.0.0.1:2222/run/bivrost-session.ABC123xyz456/api.sock", identity: "/owned/key"}
	for _, result := range []string{"true", "false", "unrecognized"} {
		script := "#!/bin/sh\n[ \"$CONTAINERS_CONF_OVERRIDE\" = /owned/config ] || exit 1\n[ \"$CONTAINER_SSHKEY\" = /owned/key ] || exit 1\n[ -n \"$CONTAINER_HOST\" ] || exit 1\nprintf '" + result + "\\n'\n"
		if err := os.WriteFile(filepath.Join(directory, "podman"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		if got := session.checkAPI(context.Background()); got != (result == "true") {
			t.Fatalf("API result %q accepted: %t", result, got)
		}
	}
}
