package session

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// A publication has its own transport capability. A cached kubeconfig cannot
// reconnect to a new owner when the old loopback port is reused.
type sessionPublication struct {
	path, target, upstream, authorization string
	server                                *http.Server
	mu                                    sync.Mutex
	closed                                bool
	connections                           map[net.Conn]struct{}
}

func publicationRoot() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".local", "state")
		}
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("publication base directory must be absolute")
	}
	for index, dir := range []string{filepath.Join(base, "bivrost"), filepath.Join(base, "bivrost", "published")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", errors.New("cannot create publication directory")
		}
		info, err := os.Lstat(dir)
		forbidden := os.FileMode(0022)
		if index == 1 {
			forbidden = 0077
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&forbidden != 0) {
			return "", errors.New("publication directory must be private and must not be a symlink")
		}
	}
	return filepath.Join(base, "bivrost", "published"), nil
}

func (a *acrActivation) publish() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil || a.pending != nil {
		return "", errors.New("session ended or switch pending")
	}
	if a.publication != nil {
		return a.publication.path, nil
	}
	if a.kubernetesUnavailable || a.config.AKS == nil || a.kubeconfig == "" {
		return "", errors.New("this session has no available Kubernetes target")
	}
	root, err := publicationRoot()
	if err != nil {
		return "", err
	}
	if err = cleanPublicationDirectory(a.ctx, root); err != nil {
		return "", err
	}
	input, err := os.ReadFile(a.kubeconfig)
	if err != nil || len(input) > maxKubeconfigJSONSize {
		return "", errors.New("session kubeconfig unavailable")
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return "", errors.New("cannot create publication capability")
	}
	token := hex.EncodeToString(secret)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", errors.New("cannot open publication transport")
	}
	proxyURL := &url.URL{Scheme: "http", Host: ln.Addr().String(), User: url.UserPassword("bivrost", token)}
	id := token[:16]
	target := "bivrost-" + id + ".invalid:443"
	data, upstream, err := publicationKubeconfig(input, a.config.Environment, id, target, proxyURL.String())
	if err != nil {
		ln.Close()
		return "", err
	}
	p := &sessionPublication{target: target, upstream: upstream, authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte("bivrost:"+token)), connections: map[net.Conn]struct{}{}}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second}
	go p.server.Serve(ln)
	f, err := os.CreateTemp(root, "bivrost-*.tmp")
	if err != nil {
		p.close()
		return "", errors.New("cannot create publication file")
	}
	p.path = f.Name()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		p.close()
		return "", errors.New("cannot write publication file")
	}
	finalPath := strings.TrimSuffix(p.path, ".tmp") + ".json"
	if err = os.Rename(p.path, finalPath); err != nil {
		p.close()
		return "", errors.New("cannot publish kubeconfig")
	}
	p.path = finalPath
	a.publication = p
	a.publicationRequested = true
	return p.path, nil
}

func publicationKubeconfig(input []byte, environment, id, target, proxyURL string) ([]byte, string, error) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(input, &doc) != nil {
		return nil, "", errors.New("invalid session kubeconfig")
	}
	current, _ := rawString(doc["current-context"])
	co, c, err := namedKubeObject(doc["contexts"], current, "context")
	if err != nil {
		return nil, "", err
	}
	clusterName, _ := rawString(c["cluster"])
	userName, _ := rawString(c["user"])
	cl, cluster, err := namedKubeObject(doc["clusters"], clusterName, "cluster")
	if err != nil {
		return nil, "", err
	}
	u, user, err := namedKubeObject(doc["users"], userName, "user")
	if err != nil {
		return nil, "", err
	}
	if err = validateAzureCLIExec(user); err != nil {
		return nil, "", err
	}
	server, _ := rawString(cluster["server"])
	parsed, err := url.Parse(server)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, "", errors.New("publication requires the session loopback Kubernetes endpoint")
	}
	tlsName, ok := rawString(cluster["tls-server-name"])
	if !ok || !validRemoteDNSName(tlsName) {
		return nil, "", errors.New("publication requires the original Kubernetes TLS identity")
	}
	ca, validCA := rawString(cluster["certificate-authority-data"])
	if !validCA || ca == "" {
		return nil, "", errors.New("publication requires an embedded cluster certificate authority")
	}
	if _, exists := cluster["certificate-authority"]; exists {
		return nil, "", errors.New("publication must not reference a separate certificate authority file")
	}
	if raw, exists := cluster["insecure-skip-tls-verify"]; exists {
		var insecure bool
		if json.Unmarshal(raw, &insecure) != nil || insecure {
			return nil, "", errors.New("publication requires Kubernetes TLS verification")
		}
		delete(cluster, "insecure-skip-tls-verify")
	}
	// Reuse exec-based identity, never serialize an Azure token or shell environment.
	var plugin map[string]json.RawMessage
	if json.Unmarshal(user["exec"], &plugin) != nil {
		return nil, "", errors.New("invalid Kubernetes exec configuration")
	}
	executable, err := exec.LookPath("kubelogin")
	if err != nil {
		return nil, "", errors.New("kubelogin is unavailable")
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, "", err
	}
	plugin["command"], _ = json.Marshal(executable)
	environmentEntries := []map[string]string{{"name": "PATH", "value": os.Getenv("PATH")}}
	if configDir := os.Getenv("AZURE_CONFIG_DIR"); configDir != "" {
		environmentEntries = append(environmentEntries, map[string]string{"name": "AZURE_CONFIG_DIR", "value": configDir})
	}
	plugin["env"], _ = json.Marshal(environmentEntries)
	user["exec"], _ = json.Marshal(plugin)
	label := "bivrost/" + environment + "/" + id
	co["name"], _ = json.Marshal(label)
	cl["name"], _ = json.Marshal(label)
	u["name"], _ = json.Marshal(label)
	c["cluster"], _ = json.Marshal(label)
	c["user"], _ = json.Marshal(label)
	cluster["server"], _ = json.Marshal("https://" + target)
	cluster["proxy-url"], _ = json.Marshal(proxyURL)
	doc["current-context"], _ = json.Marshal(label)
	doc["contexts"], err = singleNamedSection(co, "context", c)
	if err != nil {
		return nil, "", err
	}
	doc["clusters"], err = singleNamedSection(cl, "cluster", cluster)
	if err != nil {
		return nil, "", err
	}
	doc["users"], err = singleNamedSection(u, "user", user)
	if err != nil {
		return nil, "", err
	}
	doc["bivrost-publication"], _ = json.Marshal(1)
	data, err := json.Marshal(doc)
	return data, parsed.Host, err
}

