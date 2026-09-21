package session

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
)

func TestProxyEnvironmentReplacesAllProxySpellings(t *testing.T) {
	t.Parallel()

	input := []string{
		"PATH=/custom/bin",
		"http_proxy=http://old-lower.example:8080",
		"HTTP_PROXY=http://old-upper.example:8080",
		"HtTpS_pRoXy=http://old-mixed.example:8080",
		"all_proxy=socks5://old.example:1080",
		"No_PrOxY=old.example",
		"HTTP_PROXY_SUFFIX=keep-me",
		"EMPTY=",
		"MALFORMED_ENTRY",
	}
	proxy := "http://127.0.0.1:18080"
	got := proxyEnvironment(input, proxy)

	wantUnrelated := []string{"PATH=/custom/bin", "HTTP_PROXY_SUFFIX=keep-me", "EMPTY=", "MALFORMED_ENTRY"}
	if want := len(wantUnrelated) + 3; len(got) != want {
		t.Errorf("proxyEnvironment() returned %d entries, want %d: %q", len(got), want, got)
	}
	for _, want := range wantUnrelated {
		if !containsString(got, want) {
			t.Errorf("proxyEnvironment() dropped or changed unrelated entry %q: %q", want, got)
		}
	}

	wantProxy := map[string]string{
		"HTTP_PROXY":  proxy,
		"HTTPS_PROXY": proxy,
		"NO_PROXY":    "127.0.0.1,localhost",
	}
	counts := make(map[string]int)
	for _, entry := range got {
		key, value, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if upper == "ALL_PROXY" {
			t.Errorf("proxyEnvironment() retained ALL_PROXY entry %q", entry)
		}
		if want, ok := wantProxy[upper]; ok {
			counts[upper]++
			if key != upper || value != want {
				t.Errorf("proxyEnvironment() entry = %q, want %s=%s", entry, upper, want)
			}
		}
	}
	for key := range wantProxy {
		if counts[key] != 1 {
			t.Errorf("proxyEnvironment() has %d %s entries, want exactly 1: %q", counts[key], key, got)
		}
	}
}

func TestSSHArgumentsUsePinnedLocalForwardOnly(t *testing.T) {
	t.Parallel()

	c := profile.Profile{
		VMResourceID: "/subscriptions/Sub-1/resourceGroups/RG/providers/Microsoft.Compute/virtualMachines/Jump-1",
		SOCKSPort:    18081,
		SSHUser:      "azureuser",
		IdentityFile: "/keys/jump_ed25519",
	}
	stateRoot := filepath.Join(string(filepath.Separator), "state", "bivrost")
	args := sshArguments(c, "/tmp/session/ssh_config", stateRoot, 32022)

	if got := argumentAfter(t, args, "-D"); got != "127.0.0.1:18081" {
		t.Fatalf("dynamic forward = %q, want explicit loopback address", got)
	}
	if got := args[len(args)-1]; got != "127.0.0.1" {
		t.Fatalf("SSH destination = %q, want explicit loopback address", got)
	}
	if !containsString(args, "-N") || !containsString(args, "-T") {
		t.Fatalf("SSH arguments must disable remote commands and terminal allocation: %q", args)
	}
	for _, arg := range args {
		if strings.EqualFold(arg, "docker") || strings.EqualFold(arg, "exec") {
			t.Fatalf("SSH arguments contain remote command component %q: %q", arg, args)
		}
	}
	if got := sshOption(t, args, "StrictHostKeyChecking"); got != "accept-new" {
		t.Errorf("StrictHostKeyChecking = %q, want accept-new", got)
	}
	if got, want := sshOption(t, args, "UserKnownHostsFile"), `"`+filepath.ToSlash(filepath.Join(stateRoot, "known_hosts"))+`"`; got != want {
		t.Errorf("UserKnownHostsFile = %q, want %q", got, want)
	}
	alias := sshOption(t, args, "HostKeyAlias")
	if !strings.HasPrefix(alias, "bivrost-") || len(alias) != len("bivrost-")+32 {
		t.Fatalf("HostKeyAlias = %q, want stable bivrost alias with a 128-bit hex digest", alias)
	}

	caseVariant := c
	caseVariant.VMResourceID = strings.ToLower(c.VMResourceID)
	if got := sshOption(t, sshArguments(caseVariant, "/different/config", "/different/state", 45022), "HostKeyAlias"); got != alias {
		t.Errorf("HostKeyAlias changed with resource ID casing or ephemeral local port: got %q, want %q", got, alias)
	}
	differentVM := c
	differentVM.VMResourceID = strings.Replace(c.VMResourceID, "Jump-1", "Jump-2", 1)
	if got := sshOption(t, sshArguments(differentVM, "/tmp/session/ssh_config", stateRoot, 32022), "HostKeyAlias"); got == alias {
		t.Errorf("HostKeyAlias %q was reused for a different VM", got)
	}

	if got := argumentAfter(t, args, "-l"); got != c.SSHUser {
		t.Errorf("SSH user = %q, want %q", got, c.SSHUser)
	}
	if got := argumentAfter(t, args, "-i"); got != c.IdentityFile {
		t.Errorf("identity file = %q, want %q", got, c.IdentityFile)
	}
}

