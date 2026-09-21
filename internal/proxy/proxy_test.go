package proxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRegistryHostMatchingIsStrict(t *testing.T) {
	t.Parallel()
	router := NewRouter(Config{Registry: "widgets"})

	tests := map[string]bool{
		"widgets.azurecr.io":                       true,
		"widgets.northeurope.data.azurecr.io":      true,
		"widgets.north-europe.data.azurecr.io":     true,
		"evilwidgets.azurecr.io":                   false,
		"widgets.azurecr.io.example.com":           false,
		"widgets.a.b.data.azurecr.io":              false,
		"other.northeurope.data.azurecr.io":        false,
		"widgets.northeurope.data.azurecr.io.evil": false,
	}
	for host, want := range tests {
		host, want := host, want
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			if got := router.isRegistryHost(host); got != want {
				t.Fatalf("isRegistryHost(%q) = %v, want %v", host, got, want)
			}
		})
	}
}

func TestNoRegistryDoesNotRouteACRThroughSOCKS(t *testing.T) {
	t.Parallel()
	router := NewRouter(Config{})
	if router.isRegistryHost("exampleregistry.azurecr.io") {
		t.Fatal("router without a registry matched an ACR host")
	}
}

func TestPrivateHostsAreExactAndExcludePublicControlPlane(t *testing.T) {
	t.Parallel()
	router := NewRouter(Config{Registry: "widgets", PrivateHosts: []string{"team-vault.vault.azure.net"}})
	for host, want := range map[string]bool{
		"team-vault.vault.azure.net":      true,
		"another.vault.azure.net":         false,
		"x.team-vault.vault.azure.net":    false,
		"team-vault.vault.azure.net.evil": false,
		"widgets.azurecr.io":              true,
	} {
		if got := router.isTunnelHost(host); got != want {
			t.Errorf("route %q = %v, want %v", host, got, want)
		}
	}
}

