package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	profile "github.com/alcxyz/bivrost/internal/config"
)

func TestCustomPlatformProfileDoesNotDefaultRegistry(t *testing.T) {
	for _, registry := range []string{"omitted", "empty", "null"} {
		t.Run(registry, func(t *testing.T) {
			c := platformTestConfig(t)
			payload, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, "registry")
			delete(fields, "registry_subscription")
			switch registry {
			case "empty":
				fields["registry"] = ""
			case "null":
				fields["registry"] = nil
			}
			payload, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "platform.json")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			c, err = profile.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if c.Registry != "" {
				t.Fatalf("registry unexpectedly defaulted to %q", c.Registry)
			}
			if err := c.ValidatePlatform(); err != nil {
				t.Fatalf("platform-only profile rejected: %v", err)
			}
			if err := c.ValidateACR(); err == nil || !strings.Contains(err.Error(), "set registry explicitly") {
				t.Fatalf("ACR validation = %v", err)
			}
			for _, args := range [][]string{
				{"acr", "proxy"}, {"acr", "connect"}, {"acr", "login"}, {"acr", "doctor"}, {"connect", "--acr"}, {"connect", "--acr", "--no-login"},
			} {
				err := Run(append(args, "--config", path), "bivrost dev")
				if err == nil || !strings.Contains(err.Error(), "set registry explicitly") {
					t.Errorf("run(%q) = %v; want explicit registry error before external operations", args, err)
				}
			}
		})
	}
}

func TestPlatformProxyWithoutRegistry(t *testing.T) {
	// Real startup catches validation in the shared proxy path as well as the
	// platform validator. No connection to the external network is needed.
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	c := platformTestConfig(t)
	c.Registry = ""
	proxy, err := startProxy(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()

	c.ProxyPort = c.SOCKSPort
	if err := c.ValidatePlatform(); err == nil {
		t.Fatal("platform-only profile accepted conflicting proxy ports")
	}
	c.ProxyPort = 0
	if err := c.ValidatePlatform(); err == nil {
		t.Fatal("platform-only profile accepted an invalid proxy port")
	}
}

func TestPlatformProbeRequiresRegistry(t *testing.T) {
	c := platformTestConfig(t)
	c.Registry = ""
	c.ACRProbeImage = "exampleregistry.azurecr.io/alpine@sha256:" + strings.Repeat("a", 64)
	if err := c.ValidatePlatform(); err == nil {
		t.Fatal("probe image accepted without explicit registry")
	}
}
