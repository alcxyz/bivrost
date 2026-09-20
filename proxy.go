package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	proxySetupTimeout = 15 * time.Second
	proxyIdleTimeout  = 5 * time.Minute
)

// proxyRouter is an HTTPS-only forward proxy. ACR connections use SOCKS5 with
// proxy-side DNS resolution; all other connections use the normal network or
// the configured HTTP CONNECT proxy.
type proxyRouter struct {
	registry     string
	privateHosts []string
	socksAddress string
	directProxy  *url.URL

	mu          sync.Mutex
	connections map[net.Conn]struct{}
	closed      bool
}

func (p *proxyRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		w.Header().Set("Allow", http.MethodConnect)
		http.Error(w, "this proxy supports HTTPS CONNECT requests only", http.StatusMethodNotAllowed)
		return
	}

	host, port, err := connectTarget(r)
	if err != nil {
		http.Error(w, "invalid CONNECT target", http.StatusBadRequest)
		return
	}
	if port != "443" {
		http.Error(w, "CONNECT is allowed only for port 443", http.StatusForbidden)
		return
	}
	if forbiddenTargetName(host) {
		http.Error(w, "CONNECT target is not allowed", http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), proxySetupTimeout)
	defer cancel()

	var upstream net.Conn
	if p.isTunnelHost(host) {
		upstream, err = p.dialSOCKS(ctx, host, port)
	} else {
		upstream, err = p.dialDirect(ctx, host, port)
	}
	if err != nil {
		diagnosticEvent(r.Context(), eventProxyRouteFailed)
		// Deliberately omit the underlying error. It may contain proxy
		// credentials, internal addresses, or other operational details.
		http.Error(w, "proxy connection failed", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "HTTP hijacking is unavailable", http.StatusInternalServerError)
		return
	}
	client, rw, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}

	if !p.track(client, upstream) {
		client.Close()
		upstream.Close()
		return
	}
	defer p.untrack(client, upstream)
	defer client.Close()
	defer upstream.Close()

	if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}

	clientWithBuffer := &readerConn{Conn: client, reader: rw.Reader}
	tunnel(clientWithBuffer, upstream)
}

// Close terminates all established tunnels and prevents new tunnels from being
// registered. It is safe to call more than once.
func (p *proxyRouter) Close() {
	p.mu.Lock()
	p.closed = true
	connections := make([]net.Conn, 0, len(p.connections))
	for connection := range p.connections {
		connections = append(connections, connection)
	}
	clear(p.connections)
	p.mu.Unlock()

	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (p *proxyRouter) track(connections ...net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	if p.connections == nil {
		p.connections = make(map[net.Conn]struct{})
	}
	for _, connection := range connections {
		p.connections[connection] = struct{}{}
	}
	return true
}

func (p *proxyRouter) untrack(connections ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, connection := range connections {
		delete(p.connections, connection)
	}
}

func connectTarget(r *http.Request) (string, string, error) {
	authority := r.Host
	if authority == "" && r.URL != nil {
		authority = r.URL.Host
	}
	if authority == "" {
		authority = r.RequestURI
	}
	if authority == "" || strings.ContainsAny(authority, "/?#@") {
		return "", "", errors.New("invalid authority")
	}

	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" || port == "" {
		return "", "", errors.New("invalid authority")
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !validDNSName(host) && net.ParseIP(host) == nil {
		return "", "", errors.New("invalid host")
	}
	return host, port, nil
}

func forbiddenTargetName(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func validDNSName(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || !asciiAlphaNumeric(label[0]) || !asciiAlphaNumeric(label[len(label)-1]) {
			return false
		}
		for i := 1; i < len(label)-1; i++ {
			if !asciiAlphaNumeric(label[i]) && label[i] != '-' {
				return false
			}
		}
	}
	return true
}

func asciiAlphaNumeric(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
}

func (p *proxyRouter) isTunnelHost(host string) bool {
	if p.isRegistryHost(host) {
		return true
	}
	for _, allowed := range p.privateHosts {
		if host == allowed {
			return true
		}
	}
	return false
}

func (p *proxyRouter) isRegistryHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	registry := strings.ToLower(strings.TrimSuffix(p.registry, "."))
	if !validDNSName(registry) || strings.Contains(registry, ".") {
		return false
	}
	if host == registry+".azurecr.io" {
		return true
	}

	prefix := registry + "."
	suffix := ".data.azurecr.io"
	if !strings.HasPrefix(host, prefix) || !strings.HasSuffix(host, suffix) {
		return false
	}
	region := strings.TrimSuffix(strings.TrimPrefix(host, prefix), suffix)
	return !strings.Contains(region, ".") && validDNSName(region)
}

