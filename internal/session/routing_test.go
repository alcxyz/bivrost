package session

import (
	"context"
	"net"
	"testing"

	profile "github.com/alcxyz/bivrost/internal/config"
)

func TestPrivateHostsValidationExcludesPublicControlPlane(t *testing.T) {
	c := profile.Profile{PrivateHosts: []string{"team-vault.vault.azure.net"}}
	if err := c.ValidatePrivateHosts(); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"*.vault.azure.net", "https://team-vault.vault.azure.net", "127.0.0.1", "foo.localhost", "vault.example:443", "management.azure.com", "login.microsoftonline.com", "HOST.example", "host.example."} {
		c.PrivateHosts = []string{host}
		if c.ValidatePrivateHosts() == nil {
			t.Errorf("accepted invalid private host %q", host)
		}
	}
}

func TestProxyIdentityIncludesPrivateRoutes(t *testing.T) {
	c := profile.Profile{Registry: "widgets", SOCKSPort: 18081, PrivateHosts: []string{"one.example", "two.example"}}
	other := c
	other.PrivateHosts = []string{"two.example", "one.example"}
	if c.ProxyID() != other.ProxyID() {
		t.Fatal("route order should not change proxy identity")
	}
	other.PrivateHosts = []string{"three.example"}
	if c.ProxyID() == other.ProxyID() {
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
	c := profile.Profile{Registry: "widgets", ProxyPort: l.Addr().(*net.TCPAddr).Port, SOCKSPort: 18081}
	if p, err := startProxy(context.Background(), c); err == nil {
		p.close()
		t.Fatal("proxy must not reuse an unrelated listener")
	}
}
