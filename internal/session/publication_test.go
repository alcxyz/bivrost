package session

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
)

func publicationFixture(t *testing.T) (*acrActivation, []byte) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	bin := t.TempDir()
	name := "kubelogin"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	data := []byte(`{"apiVersion":"v1","kind":"Config","current-context":"original","contexts":[{"name":"original","context":{"cluster":"cluster","user":"identity"}}],"clusters":[{"name":"cluster","cluster":{"server":"https://127.0.0.1:19443","tls-server-name":"cluster.example.test","certificate-authority-data":"Y2E="}}],"users":[{"name":"identity","user":{"exec":{"apiVersion":"client.authentication.k8s.io/v1beta1","command":"kubelogin","args":["get-token","--login","azurecli"]}}}]}`)
	a := newACRActivation(context.Background(), profile.Profile{Environment: "test", AKS: &profile.AKS{Name: "test"}}, platformServices{}, t.TempDir(), "bash")
	a.kubeconfig = filepath.Join(a.directory, "kubeconfig")
	if err := os.WriteFile(a.kubeconfig, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	return a, data
}
func TestPublicationLifecycleAndIsolation(t *testing.T) {
	a, original := publicationFixture(t)
	path, err := a.publish()
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.publish()
	if err != nil || again != path {
		t.Fatal("publish must be idempotent", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("publication is not private")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	name, _ := rawString(doc["current-context"])
	if !strings.HasPrefix(name, "bivrost/test/") {
		t.Fatal("missing session label")
	}
	_, cluster, err := namedKubeObject(doc["clusters"], name, "cluster")
	if err != nil {
		t.Fatal(err)
	}
	server, _ := rawString(cluster["server"])
	if !strings.HasSuffix(server, ".invalid:443") {
		t.Fatal("published clients must depend on their own gateway")
	}
	tlsName, _ := rawString(cluster["tls-server-name"])
	if tlsName != "cluster.example.test" {
		t.Fatal("changed TLS identity")
	}
	proxyString, _ := rawString(cluster["proxy-url"])
	proxyURL, err := url.Parse(proxyString)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := proxyURL.User.Password()
	id := strings.TrimPrefix(name, "bivrost/test/")
	if len(id) != 16 || len(token) != 64 || strings.Contains(token, id) || strings.Contains(server, token[:16]) {
		t.Fatal("publication identifier reveals part of the gateway capability")
	}
	_, user, err := namedKubeObject(doc["users"], name, "user")
	if err != nil {
		t.Fatal(err)
	}
	var plugin struct{ Command string }
	json.Unmarshal(user["exec"], &plugin)
	if !filepath.IsAbs(plugin.Command) {
		t.Fatal("GUI authentication executable must be absolute")
	}
	source, _ := os.ReadFile(a.kubeconfig)
	if string(source) != string(original) {
		t.Fatal("modified original session kubeconfig")
	}
	w := httptest.NewRecorder()
	a.handlePublication(w, httptest.NewRequest("POST", "/unpublish", nil))
	if w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unpublish retained file")
	}
	if a.isPublished() {
		t.Fatal("unpublish retained state")
	}
	newer, err := a.publish()
	if err != nil || newer == path {
		t.Fatal("republish must get a new path", err)
	}
	a.close()
	if _, err = os.Stat(newer); !os.IsNotExist(err) {
		t.Fatal("owner close retained file")
	}
}
func TestPublicationRejectsUnavailableAndPending(t *testing.T) {
	a, _ := publicationFixture(t)
	a.kubernetesUnavailable = true
	if _, err := a.publish(); err == nil {
		t.Fatal("published unavailable Kubernetes")
	}
	a.kubernetesUnavailable = false
	a.pending = &profile.Profile{}
	if _, err := a.publish(); err == nil {
		t.Fatal("published during switch")
	}
	a.pending = nil
	a.cancel()
	if _, err := a.publish(); err == nil {
		t.Fatal("published ended session")
	}
}
func TestPublicationControllerRequiresOwnerCapability(t *testing.T) {
	a, _ := publicationFixture(t)
	env, err := a.listen(nil)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, e := range env {
		if strings.HasPrefix(e, "BIVROST_CONTROL_FILE=") {
			path = strings.TrimPrefix(e, "BIVROST_CONTROL_FILE=")
		}
	}
	control := readActivationControl(t, path)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	for _, endpoint := range []string{"/publish", "/unpublish", "/publication-path"} {
		req, _ := http.NewRequest("POST", "http://"+control.Address+endpoint, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatal("unauthenticated publication accepted")
		}
	}
	req, _ := http.NewRequest("POST", "http://"+control.Address+"/publish", nil)
	req.Header.Set("Authorization", "Bearer "+control.Token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal("owner request failed", resp.StatusCode)
	}
}
func TestPublicationGatewayRejectsTargetsAndRevokesOpenConnections(t *testing.T) {
	a, _ := publicationFixture(t)
	if _, err := a.publish(); err != nil {
		t.Fatal(err)
	}
	p := a.publication
	for _, test := range []struct{ target, auth string }{{p.target, ""}, {"other.invalid:443", p.authorization}, {"127.0.0.1:22", p.authorization}} {
		req := httptest.NewRequest("CONNECT", "http://"+test.target, nil)
		req.Header.Set("Proxy-Authorization", test.auth)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatal("gateway accepted invalid target/capability")
		}
	}
	// An actual established tunnel must close on withdrawal, not just its descriptor.
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	p.upstream = upstream.Addr().String()
	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn)
	}()
	gateway := httptest.NewServer(p)
	defer gateway.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(gateway.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", p.target, p.target, p.authorization)
	reader := bufio.NewReader(conn)
	req := &http.Request{Method: "CONNECT"}
	resp, err := http.ReadResponse(reader, req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal("connect failed", err)
	}
	conn.Write([]byte("hello"))
	data := make([]byte, 5)
	if _, err = io.ReadFull(reader, data); err != nil || string(data) != "hello" {
		t.Fatal("relay failed", err)
	}
	p.close()
	if _, err = reader.ReadByte(); err == nil {
		t.Fatal("withdrawal left connection open")
	}
	<-echoDone
}
func TestCleanPublicationRetainsLiveAndRemovesCrashedOwner(t *testing.T) {
	a, _ := publicationFixture(t)
	path, err := a.publish()
	if err != nil {
		t.Fatal(err)
	}
	if err = cleanPublications(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("removed live descriptor")
	}
	// Simulate owner death without normal file cleanup.
	a.publication.server.Close()
	if err = cleanPublications(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("stale descriptor not removed")
	}
}
func TestCleanPublicationRemovesOnlyAbandonedTemporaryFiles(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	root, err := publicationRoot()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * staleTemporaryPublication)
	// Each file is old unless noted; the value says whether it must be removed.
	files := map[string]bool{
		"bivrost-1.tmp":  true,
		"bivrost-2.tmp":  false, // recent: a live publish may still rename it
		"other-3.tmp":    false,
		"bivrost-4.temp": false,
	}
	for name := range files {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		if name == "bivrost-2.tmp" {
			continue
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS != "windows" {
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(target, old, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "bivrost-5.tmp")); err != nil {
			t.Fatal(err)
		}
		files["bivrost-5.tmp"] = false
	}
	if err := cleanPublications(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, stale := range files {
		_, err := os.Lstat(filepath.Join(root, name))
		if removed := os.IsNotExist(err); removed != stale {
			t.Errorf("%s removed=%v, want %v", name, removed, stale)
		}
	}
}

func TestPublicationRootFallbackAndSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink permissions")
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	root, err := publicationRoot()
	if err != nil || root != filepath.Join(base, "bivrost", "published") {
		t.Fatal(root, err)
	}
	other := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", other)
	if err = os.Symlink(t.TempDir(), filepath.Join(other, "bivrost")); err != nil {
		t.Fatal(err)
	}
	if _, err = publicationRoot(); err == nil {
		t.Fatal("accepted symlink root")
	}
}

