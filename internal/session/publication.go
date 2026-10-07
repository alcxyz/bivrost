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
	// Probing stale descriptors can take seconds, so it runs without the session
	// lock and does not delay status, switch or unpublish requests. The session
	// state is checked again once the lock is held.
	a.mu.Lock()
	path, err := a.publicationStateLocked()
	withdrawals := a.withdrawals
	a.mu.Unlock()
	if path != "" || err != nil {
		return path, err
	}
	root, err := publicationRoot()
	if err != nil {
		return "", err
	}
	if err = cleanPublicationDirectory(a.ctx, root); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if path, err = a.publicationStateLocked(); path != "" || err != nil {
		return path, err
	}
	if a.withdrawals != withdrawals {
		return "", errors.New("session sharing was withdrawn while the publication was prepared")
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
	// The ID appears in names and the gateway hostname, so it must not reveal
	// any part of the capability.
	idBytes := make([]byte, 8)
	if _, err = rand.Read(idBytes); err != nil {
		return "", errors.New("cannot create publication identifier")
	}
	id := hex.EncodeToString(idBytes)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", errors.New("cannot open publication transport")
	}
	proxyURL := &url.URL{Scheme: "http", Host: ln.Addr().String(), User: url.UserPassword("bivrost", token)}
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

// publicationStateLocked returns the existing publication's path, or an error
// when this session cannot publish. Both are empty when publishing may proceed.
func (a *acrActivation) publicationStateLocked() (string, error) {
	if a.ctx.Err() != nil || a.pending != nil {
		return "", errors.New("session ended or switch pending")
	}
	if a.publication != nil {
		return a.publication.path, nil
	}
	if a.kubernetesUnavailable || a.config.AKS == nil || a.kubeconfig == "" {
		return "", errors.New("this session has no available Kubernetes target")
	}
	return "", nil
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
		a.withdrawals++
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

// A crash between creating and renaming a descriptor leaves its temporary
// file. Publish renames it at once, so an older one has no owner.
const staleTemporaryPublication = time.Minute

// Only remove recognised descriptors whose local owner is conclusively gone.
// Timeouts and other ambiguous failures leave files alone.
func cleanPublications(ctx context.Context) error {
	root, err := publicationRoot()
	if err != nil {
		return err
	}
	return cleanPublicationDirectory(ctx, root)
}

// A loopback dial is normally accepted or refused at once, but Windows retries
// a refused connection and may take about two seconds to report it. The dial
// timeout stays well above that so a closed port is recognised as stale rather
// than ambiguous. A live gateway answers /health immediately, so the response
// timeout is shorter. Probes run concurrently, so many stale descriptors cost
// about one probe period per batch.
const (
	publicationProbeDialTimeout     = 5 * time.Second
	publicationProbeResponseTimeout = 2 * time.Second
	publicationProbeConcurrency     = 8
)

type publicationProbe struct {
	path, host, authorization string
}

func cleanPublicationDirectory(ctx context.Context, root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return errors.New("cannot inspect publications")
	}
	var probes []publicationProbe
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "bivrost-") || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if strings.HasSuffix(entry.Name(), ".tmp") {
			if time.Since(info.ModTime()) > staleTemporaryPublication {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return errors.New("cannot remove stale publication")
				}
			}
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") || info.Size() > maxKubeconfigJSONSize {
			continue
		}
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
		probes = append(probes, publicationProbe{path: path, host: u.Host, authorization: auth})
	}

	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: publicationProbeDialTimeout}).DialContext,
		ResponseHeaderTimeout: publicationProbeResponseTimeout,
		DisableKeepAlives:     true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	gone := make([]bool, len(probes))
	limit := make(chan struct{}, publicationProbeConcurrency)
	var wg sync.WaitGroup
	for i, probe := range probes {
		limit <- struct{}{}
		wg.Go(func() {
			defer func() { <-limit }()
			gone[i] = publicationOwnerGone(ctx, client, probe)
		})
	}
	wg.Wait()
	for i, probe := range probes {
		if !gone[i] {
			continue
		}
		if err := os.Remove(probe.path); err != nil && !os.IsNotExist(err) {
			return errors.New("cannot remove stale publication")
		}
	}
	return ctx.Err()
}

// publicationOwnerGone reports whether the descriptor's gateway conclusively
// no longer exists: its port refuses connections, or a different owner rejects
// the capability. Timeouts and other failures are ambiguous.
func publicationOwnerGone(ctx context.Context, client *http.Client, probe publicationProbe) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+probe.host+"/health", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Proxy-Authorization", probe.authorization)
	response, err := client.Do(req)
	if err != nil {
		var op *net.OpError
		return errors.As(err, &op) && op.Op == "dial" && !op.Timeout() && ctx.Err() == nil
	}
	response.Body.Close()
	return response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusGone
}