func (p *sessionPublication) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Proxy-Authorization")), []byte(p.authorization)) != 1 {
		http.Error(w, "publication access denied", http.StatusForbidden)
		return
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		http.Error(w, "publication ended", http.StatusGone)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/health" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodConnect || r.Host != p.target || r.URL.Host != p.target {
		http.Error(w, "publication target denied", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	upstream, err := (&net.Dialer{}).DialContext(ctx, "tcp", p.upstream)
	if err != nil {
		http.Error(w, "session transport unavailable", http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "tunnel unavailable", http.StatusInternalServerError)
		return
	}
	client, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.connections[client] = struct{}{}
	p.connections[upstream] = struct{}{}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.connections, client); delete(p.connections, upstream); p.mu.Unlock() }()
	if _, err = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = rw.Flush(); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	done := make(chan struct{})
	go func() { io.Copy(upstream, rw); upstream.Close(); client.Close(); close(done) }()
	io.Copy(client, upstream)
	client.Close()
	upstream.Close()
	<-done
}

func (p *sessionPublication) close() {
	p.mu.Lock()
	p.closed = true
	for conn := range p.connections {
		conn.Close()
	}
	p.mu.Unlock()
	if p.server != nil {
		p.server.Close()
	}
	if p.path != "" {
		os.Remove(p.path)
	}
}
func (a *acrActivation) wantsPublication() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.publicationRequested
}

func (a *acrActivation) isPublished() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.publication != nil
}
func (a *acrActivation) handlePublication(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/publish" {
		path, err := a.publish()
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		fmt.Fprintln(w, path)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		http.Error(w, "session ended", http.StatusGone)
		return
	}
	if r.URL.Path == "/unpublish" {
		a.publicationRequested = false
		if a.publication != nil {
			a.publication.close()
			a.publication = nil
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if a.publication == nil {
		http.Error(w, "session is not published; run bivrost session publish", http.StatusConflict)
		return
	}
	fmt.Fprintln(w, a.publication.path)
}
func runSessionPublication(ctx context.Context, action string) error {
	endpoint := "/" + action
	if action == "path" {
		endpoint = "/publication-path"
	}
	response, err := sessionRequest(ctx, endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(data) > 4096 {
		return errors.New("invalid publication response")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return errors.New(strings.TrimSpace(string(data)))
	}
	if action == "unpublish" {
		fmt.Println("Session publication withdrawn.")
	} else {
		fmt.Print(string(data))
	}
	return nil
}

// Only remove recognised descriptors whose local owner is conclusively gone.
// Timeouts and other ambiguous failures leave files alone.
func cleanPublications(ctx context.Context) error {
	root, err := publicationRoot()
	if err != nil {
		return err
	}
	return cleanPublicationDirectory(ctx, root)
}
func cleanPublicationDirectory(ctx context.Context, root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return errors.New("cannot inspect publications")
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "bivrost-") || !strings.HasSuffix(entry.Name(), ".json") || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxKubeconfigJSONSize {
			continue
		}
		path := filepath.Join(root, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var doc struct {
			Marker   int `json:"bivrost-publication"`
			Clusters []struct {
				Cluster struct {
					Proxy string `json:"proxy-url"`
				} `json:"cluster"`
			} `json:"clusters"`
		}
		if json.Unmarshal(data, &doc) != nil || doc.Marker != 1 || len(doc.Clusters) != 1 {
			continue
		}
		u, err := url.Parse(doc.Clusters[0].Cluster.Proxy)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User == nil {
			continue
		}
		password, ok := u.User.Password()
		if !ok || u.User.Username() != "bivrost" || len(password) != 64 {
			continue
		}
		auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("bivrost:"+password))
		req, err := http.NewRequestWithContext(ctx, "GET", "http://"+u.Host+"/health", nil)
		if err != nil {
			continue
		}
		req.Header.Set("Proxy-Authorization", auth)
		response, err := client.Do(req)
		stale := false
		if err != nil {
			var op *net.OpError
			stale = errors.As(err, &op) && op.Op == "dial" && !op.Timeout() && ctx.Err() == nil
		} else {
			response.Body.Close()
			stale = response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusGone
		}
		if stale {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return errors.New("cannot remove stale publication")
			}
		}
	}
	return ctx.Err()
}
