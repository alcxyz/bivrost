// bivrost provides local Azure login, management shells, and ACR access through Bastion.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alcxyz/bivrost/internal/azure"
)

type aksConfig struct {
	Name          string `json:"name"`
	ResourceGroup string `json:"resource_group"`
	Subscription  string `json:"subscription"`
}

type config struct {
	ACRProbeImage        string          `json:"acr_probe_image,omitempty"`
	SkipPullProbe        bool            `json:"-"`
	ACRSession           bool            `json:"-"`
	SkipRegistryLogin    bool            `json:"-"`
	RequiresPIM          bool            `json:"requires_pim,omitempty"`
	Prompt               *promptSettings `json:"-"`
	Environment          string          `json:"-"`
	AKS                  *aksConfig      `json:"aks,omitempty"`
	PrivateHosts         []string        `json:"private_hosts,omitempty"`
	Registry             string          `json:"registry"`
	RegistrySubscription string          `json:"registry_subscription"`
	Subscription         string          `json:"subscription"`
	BastionName          string          `json:"bastion_name"`
	BastionResourceGroup string          `json:"bastion_resource_group"`
	VMResourceID         string          `json:"vm_resource_id"`
	ProxyPort            int             `json:"proxy_port"`
	SOCKSPort            int             `json:"socks_port"`
	SSHUser              string          `json:"ssh_user,omitempty"`
	IdentityFile         string          `json:"identity_file,omitempty"`
}

var registryPattern = regexp.MustCompile(`^[a-z0-9]{5,50}$`)
var resourcePattern = regexp.MustCompile(`(?i)^/subscriptions/[a-z0-9-]+/resourceGroups/[a-z0-9_.()-]+/providers/Microsoft\.Compute/virtualMachines/[a-z0-9_.-]+$`)
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_.()-]+$`)

func main() {
	if strings.EqualFold(filepath.Base(os.Args[0]), "podman") || strings.EqualFold(filepath.Base(os.Args[0]), "podman.exe") {
		os.Exit(runPodmanWrapper())
	}
	log.SetFlags(0)
	if err := run(os.Args[1:]); err != nil {
		log.Print("bivrost: ", err)
		os.Exit(1)
	}
}

func loadConfig(path string) (config, error) {
	c := config{ProxyPort: 18080, SOCKSPort: 18081}
	f, err := os.Open(path)
	if err != nil {
		return c, fmt.Errorf("open configuration: %w (copy config.example.json and fill in connection details)", err)
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 64*1024))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, errors.New("configuration must be a JSON object using the fields in config.example.json")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, errors.New("unexpected data after configuration")
	}
	return c, nil
}

var probeRepositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*@sha256:[a-f0-9]{64}$`)

func (c config) validateACR() error {
	if c.Registry == "" {
		return errors.New("set registry explicitly in the configuration to enable ACR access")
	}
	return c.validateProxy()
}

func (c config) validateProxy() error {
	if c.Registry != "" && !registryPattern.MatchString(c.Registry) {
		return errors.New("registry must be a lowercase ACR name, without .azurecr.io")
	}
	if c.ACRProbeImage != "" {
		prefix := c.Registry + ".azurecr.io/"
		if c.Registry == "" || !strings.HasPrefix(c.ACRProbeImage, prefix) || !probeRepositoryPattern.MatchString(strings.TrimPrefix(c.ACRProbeImage, prefix)) {
			return errors.New("acr_probe_image must be a digest-pinned image in this environment's ACR registry")
		}
	}
	if c.ProxyPort < 1024 || c.ProxyPort > 65535 || c.SOCKSPort < 1024 || c.SOCKSPort > 65535 || c.ProxyPort == c.SOCKSPort {
		return errors.New("proxy_port and socks_port must be different ports between 1024 and 65535")
	}
	return nil
}

func (c config) validatePlatform() error {
	if err := c.validateConnection(); err != nil {
		return err
	}
	if err := c.validateProxy(); err != nil {
		return err
	}
	if c.AKS != nil && (!namePattern.MatchString(c.AKS.Name) || !namePattern.MatchString(c.AKS.ResourceGroup) || !namePattern.MatchString(c.AKS.Subscription)) {
		return errors.New("aks must specify name, resource_group, and subscription")
	}
	return c.validatePrivateHosts()
}

