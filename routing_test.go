package main

import (
	"context"
	"net"
	"testing"
)

func TestPrivateHostsAreExactAndExcludePublicControlPlane(t *testing.T) {
	c := config{PrivateHosts: []string{"team-vault.vault.azure.net"}}
	if err := c.validatePrivateHosts(); err != nil {
		t.Fatal(err)
	}
	p := proxyRouter{registry: "widgets", privateHosts: c.PrivateHosts}
	for host, want := range map[string]bool{
		"team-vault.vault.azure.net":      true,
		"another.vault.azure.net":         false,
		"x.team-vault.vault.azure.net":    false,
		"team-vault.vault.azure.net.evil": false,
		"widgets.azurecr.io":              true,
	} {
		if got := p.isTunnelHost(host); got != want {
			t.Errorf("route %q = %v, want %v", host, got, want)
		}
	}
	for _, host := range []string{"*.vault.azure.net", "https://team-vault.vault.azure.net", "127.0.0.1", "foo.localhost", "vault.example:443", "management.azure.com", "login.microsoftonline.com", "HOST.example", "host.example."} {
		c.PrivateHosts = []string{host}
		if c.validatePrivateHosts() == nil {
			t.Errorf("accepted invalid private host %q", host)
		}
	}
}

func TestProxyIdentityIncludesPrivateRoutes(t *testing.T) {
	c := config{Registry: "widgets", SOCKSPort: 18081, PrivateHosts: []string{"one.example", "two.example"}}
	other := c
	other.PrivateHosts = []string{"two.example", "one.example"}
	if c.proxyID() != other.proxyID() {
		t.Fatal("route order should not change proxy identity")
	}
	other.PrivateHosts = []string{"three.example"}
	if c.proxyID() == other.proxyID() {
		t.Fatal("different routing profiles share proxy identity")
	}
}

func TestStartProxyFailsSynchronouslyOnOccupiedPort(t *testing.T) {
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c := config{Registry: "widgets", ProxyPort: l.Addr().(*net.TCPAddr).Port, SOCKSPort: 18081}
	if p, err := startProxy(context.Background(), c); err == nil {
		p.close()
		t.Fatal("proxy must not reuse an unrelated listener")
	}
}