func (p *proxyRouter) dialSOCKS(ctx context.Context, host, port string) (net.Conn, error) {
	if err := validateLocalSOCKSAddress(p.socksAddress); err != nil {
		return nil, err
	}
	socksAddress := p.socksAddress
	socksHost, socksPort, _ := net.SplitHostPort(socksAddress)
	if strings.EqualFold(socksHost, "localhost") {
		socksAddress = net.JoinHostPort("127.0.0.1", socksPort)
	}

	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", socksAddress)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			connection.Close()
		}
	}()
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		if err := connection.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	if _, err := connection.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return nil, err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(connection, response); err != nil {
		return nil, err
	}
	if response[0] != 0x05 || response[1] != 0x00 {
		return nil, errors.New("SOCKS authentication method rejected")
	}

	if len(host) > 255 {
		return nil, errors.New("SOCKS target name is too long")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return nil, errors.New("invalid SOCKS target port")
	}
	request := make([]byte, 0, 7+len(host))
	request = append(request, 0x05, 0x01, 0x00, 0x03, byte(len(host)))
	request = append(request, host...)
	request = binary.BigEndian.AppendUint16(request, uint16(portNumber))
	if _, err := connection.Write(request); err != nil {
		return nil, err
	}

	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	if header[0] != 0x05 || header[1] != 0x00 || header[2] != 0x00 {
		return nil, errors.New("SOCKS connection rejected")
	}
	if err := discardSOCKSAddress(connection, header[3]); err != nil {
		return nil, err
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	ok = true
	return connection, nil
}

func validateLocalSOCKSAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return errors.New("invalid SOCKS address")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("SOCKS address is not local")
	}
	return nil
}

func discardSOCKSAddress(reader io.Reader, addressType byte) error {
	length := 0
	switch addressType {
	case 0x01:
		length = net.IPv4len
	case 0x04:
		length = net.IPv6len
	case 0x03:
		lengthByte := []byte{0}
		if _, err := io.ReadFull(reader, lengthByte); err != nil {
			return err
		}
		length = int(lengthByte[0])
	default:
		return errors.New("invalid SOCKS address type")
	}
	_, err := io.CopyN(io.Discard, reader, int64(length+2))
	return err
}

func (p *proxyRouter) dialDirect(ctx context.Context, host, port string) (net.Conn, error) {
	// The configured corporate proxy is an explicit trust boundary and may use
	// hostname ACLs or its own DNS view, so retain the original authority. Direct
	// sockets resolve locally and pin a checked public address instead.
	if p.directProxy != nil {
		return dialHTTPProxy(ctx, p.directProxy, net.JoinHostPort(host, port))
	}
	addresses, err := publicAddresses(ctx, host)
	if err != nil {
		return nil, err
	}

	dialer := net.Dialer{}
	var dialErr error
	for _, address := range addresses {
		connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.String(), port))
		if err == nil {
			return connection, nil
		}
		dialErr = err
	}
	return nil, dialErr
}

func publicAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	public := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if address.IsGlobalUnicast() && !address.IsPrivate() && !address.IsLoopback() && !address.IsLinkLocalUnicast() {
			public = append(public, address)
		}
	}
	if len(public) == 0 {
		return nil, errors.New("target has no public address")
	}
	return public, nil
}

func dialHTTPProxy(ctx context.Context, proxyURL *url.URL, target string) (net.Conn, error) {
	if proxyURL == nil || !strings.EqualFold(proxyURL.Scheme, "http") || proxyURL.Host == "" || proxyURL.Fragment != "" || proxyURL.RawQuery != "" || proxyURL.Path != "" && proxyURL.Path != "/" {
		return nil, errors.New("invalid HTTP proxy configuration")
	}
	proxyHost := proxyURL.Hostname()
	if proxyHost == "" {
		return nil, errors.New("invalid HTTP proxy configuration")
	}
	proxyPort := proxyURL.Port()
	if proxyPort == "" {
		proxyPort = "80"
	}
	proxyAddress := net.JoinHostPort(proxyHost, proxyPort)

	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", proxyAddress)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			connection.Close()
		}
	}()
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		if err := connection.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	headers := make(http.Header)
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		if strings.Contains(username, ":") {
			return nil, errors.New("invalid HTTP proxy credentials")
		}
		password, _ := proxyURL.User.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		headers.Set("Proxy-Authorization", "Basic "+credentials)
	}
	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: headers,
	}
	if err := request.Write(connection); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("HTTP proxy returned status %d", response.StatusCode)
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	ok = true
	if reader.Buffered() != 0 {
		return &readerConn{Conn: connection, reader: reader}, nil
	}
	return connection, nil
}

type readerConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *readerConn) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}

type activityConn struct {
	net.Conn
	activity *tunnelActivity
}

func (c activityConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	if n > 0 {
		c.activity.touch()
	}
	return n, err
}

func (c activityConn) Write(buffer []byte) (int, error) {
	n, err := c.Conn.Write(buffer)
	if n > 0 {
		c.activity.touch()
	}
	return n, err
}

func tunnel(client, upstream net.Conn) {
	tunnelWithIdleTimeout(client, upstream, proxyIdleTimeout)
}

type tunnelActivity struct {
	mu          sync.Mutex
	connections [2]net.Conn
	timeout     time.Duration
}

func (a *tunnelActivity) touch() {
	a.mu.Lock()
	defer a.mu.Unlock()
	deadline := time.Now().Add(a.timeout)
	for _, connection := range a.connections {
		_ = connection.SetDeadline(deadline)
	}
}

func tunnelWithIdleTimeout(client, upstream net.Conn, timeout time.Duration) {
	activity := &tunnelActivity{connections: [2]net.Conn{client, upstream}, timeout: timeout}
	activity.touch()
	clientActive := activityConn{Conn: client, activity: activity}
	upstreamActive := activityConn{Conn: upstream, activity: activity}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(clientActive, upstreamActive)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(upstreamActive, clientActive)
		done <- struct{}{}
	}()
	<-done
	_ = client.Close()
	_ = upstream.Close()
	<-done
}