func (c config) validatePrivateHosts() error {
	for _, host := range c.PrivateHosts {
		if host != strings.ToLower(host) || strings.HasSuffix(host, ".") || !strings.Contains(host, ".") || !validDNSName(host) || forbiddenTargetName(host) {
			return errors.New("private_hosts must contain exact lowercase DNS names without URLs, wildcards, IP addresses, or ports")
		}
		if host == "management.azure.com" || host == "login.microsoftonline.com" {
			return errors.New("private_hosts must not contain public Azure management or login endpoints")
		}
	}
	return nil
}

func (c config) validateConnection() error {
	if !namePattern.MatchString(c.Subscription) || !namePattern.MatchString(c.BastionName) || !namePattern.MatchString(c.BastionResourceGroup) || !resourcePattern.MatchString(c.VMResourceID) {
		return errors.New("fill in subscription, bastion_name, bastion_resource_group and a VM resource ID in the configuration")
	}
	if (c.SSHUser == "") != (c.IdentityFile == "") {
		return errors.New("ssh_user and identity_file must both be set for SSH key authentication; omit both for Entra authentication")
	}
	if strings.ContainsAny(c.SSHUser, "\r\n\t ") || strings.HasPrefix(c.SSHUser, "-") {
		return errors.New("invalid ssh_user")
	}
	if c.IdentityFile != "" && !filepath.IsAbs(c.IdentityFile) {
		return errors.New("identity_file must be an absolute path")
	}
	return nil
}

func loopback(port int) string    { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }
func (c config) proxyURL() string { return "http://" + loopback(c.ProxyPort) }

// This value contains no credentials and lets connect reject a mismatched proxy.
func (c config) proxyID() string {
	hosts := append([]string(nil), c.PrivateHosts...)
	sort.Strings(hosts)
	sum := sha256.Sum256([]byte(c.Registry + ":" + strconv.Itoa(c.SOCKSPort) + ":" + strings.Join(hosts, ",")))
	return hex.EncodeToString(sum[:])
}

func upstreamProxy() (*url.URL, error) {
	raw := os.Getenv("BIVROST_UPSTREAM_PROXY")
	if raw == "" {
		raw = os.Getenv("BIVROST_ACR_UPSTREAM_PROXY")
	}
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("BIVROST_UPSTREAM_PROXY must be an http://host:port URL; its value will not be logged")
	}
	if u.Port() != "" {
		p, e := strconv.Atoi(u.Port())
		if e != nil || p < 1 || p > 65535 {
			return nil, errors.New("invalid upstream proxy port")
		}
	}
	return u, nil
}

type runningProxy struct {
	server   *http.Server
	router   *proxyRouter
	done     <-chan error
	bridgeMu sync.RWMutex
	bridge   *podmanProxyBridge
}

// Publish the capability only after SSH confirms the VM forward is allocated.
func (p *runningProxy) setPodmanBridge(bridge *podmanProxyBridge) {
	p.bridgeMu.Lock()
	defer p.bridgeMu.Unlock()
	p.bridge = bridge
}

func (p *runningProxy) podmanBridgeID() string {
	p.bridgeMu.RLock()
	defer p.bridgeMu.RUnlock()
	if p.bridge == nil {
		return ""
	}
	select {
	case <-p.bridge.done:
		return ""
	default:
		return p.bridge.machine.proxyBridgeID()
	}
}

func (p *runningProxy) close() {
	_ = p.server.Close()
	p.router.Close()
}

func startProxy(ctx context.Context, c config) (_ *runningProxy, resultErr error) {
	finish := diagnosticStep(ctx, eventProxy)
	defer func() { finish(resultErr) }()
	if err := c.validateProxy(); err != nil {
		return nil, err
	}
	if err := c.validatePrivateHosts(); err != nil {
		return nil, err
	}
	upstream, err := upstreamProxy()
	if err != nil {
		return nil, err
	}
	if upstream != nil && (upstream.Hostname() == "localhost" || upstream.Hostname() == "127.0.0.1" || upstream.Hostname() == "::1") && upstream.Port() == strconv.Itoa(c.ProxyPort) {
		return nil, errors.New("upstream proxy cannot point back at bivrost")
	}
	listener, err := net.Listen("tcp4", loopback(c.ProxyPort))
	if err != nil {
		return nil, errors.New("proxy port is occupied; stop the existing listener or choose a different proxy_port")
	}
	router := &proxyRouter{registry: c.Registry, privateHosts: append([]string(nil), c.PrivateHosts...), socksAddress: loopback(c.SOCKSPort), directProxy: upstream}
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024, ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return ctx }}
	p := &runningProxy{server: server, router: router}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/bivrost/health" && !r.URL.IsAbs() {
			w.Header().Set("Content-Type", "text/plain")
			if ctx.Err() == nil {
				if id := p.podmanBridgeID(); id != "" {
					w.Header().Set("Bivrost-Podman-Bridge", id)
				}
			}
			fmt.Fprint(w, c.proxyID())
			return
		}
		router.ServeHTTP(w, r)
	})
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	p.done = done
	return p, nil
}