func TestPublishedKubeconfigUsesAuthenticatedProxyAndOriginalTLSIdentity(t *testing.T) {
	a, input := publicationFixture(t)
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ready") }))
	defer api.Close()
	var document map[string]json.RawMessage
	json.Unmarshal(input, &document)
	outer, cluster, err := namedKubeObject(document["clusters"], "cluster", "cluster")
	if err != nil {
		t.Fatal(err)
	}
	cluster["server"], _ = json.Marshal(api.URL)
	cluster["tls-server-name"], _ = json.Marshal(api.Certificate().DNSNames[0])
	document["clusters"], _ = singleNamedSection(outer, "cluster", cluster)
	updated, _ := json.Marshal(document)
	if err = os.WriteFile(a.kubeconfig, updated, 0600); err != nil {
		t.Fatal(err)
	}
	path, err := a.publish()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &document)
	name, _ := rawString(document["current-context"])
	_, cluster, err = namedKubeObject(document["clusters"], name, "cluster")
	if err != nil {
		t.Fatal(err)
	}
	proxyString, _ := rawString(cluster["proxy-url"])
	proxyURL, _ := url.Parse(proxyString)
	server, _ := rawString(cluster["server"])
	tlsName, _ := rawString(cluster["tls-server-name"])
	roots := x509.NewCertPool()
	roots.AddCert(api.Certificate())
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: tlsName}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	response, err := client.Get(server + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "ready" {
		t.Fatal("unexpected TLS response")
	}
	a.publication.close()
	if response, err = client.Get(server + "/readyz"); err == nil {
		response.Body.Close()
		t.Fatal("cached client remained connected after unpublish")
	}
}

