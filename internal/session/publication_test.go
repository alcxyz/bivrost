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