func TestValidRemoteDNSName(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]bool{
		"team-vault.vault.azure.net":         true,
		"*.vault.azure.net":                  false,
		"https://team-vault.vault.azure.net": false,
		"127.0.0.1":                          false,
		"foo.localhost":                      false,
		"vault.example:443":                  false,
		"HOST.example":                       false,
		"host.example.":                      false,
		"singlelabel":                        false,
	} {
		if got := ValidRemoteDNSName(host); got != want {
			t.Errorf("ValidRemoteDNSName(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestRegistryCONNECTUsesSOCKSRemoteDNSAndPreservesBufferedBytes(t *testing.T) {
	testSOCKSRoute(t, "widgets.azurecr.io", nil)
}

func TestPrivateEndpointCONNECTUsesRemoteDNS(t *testing.T) {
	testSOCKSRoute(t, "team-vault.vault.azure.net", []string{"team-vault.vault.azure.net"})
}

func testSOCKSRoute(t *testing.T, host string, privateHosts []string) {
	t.Helper()
	socksListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socksListener.Close()

	type socksTarget struct {
		host string
		port uint16
	}
	targets := make(chan socksTarget, 1)
	socksDone := make(chan error, 1)
	go func() {
		connection, err := socksListener.Accept()
		if err != nil {
			socksDone <- err
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)

		greeting := make([]byte, 3)
		if _, err := io.ReadFull(reader, greeting); err != nil {
			socksDone <- err
			return
		}
		if string(greeting) != string([]byte{0x05, 0x01, 0x00}) {
			socksDone <- &testError{"unexpected SOCKS greeting"}
			return
		}
		if _, err := connection.Write([]byte{0x05, 0x00}); err != nil {
			socksDone <- err
			return
		}

		header := make([]byte, 5)
		if _, err := io.ReadFull(reader, header); err != nil {
			socksDone <- err
			return
		}
		if string(header[:4]) != string([]byte{0x05, 0x01, 0x00, 0x03}) {
			socksDone <- &testError{"SOCKS request did not use a domain name"}
			return
		}
		hostBytes := make([]byte, int(header[4]))
		portBytes := make([]byte, 2)
		if _, err := io.ReadFull(reader, hostBytes); err != nil {
			socksDone <- err
			return
		}
		if _, err := io.ReadFull(reader, portBytes); err != nil {
			socksDone <- err
			return
		}
		targets <- socksTarget{host: string(hostBytes), port: binary.BigEndian.Uint16(portBytes)}
		if _, err := connection.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0}); err != nil {
			socksDone <- err
			return
		}

		_, err = io.Copy(connection, reader)
		socksDone <- err
	}()

	router := NewRouter(Config{Registry: "widgets", PrivateHosts: privateHosts, SOCKSAddress: socksListener.Addr().String()})
	server := httptest.NewServer(router)
	defer server.Close()
	defer router.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("tcp", serverURL.Host)
	if err != nil {
		t.Fatal(err)
	}

	earlyBytes := []byte{0x16, 0x03, 0x01, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
	request := "CONNECT " + host + ":443 HTTP/1.1\r\nHost: " + host + ":443\r\n\r\n"
	if _, err := client.Write(append([]byte(request), earlyBytes...)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, " 200 ") {
		t.Fatalf("unexpected CONNECT response: %q", status)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}

	echoed := make([]byte, len(earlyBytes))
	if _, err := io.ReadFull(reader, echoed); err != nil {
		t.Fatal(err)
	}
	if string(echoed) != string(earlyBytes) {
		t.Fatalf("echoed buffered bytes = %x, want %x", echoed, earlyBytes)
	}
	if target := <-targets; target.host != host || target.port != 443 {
		t.Fatalf("SOCKS target = %s:%d", target.host, target.port)
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-socksDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SOCKS connection did not close")
	}
}

func TestRegistrySOCKSFailureDoesNotUseDirectProxy(t *testing.T) {
	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddress := closedListener.Addr().String()
	closedListener.Close()

	directListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer directListener.Close()
	directURL, err := url.Parse("http://" + directListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Config{Registry: "widgets", PrivateHosts: []string{"team-vault.vault.azure.net"}, SOCKSAddress: closedAddress, DirectProxy: directURL})

	for _, host := range []string{"widgets.azurecr.io", "team-vault.vault.azure.net"} {
		request := httptest.NewRequest(http.MethodConnect, "/", nil)
		request.Host = host + ":443"
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
		}

	}
	if err := directListener.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if connection, err := directListener.Accept(); err == nil {
		connection.Close()
		t.Fatal("direct proxy was contacted after the ACR SOCKS route failed")
	}
}

func TestRejectsPlainHTTPAndNonHTTPSCONNECT(t *testing.T) {
	t.Parallel()
	router := NewRouter(Config{})

	plain := httptest.NewRecorder()
	router.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "http://example.com/v2/", nil))
	if plain.Code != http.StatusMethodNotAllowed || plain.Header().Get("Allow") != http.MethodConnect {
		t.Fatalf("plain HTTP response = %d Allow=%q", plain.Code, plain.Header().Get("Allow"))
	}
	if !strings.Contains(plain.Body.String(), "HTTPS CONNECT") {
		t.Fatalf("plain HTTP error is unclear: %q", plain.Body.String())
	}

	nonHTTPS := httptest.NewRecorder()
	nonHTTPSRequest := httptest.NewRequest(http.MethodConnect, "/", nil)
	nonHTTPSRequest.Host = "example.com:80"
	router.ServeHTTP(nonHTTPS, nonHTTPSRequest)
	if nonHTTPS.Code != http.StatusForbidden {
		t.Fatalf("non-443 CONNECT status = %d, want %d", nonHTTPS.Code, http.StatusForbidden)
	}
}

func TestRejectsLiteralAndLocalTargets(t *testing.T) {
	t.Parallel()
	router := NewRouter(Config{})
	for _, target := range []string{"127.0.0.1:443", "[::1]:443", "localhost:443", "service.localhost:443"} {
		target := target
		t.Run(target, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodConnect, "http://example.invalid", nil)
			request.Host = target
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
			}
		})
	}
}

