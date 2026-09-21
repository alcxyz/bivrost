package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/alcxyz/bivrost/internal/azure"
	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
)

func registryCheck(ctx context.Context, c profile.Profile) error {
	u, _ := url.Parse(c.ProxyURL())
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

func doctor(ctx context.Context, c profile.Profile) (resultErr error) {
	finish := diagnostics.Step(ctx, diagnostics.EventDoctor)
	defer func() { finish(resultErr) }()
	for _, tool := range []string{"az", "ssh", "podman"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is missing from PATH", tool)
		}
		fmt.Println(tool + ": available")
	}
	if err := c.ValidateConnection(); err != nil {
		return err
	}
	if !profile.ValidResourceName(c.RegistrySubscription) {
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

func acrLogin(ctx context.Context, c profile.Profile) (resultErr error) {
	finish := diagnostics.Step(ctx, diagnostics.EventACRLogin)
	defer func() { finish(resultErr) }()
	if !profile.ValidResourceName(c.RegistrySubscription) {
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
	podman.Env = proxyEnvironment(os.Environ(), c.ProxyURL())
	return loginRegistryWithCommand(childCtx, c, podman)
}

func loginPodmanSession(ctx context.Context, c profile.Profile, session *podmanSession) (resultErr error) {
	finish := diagnostics.Step(ctx, diagnostics.EventACRLogin)
	defer func() { finish(resultErr) }()
	childCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	podman, err := session.loginCommand(childCtx, c)
	if err != nil {
		return err
	}
	return loginRegistryWithCommand(childCtx, c, podman)
}

func loginRegistryWithCommand(childCtx context.Context, c profile.Profile, podman *exec.Cmd) error {
	cmd, err := azure.Command(childCtx, "acr", "login", "--name", c.Registry, "--subscription", c.RegistrySubscription, "--expose-token", "--output", "json", "--only-show-errors")
	if err != nil {
		return err
	}
	// Azure's data-plane requests also need the tunnel. Only this child gets these
	// values; the user's shell, global Azure subscription, and container connection stay intact.
	cmd.Env = proxyEnvironment(os.Environ(), c.ProxyURL())
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