func TestRequireProxyRejectsMismatchedConfiguration(t *testing.T) {
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")

	proxyPort := availablePort(t)
	socksPort := availablePort(t)
	for socksPort == proxyPort {
		socksPort = availablePort(t)
	}
	c := profile.Profile{Registry: "exampleregistry", ProxyPort: proxyPort, SOCKSPort: socksPort}
	p, err := startProxy(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()

	if err := checkProxy(context.Background(), c, ""); err != nil {
		t.Fatalf("requireProxy() rejected matching configuration: %v", err)
	}
	for _, mismatch := range []profile.Profile{
		{Registry: "otherregistry", ProxyPort: proxyPort, SOCKSPort: socksPort},
		{Registry: c.Registry, ProxyPort: proxyPort, SOCKSPort: socksPort + 1},
	} {
		if err := checkProxy(context.Background(), mismatch, ""); err == nil || err.Error() != "local proxy does not match this configuration" {
			t.Errorf("requireProxy() mismatch error = %v, want local proxy configuration error", err)
		}
	}
}

func TestStartChildAndStop(t *testing.T) {
	cmd := helperCommand(t, "wait")
	p, err := startChild(cmd)
	if err != nil {
		t.Fatalf("startChild() error = %v", err)
	}

	select {
	case <-p.done:
		t.Fatal("helper exited before stop")
	case <-time.After(50 * time.Millisecond):
	}
	p.stop()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("child.stop() did not reap the helper")
	}
	if p.err == nil {
		t.Error("stopped child has nil wait error; want termination reported by os/exec")
	}
	// Stopping an already reaped child must be harmless and non-blocking.
	p.stop()
}

func TestStartChildReportsStartFailure(t *testing.T) {
	_, err := startChild(exec.CommandContext(context.Background(), filepath.Join(t.TempDir(), "does-not-exist")))
	if err == nil {
		t.Fatal("startChild() error = nil for nonexistent executable")
	}
}

func TestStartChildHonorsCommandContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := startChild(helperCommandContext(t, ctx, "wait"))
	if err != nil {
		t.Fatalf("startChild() error = %v", err)
	}
	cancel()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		p.stop()
		t.Fatal("command context cancellation did not terminate and reap the child")
	}
	if p.err == nil {
		t.Error("canceled child has nil wait error; want termination reported by os/exec")
	}
}

