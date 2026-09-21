package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
	"github.com/alcxyz/bivrost/internal/proxy"
)

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
	router   *proxy.Router
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

func startProxy(ctx context.Context, c profile.Profile) (_ *runningProxy, resultErr error) {
	finish := diagnostics.Step(ctx, diagnostics.EventProxy)
	defer func() { finish(resultErr) }()
	if err := c.ValidateProxy(); err != nil {
		return nil, err
	}
	if err := c.ValidatePrivateHosts(); err != nil {
		return nil, err
	}
	upstream, err := upstreamProxy()
	if err != nil {
		return nil, err
	}
	if upstream != nil && (upstream.Hostname() == "localhost" || upstream.Hostname() == "127.0.0.1" || upstream.Hostname() == "::1") && upstream.Port() == strconv.Itoa(c.ProxyPort) {
		return nil, errors.New("upstream proxy cannot point back at bivrost")
	}
	listener, err := net.Listen("tcp4", profile.Loopback(c.ProxyPort))
	if err != nil {
		return nil, errors.New("proxy port is occupied; stop the existing listener or choose a different proxy_port")
	}
	router := proxy.NewRouter(proxy.Config{Registry: c.Registry, PrivateHosts: append([]string(nil), c.PrivateHosts...), SOCKSAddress: profile.Loopback(c.SOCKSPort), DirectProxy: upstream})
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
			fmt.Fprint(w, c.ProxyID())
			return
		}
		router.ServeHTTP(w, r)
	})
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	p.done = done
	return p, nil
}

func serveProxy(ctx context.Context, c profile.Profile) error {
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
	fmt.Println("Advanced proxy ready at " + c.ProxyURL() + ". Private ACR traffic requires a separately supplied SOCKS tunnel.")
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

func requireProxy(ctx context.Context, c profile.Profile) error {
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

func checkProxy(ctx context.Context, c profile.Profile, bridgeID string) error {
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ProxyURL()+"/bivrost/health", nil)
	if err != nil {
		return errors.New("invalid local proxy URL; check proxy_port in the configuration")
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("start bivrost acr proxy with the same configuration in another terminal first")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != c.ProxyID() {
		return errors.New("local proxy does not match this configuration")
	}
	if bridgeID != "" && resp.Header.Get("Bivrost-Podman-Bridge") != bridgeID {
		return errors.New("local proxy has no ready forward for the selected Podman Machine; restart bivrost acr proxy with the same environment and Podman connection")
	}
	return nil
}