func serveProxy(ctx context.Context, c config) error {
	p, err := startProxy(ctx, c)
	if err != nil {
		return err
	}
	defer p.close()
	var bridgeDone <-chan struct{}
	bridge, err := startPodmanProxyBridge(ctx, c)
	if err != nil {
		return err
	}
	if bridge != nil {
		defer bridge.stop()
		p.setPodmanBridge(bridge)
		bridgeDone = bridge.done
		fmt.Println("Podman Machine proxy forward allocated on guest loopback.")
	}
	fmt.Println("Advanced proxy ready at " + c.proxyURL() + ". Private ACR traffic requires a separately supplied SOCKS tunnel.")
	fmt.Println("For a managed local shell, stop this proxy and use bivrost acr connect instead.")
	fmt.Println("Keep this proxy running while your container engine is configured to use it.")
	select {
	case <-bridgeDone:
		return errors.New("Podman Machine proxy bridge disconnected; restart bivrost acr proxy after checking the machine")
	case <-ctx.Done():
		return nil
	case err := <-p.done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func requireProxy(ctx context.Context, c config) error {
	machine, err := inspectPodman(ctx)
	if err != nil {
		return err
	}
	bridgeID := ""
	if machine != nil {
		bridgeID = machine.proxyBridgeID()
	}
	return checkProxy(ctx, c, bridgeID)
}

func checkProxy(ctx context.Context, c config, bridgeID string) error {
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.proxyURL()+"/bivrost/health", nil)
	if err != nil {
		return errors.New("invalid local proxy URL; check proxy_port in the configuration")
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("start bivrost acr proxy with the same configuration in another terminal first")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != c.proxyID() {
		return errors.New("local proxy does not match this configuration")
	}
	if bridgeID != "" && resp.Header.Get("Bivrost-Podman-Bridge") != bridgeID {
		return errors.New("local proxy has no ready forward for the selected Podman Machine; restart bivrost acr proxy with the same environment and Podman connection")
	}
	return nil
}

func registryCheck(ctx context.Context, c config) error {
	u, _ := url.Parse(c.proxyURL())
	tr := &http.Transport{Proxy: http.ProxyURL(u), TLSHandshakeTimeout: 15 * time.Second}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+c.Registry+".azurecr.io/v2/", nil)
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("ACR TLS/connectivity check failed; check the Bastion connection, private DNS, and VM forwarding access")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("ACR returned HTTP %d; check registry network access", resp.StatusCode)
	}
	return nil
}

func doctor(ctx context.Context, c config) (resultErr error) {
	finish := diagnosticStep(ctx, eventDoctor)
	defer func() { finish(resultErr) }()
	for _, tool := range []string{"az", "ssh", "podman"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is missing from PATH", tool)
		}
		fmt.Println(tool + ": available")
	}
	if err := c.validateConnection(); err != nil {
		return err
	}
	if !namePattern.MatchString(c.RegistrySubscription) {
		return errors.New("set registry_subscription to the subscription containing ACR")
	}
	if err := requirePodmanForACR(ctx, c); err != nil {
		return err
	}
	fmt.Println("Local Podman: reachable")
	if err := requireProxy(ctx, c); err != nil {
		return err
	}
	fmt.Println("Local proxy: ready")
	if err := registryCheck(ctx, c); err != nil {
		return err
	}
	fmt.Println("ACR: TLS and /v2/ reachable through the proxy (push permissions and layer endpoints still require a real transfer)")
	return nil
}

func acrLogin(ctx context.Context, c config) (resultErr error) {
	finish := diagnosticStep(ctx, eventACRLogin)
	defer func() { finish(resultErr) }()
	if !namePattern.MatchString(c.RegistrySubscription) {
		return errors.New("set registry_subscription to the subscription containing ACR")
	}
	if err := requirePodmanForACR(ctx, c); err != nil {
		return err
	}
	if err := requireProxy(ctx, c); err != nil {
		return err
	}
	if err := registryCheck(ctx, c); err != nil {
		return err
	}
	childCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	podman, err := registryLoginCommand(childCtx, c)
	if err != nil {
		return err
	}
	podman.Env = proxyEnvironment(os.Environ(), c.proxyURL())
	return loginRegistryWithCommand(childCtx, c, podman)
}