func TestWaitPort(t *testing.T) {
	t.Run("listener becomes ready", func(t *testing.T) {
		port := availablePort(t)
		p, err := startChild(helperCommand(t, "listen", profile.Loopback(port)))
		if err != nil {
			t.Fatalf("startChild() error = %v", err)
		}
		defer p.stop()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := waitPort(ctx, profile.Loopback(port), p); err != nil {
			t.Fatalf("waitPort() error = %v", err)
		}
	})

	t.Run("child exits first", func(t *testing.T) {
		p, err := startChild(helperCommand(t, "exit"))
		if err != nil {
			t.Fatalf("startChild() error = %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = waitPort(ctx, profile.Loopback(availablePort(t)), p)
		if err == nil || err.Error() != "connection process exited before its local port became ready" {
			t.Fatalf("waitPort() error = %v, want child-exited error", err)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		p, err := startChild(helperCommand(t, "wait"))
		if err != nil {
			t.Fatalf("startChild() error = %v", err)
		}
		defer p.stop()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err = waitPort(ctx, profile.Loopback(availablePort(t)), p)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitPort() error = %v, want context.Canceled", err)
		}
	})
}

// TestProcessHelper is re-executed by the lifecycle tests so they exercise a
// real os.Process without requiring Azure, SSH, Docker, or external services.
func TestProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_BIVROST_ACR_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(2)
	}
	switch os.Args[separator+1] {
	case "exit":
		return
	case "wait":
		for {
			time.Sleep(time.Hour)
		}
	case "listen":
		if separator+2 >= len(os.Args) {
			os.Exit(2)
		}
		listener, err := net.Listen("tcp4", os.Args[separator+2])
		if err != nil {
			os.Exit(3)
		}
		defer listener.Close()
		for {
			connection, err := listener.Accept()
			if err != nil {
				os.Exit(4)
			}
			_ = connection.Close()
		}
	default:
		os.Exit(2)
	}
}

func helperCommand(t *testing.T, mode string, args ...string) *exec.Cmd {
	return helperCommandContext(t, context.Background(), mode, args...)
}

func helperCommandContext(t *testing.T, ctx context.Context, mode string, args ...string) *exec.Cmd {
	t.Helper()
	commandArgs := []string{"-test.run=^TestProcessHelper$", "--", mode}
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], commandArgs...)
	cmd.Env = append(os.Environ(), "GO_WANT_BIVROST_ACR_HELPER=1")
	return cmd
}

func availablePort(t *testing.T) int {
	t.Helper()
	for {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		if port >= 1024 {
			return port
		}
	}
}

func waitForListener(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp4", address, 20*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("listener %s did not become ready", address)
}

func argumentAfter(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	t.Fatalf("argument %q not found in %q", flag, args)
	return ""
}

func sshOption(t *testing.T, args []string, name string) string {
	t.Helper()
	prefix := name + "="
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" && strings.HasPrefix(args[i+1], prefix) {
			return strings.TrimPrefix(args[i+1], prefix)
		}
	}
	t.Fatalf("SSH option %q not found in %q", name, args)
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestConnectValidatesRegistrySubscriptionBeforeSideEffects(t *testing.T) {
	c := profile.Profile{
		Registry:             "exampleregistry",
		ProxyPort:            18080,
		SOCKSPort:            18081,
		Subscription:         "bastion-subscription",
		BastionName:          "bastion",
		BastionResourceGroup: "network-rg",
		VMResourceID:         "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/jump",
	}
	t.Setenv("PATH", t.TempDir())
	if err := connect(context.Background(), c, false); err == nil || !strings.Contains(err.Error(), "registry_subscription") {
		t.Fatalf("expected configuration error before probing or launching tools, got %v", err)
	}
}

func TestRequireProxyRejectsInvalidURL(t *testing.T) {
	if err := checkProxy(context.Background(), profile.Profile{ProxyPort: -1}, ""); err == nil || !strings.Contains(err.Error(), "invalid local proxy URL") {
		t.Fatalf("invalid proxy URL should return a configuration error, got %v", err)
	}
}