func TestCloseTerminatesTrackedConnections(t *testing.T) {
	t.Parallel()
	router := NewRouter(Config{})
	tracked, peer := net.Pipe()
	defer peer.Close()
	if !router.track(tracked) {
		t.Fatal("track unexpectedly refused a connection")
	}

	router.Close()
	buffer := make([]byte, 1)
	if _, err := peer.Read(buffer); err == nil {
		t.Fatal("tracked connection remained open after Close")
	}

	second, secondPeer := net.Pipe()
	defer second.Close()
	defer secondPeer.Close()
	if router.track(second) {
		t.Fatal("track accepted a connection after Close")
	}
}

func TestTunnelOneWayActivityKeepsBothDirectionsAlive(t *testing.T) {
	t.Parallel()
	clientProxy, clientPeer := net.Pipe()
	upstreamProxy, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer upstreamPeer.Close()
	for _, connection := range []net.Conn{clientPeer, upstreamPeer} {
		if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	idleTimeout := 80 * time.Millisecond
	done := make(chan struct{})
	go func() {
		tunnelWithIdleTimeout(clientProxy, upstreamProxy, idleTimeout)
		close(done)
	}()

	// Keep an upload active for several idle windows. The blocked reverse read
	// must receive each shared deadline refresh as bytes move in this direction.
	for i := byte(0); i < 6; i++ {
		if _, err := clientPeer.Write([]byte{i}); err != nil {
			t.Fatalf("upload byte %d: %v", i, err)
		}
		got := []byte{0}
		if _, err := io.ReadFull(upstreamPeer, got); err != nil {
			t.Fatalf("read upload byte %d: %v", i, err)
		}
		if got[0] != i {
			t.Fatalf("upload byte = %d, want %d", got[0], i)
		}
		time.Sleep(idleTimeout / 2)
	}

	if _, err := upstreamPeer.Write([]byte("reply")); err != nil {
		t.Fatalf("reverse write after sustained upload: %v", err)
	}
	reply := make([]byte, len("reply"))
	if _, err := io.ReadFull(clientPeer, reply); err != nil {
		t.Fatalf("reverse read after sustained upload: %v", err)
	}
	if string(reply) != "reply" {
		t.Fatalf("reverse reply = %q", reply)
	}

	clientPeer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("tunnel did not stop after peer close")
	}
}

func TestHTTPProxyAuthAndSanitizedFailure(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	type receivedRequest struct {
		host   string
		header http.Header
	}
	requests := make(chan receivedRequest, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		request, err := http.ReadRequest(bufio.NewReader(connection))
		if err != nil {
			return
		}
		requests <- receivedRequest{host: request.Host, header: request.Header}
		_, _ = io.WriteString(connection, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
	}()

	proxyURL, err := url.Parse("http://client:very-secret@" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := contextWithTestTimeout(t)
	defer cancel()
	router := NewRouter(Config{DirectProxy: proxyURL})
	_, err = router.dialDirect(ctx, "public.example.invalid", "443")
	if err == nil {
		t.Fatal("dialHTTPProxy succeeded after a 407 response")
	}
	if strings.Contains(err.Error(), "very-secret") || strings.Contains(err.Error(), "client") {
		t.Fatalf("proxy error exposed credentials: %q", err)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("client:very-secret"))
	received := <-requests
	if received.host != "public.example.invalid:443" {
		t.Fatalf("upstream CONNECT host = %q, want hostname authority", received.host)
	}
	if got := received.header.Get("Proxy-Authorization"); got != wantAuth {
		t.Fatalf("Proxy-Authorization = %q, want %q", got, wantAuth)
	}
}

func contextWithTestTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 2*time.Second)
}

type testError struct {
	message string
}

func (e *testError) Error() string {
	return e.message
}