func loginPodmanSession(ctx context.Context, c config, session *podmanSession) (resultErr error) {
	finish := diagnosticStep(ctx, eventACRLogin)
	defer func() { finish(resultErr) }()
	childCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	podman, err := session.loginCommand(childCtx, c)
	if err != nil {
		return err
	}
	return loginRegistryWithCommand(childCtx, c, podman)
}

func loginRegistryWithCommand(childCtx context.Context, c config, podman *exec.Cmd) error {
	cmd, err := azure.Command(childCtx, "acr", "login", "--name", c.Registry, "--subscription", c.RegistrySubscription, "--expose-token", "--output", "json", "--only-show-errors")
	if err != nil {
		return err
	}
	// Azure's data-plane requests also need the tunnel. Only this child gets these
	// values; the user's shell, global Azure subscription, and container connection stay intact.
	cmd.Env = proxyEnvironment(os.Environ(), c.proxyURL())
	output, err := cmd.Output() // stdout contains a credential: never attach it to an error or log.
	if err != nil {
		return errors.New("ACR token acquisition failed; run bivrost login (or az login) and verify the registry subscription and your ACR role")
	}
	var token struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(output, &token) != nil || token.AccessToken == "" {
		return errors.New("Azure CLI did not return an ACR token")
	}
	podman.Stdin = strings.NewReader(token.AccessToken + "\n")
	// Suppress subprocess output, including credential-helper errors which may
	// embed stdin. Podman retains the login using its usual credential settings.
	if err := podman.Run(); err != nil {
		return errors.New("Podman registry login failed; run bivrost doctor for this environment and verify proxy and credential storage settings")
	}
	fmt.Println("Local Podman registry login refreshed.")
	return nil
}

func proxyEnvironment(env []string, proxy string) []string {
	result := make([]string, 0, len(env)+3)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
			continue
		}
		result = append(result, entry)
	}
	return append(result, "HTTPS_PROXY="+proxy, "HTTP_PROXY="+proxy, "NO_PROXY=127.0.0.1,localhost")
}

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func startChild(cmd *exec.Cmd) (*child, error) {
	prepareProcess(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &child{cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}
func (p *child) stop() {
	select {
	case <-p.done:
		return
	default:
	}
	killProcess(p.cmd)
	<-p.done
}

func freePort() (int, error) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
func waitPort(ctx context.Context, address string, p *child) error {
	timeout := time.NewTimer(90 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return errors.New("connection process exited before its local port became ready")
		case <-timeout.C:
			return errors.New("timed out waiting for the connection; check Azure login, VM access and SSH authentication")
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
			if err == nil {
				conn.Close()
				return nil
			}
		}
	}
}

func connect(ctx context.Context, c config, noLogin bool) (resultErr error) {
	finish := diagnosticStep(ctx, eventACRConnect)
	defer func() { finish(resultErr) }()
	if err := c.validateACR(); err != nil {
		return err
	}
	if !noLogin && !namePattern.MatchString(c.RegistrySubscription) {
		return errors.New("set registry_subscription to the subscription containing ACR")
	}
	if err := c.validatePlatform(); err != nil {
		return err
	}
	if err := requirePodmanForACR(ctx, c); err != nil {
		return err
	}
	c.ACRSession = true
	c.SkipRegistryLogin = noLogin
	return platformConnect(ctx, c)
}

func baseSSHArguments(c config, sshConfig, stateRoot string, port int) []string {
	// A stable alias separates VM identities from ephemeral localhost ports.
	sum := sha256.Sum256([]byte(strings.ToLower(c.VMResourceID)))
	args := []string{"-F", sshConfig, "-p", strconv.Itoa(port),
		"-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=15", "-o", "BatchMode=yes", "-o", "ForwardAgent=no",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "HostKeyAlias=bivrost-" + hex.EncodeToString(sum[:16]),
		"-o", "UserKnownHostsFile=\"" + strings.ReplaceAll(filepath.ToSlash(filepath.Join(stateRoot, "known_hosts")), "\"", "\\\"") + "\""}
	if c.SSHUser != "" {
		args = append(args, "-l", c.SSHUser, "-i", c.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	return args
}

func sshArguments(c config, sshConfig, stateRoot string, port int) []string {
	return append(baseSSHArguments(c, sshConfig, stateRoot, port), "-N", "-T", "-D", loopback(c.SOCKSPort), "127.0.0.1")
}
func interactiveSSHArguments(c config, sshConfig, stateRoot string, port int) []string {
	return append(baseSSHArguments(c, sshConfig, stateRoot, port), "-tt", "127.0.0.1")
}