func TestPublicationRejectsChangedTrustConfiguration(t *testing.T) {
	a, input := publicationFixture(t)
	for name, change := range map[string]func(map[string]json.RawMessage){
		"missing CA":       func(c map[string]json.RawMessage) { delete(c, "certificate-authority-data") },
		"empty CA":         func(c map[string]json.RawMessage) { c["certificate-authority-data"] = json.RawMessage(`""`) },
		"insecure TLS":     func(c map[string]json.RawMessage) { c["insecure-skip-tls-verify"] = json.RawMessage(`true`) },
		"invalid TLS flag": func(c map[string]json.RawMessage) { c["insecure-skip-tls-verify"] = json.RawMessage(`"false"`) },
		"external CA":      func(c map[string]json.RawMessage) { c["certificate-authority"] = json.RawMessage(`"/unexpected/file"`) },
	} {
		t.Run(name, func(t *testing.T) {
			var doc map[string]json.RawMessage
			json.Unmarshal(input, &doc)
			outer, c, err := namedKubeObject(doc["clusters"], "cluster", "cluster")
			if err != nil {
				t.Fatal(err)
			}
			change(c)
			doc["clusters"], _ = singleNamedSection(outer, "cluster", c)
			data, _ := json.Marshal(doc)
			os.WriteFile(a.kubeconfig, data, 0600)
			if _, err = a.publish(); err == nil {
				t.Fatal("published unsafe trust configuration")
			}
		})
	}
}

func TestUnpublishClearsSuspendedIntent(t *testing.T) {
	a, _ := publicationFixture(t)
	a.publicationRequested = true
	a.kubernetesUnavailable = true
	if !a.wantsPublication() || a.isPublished() {
		t.Fatal("suspended publication intent lost")
	}
	w := httptest.NewRecorder()
	a.handlePublication(w, httptest.NewRequest("POST", "/unpublish", nil))
	if w.Code != http.StatusNoContent || a.wantsPublication() {
		t.Fatal("unpublish failed to clear suspended intent")
	}
}

func writePublicationDescriptor(t *testing.T, root, name, host string) string {
	t.Helper()
	path := filepath.Join(root, name)
	proxyURL := "http://bivrost:" + strings.Repeat("a", 64) + "@" + host
	data := fmt.Sprintf(`{"bivrost-publication":1,"clusters":[{"name":"c","cluster":{"proxy-url":%q}}]}`, proxyURL)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// silentGateway accepts probe connections and never answers them, like a live
// but unresponsive owner. Accepted connections are delivered on the channel.
func silentGateway(t *testing.T) (string, <-chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 16)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		for {
			select {
			case conn := <-accepted:
				conn.Close()
			default:
				return
			}
		}
	})
	return ln.Addr().String(), accepted
}

func closedLoopbackAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	ln.Close()
	return address
}

func TestPublishProbesStalePublicationsWithoutSessionLock(t *testing.T) {
	a, _ := publicationFixture(t)
	root, err := publicationRoot()
	if err != nil {
		t.Fatal(err)
	}
	address, accepted := silentGateway(t)
	unclear := writePublicationDescriptor(t, root, "bivrost-unclear.json", address)

	type result struct {
		path string
		err  error
	}
	published := make(chan result, 1)
	go func() {
		path, err := a.publish()
		published <- result{path, err}
	}()
	var probe net.Conn
	select {
	case probe = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("publish did not probe the existing descriptor")
	}

	// Another controller request needs the session lock while the probe waits.
	answered := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		a.handlePublication(w, httptest.NewRequest("POST", "/publication-path", nil))
		answered <- w.Code
	}()
	select {
	case code := <-answered:
		if code != http.StatusConflict {
			t.Fatalf("publication path status = %d, want not yet published", code)
		}
	case <-time.After(publicationProbeResponseTimeout / 2):
		t.Fatal("controller request waited for the stale-publication probe")
	}
	select {
	case <-published:
		t.Fatal("publish finished before its probe was answered")
	default:
	}

	probe.Close()
	got := <-published
	if got.err != nil || got.path == "" {
		t.Fatalf("publish() = %q, %v", got.path, got.err)
	}
	if _, err := os.Stat(unclear); err != nil {
		t.Fatal("removed a descriptor whose owner was not conclusively gone")
	}
}

func TestUnpublishDuringCleanupPreventsLaterPublication(t *testing.T) {
	a, _ := publicationFixture(t)
	root, err := publicationRoot()
	if err != nil {
		t.Fatal(err)
	}
	address, accepted := silentGateway(t)
	writePublicationDescriptor(t, root, "bivrost-unclear.json", address)

	published := make(chan error, 1)
	go func() {
		_, err := a.publish()
		published <- err
	}()
	var probe net.Conn
	select {
	case probe = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("publish did not probe the existing descriptor")
	}
	w := httptest.NewRecorder()
	a.handlePublication(w, httptest.NewRequest("POST", "/unpublish", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("unpublish status = %d", w.Code)
	}
	probe.Close()
	if err := <-published; err == nil {
		t.Fatal("publish succeeded after sharing was withdrawn")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.publication != nil || a.publicationRequested {
		t.Fatal("withdrawn sharing was reinstated")
	}
}

func TestCleanPublicationProbesConcurrently(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	root, err := publicationRoot()
	if err != nil {
		t.Fatal(err)
	}
	address, accepted := silentGateway(t)
	const unanswered = 3
	var kept, removed []string
	for i := range unanswered {
		kept = append(kept, writePublicationDescriptor(t, root, fmt.Sprintf("bivrost-silent-%d.json", i), address))
		removed = append(removed, writePublicationDescriptor(t, root, fmt.Sprintf("bivrost-closed-%d.json", i), closedLoopbackAddress(t)))
	}
	done := make(chan error, 1)
	go func() { done <- cleanPublications(context.Background()) }()
	// Sequential probing would reach the next silent owner only after the
	// previous probe's response timeout.
	var probes []net.Conn
	for range unanswered {
		select {
		case conn := <-accepted:
			probes = append(probes, conn)
		case <-time.After(publicationProbeResponseTimeout / 2):
			t.Fatal("stale publications were not probed concurrently")
		}
	}
	for _, conn := range probes {
		conn.Close()
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, path := range kept {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("removed unanswered descriptor %s", filepath.Base(path))
		}
	}
	for _, path := range removed {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("kept descriptor %s whose port refuses connections", filepath.Base(path))
		}
	}
}
