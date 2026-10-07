package session

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
)

func TestPrivateHostsValidationExcludesPublicControlPlane(t *testing.T) {
	// Only hosts at or below a blocked name are rejected, not lookalikes.
	c := profile.Profile{PrivateHosts: []string{"team-vault.vault.azure.net", "management.azure.com.example", "notgraph.microsoft.com"}}
	if err := c.ValidatePrivateHosts(); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"*.vault.azure.net", "https://team-vault.vault.azure.net", "127.0.0.1", "foo.localhost", "vault.example:443", "management.azure.com", "login.microsoftonline.com", "HOST.example", "host.example."} {
		c.PrivateHosts = []string{host}
		if c.ValidatePrivateHosts() == nil {
			t.Errorf("accepted invalid private host %q", host)
		}
	}
	for _, host := range []string{
		"login.microsoft.com", "login.windows.net", "login.microsoftonline.us",
		"login.chinacloudapi.cn", "login.partner.microsoftonline.cn",
		"management.core.windows.net", "management.usgovcloudapi.net", "management.chinacloudapi.cn",
		"graph.microsoft.com", "graph.microsoft.us", "dod-graph.microsoft.us",
		"microsoftgraph.chinacloudapi.cn", "graph.windows.net",
		"westus.management.azure.com", "canary.graph.microsoft.com", "a.b.login.microsoftonline.com",
	} {
		c.PrivateHosts = []string{host}
		if c.ValidatePrivateHosts() == nil {
			t.Errorf("accepted Microsoft sign-in, management or Graph endpoint %q", host)
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

func TestWithPrivateHostsAddsValidatedUniqueSessionRoutes(t *testing.T) {
	configured := profile.Profile{PrivateHosts: []string{"configured.example", "configured.example"}}
	unchanged, err := withPrivateHosts(configured, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged.PrivateHosts) != 2 {
		t.Fatalf("no-flag profile was normalized: %v", unchanged.PrivateHosts)
	}
	effective, err := withPrivateHosts(configured, []string{"added.example", "configured.example"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(effective.PrivateHosts); got != 3 || effective.PrivateHosts[0] != "configured.example" || effective.PrivateHosts[1] != "configured.example" || effective.PrivateHosts[2] != "added.example" {
		t.Fatalf("effective private hosts = %v", effective.PrivateHosts)
	}
	if len(configured.PrivateHosts) != 2 || configured.PrivateHosts[0] != "configured.example" || configured.PrivateHosts[1] != "configured.example" {
		t.Fatalf("configured profile was changed: %v", configured.PrivateHosts)
	}
	if _, err := withPrivateHosts(configured, []string{"127.0.0.1"}); err == nil {
		t.Fatal("withPrivateHosts accepted an IP address")
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

// fakeSOCKS completes a SOCKS5 CONNECT, reports the requested host and then
// echoes the tunnel's data.
func fakeSOCKS(t *testing.T) (int, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	hosts := make(chan string, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				header := make([]byte, 5)
				if _, err := io.ReadFull(conn, header[:3]); err != nil {
					return
				}
				conn.Write([]byte{0x05, 0x00})
				if _, err := io.ReadFull(conn, header); err != nil {
					return
				}
				request := make([]byte, int(header[4])+2)
				if _, err := io.ReadFull(conn, request); err != nil {
					return
				}
				hosts <- string(request[:len(request)-2])
				conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
				io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, hosts
}

// connectThrough opens a CONNECT tunnel through the session proxy.
func connectThrough(t *testing.T, proxyAddress, target string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	conn, err := net.Dial("tcp4", proxyAddress)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	return conn, reader, response.StatusCode
}

func TestRunningProxyReplacesRoutesInPlace(t *testing.T) {
	// Direct targets go to a local upstream proxy that refuses every request.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "direct route", http.StatusForbidden)
	}))
	defer upstream.Close()
	t.Setenv("BIVROST_UPSTREAM_PROXY", upstream.URL)
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	socksPort, tunnelled := fakeSOCKS(t)
	c := profile.Profile{Registry: "widgets", ProxyPort: availablePort(t), SOCKSPort: socksPort, PrivateHosts: []string{"local.private.example"}}
	p, err := startProxy(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	address := profile.Loopback(c.ProxyPort)

	// Carry a connection through the bootstrap routes.
	established, reader, status := connectThrough(t, address, "local.private.example:443")
	defer established.Close()
	if status != http.StatusOK || <-tunnelled != "local.private.example" {
		t.Fatalf("bootstrap tunnel status = %d", status)
	}
	if _, err := established.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(reader, echo); err != nil || string(echo) != "ping" {
		t.Fatal("bootstrap tunnel did not relay data")
	}
	direct, _, status := connectThrough(t, address, "remote.private.example:443")
	direct.Close()
	if status != http.StatusBadGateway {
		t.Fatalf("unrouted host status = %d, want direct route failure", status)
	}

	updated, err := withPrivateHosts(c, []string{"remote.private.example"})
	if err != nil {
		t.Fatal(err)
	}
	// Health checks race with the replacement; run with -race.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			_ = checkProxy(context.Background(), c, "")
		}
	}()
	if err := p.replaceRoutes(updated); err != nil {
		t.Fatal(err)
	}
	<-done

	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("tunnel opened through the bootstrap routes stayed open")
	}
	if err := checkProxy(context.Background(), updated, ""); err != nil {
		t.Fatalf("health does not report the replacement routes: %v", err)
	}
	if err := checkProxy(context.Background(), c, ""); err == nil {
		t.Fatal("health still reports the bootstrap routes")
	}
	conn, _, status := connectThrough(t, address, "remote.private.example:443")
	defer conn.Close()
	if status != http.StatusOK || <-tunnelled != "remote.private.example" {
		t.Fatalf("replacement route status = %d, want SOCKS tunnel on the same port", status)
	}

	moved := updated
	moved.SOCKSPort = availablePort(t)
	if err := p.replaceRoutes(moved); err == nil {
		t.Fatal("route replacement changed the SOCKS port")
	}
	invalid := updated
	invalid.PrivateHosts = []string{"management.azure.com"}
	if err := p.replaceRoutes(invalid); err == nil {
		t.Fatal("route replacement accepted an invalid route")
	}
}
