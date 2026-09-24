package session

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
)

// The original connection owns both the capability and its lifetime. The
// inherited control file is a local session capability, never a registry token.
type activationControl struct {
	Address string
	Token   string
}
type acrActivation struct {
	ctx                      context.Context
	cancel                   context.CancelFunc
	mu                       sync.Mutex
	session                  *podmanSession
	config                   profile.Profile
	services                 platformServices
	directory, shell, script string
	kubeconfig               string
	kubernetesUnavailable    bool
	server                   *http.Server
	done                     chan struct{}
	pending                  *profile.Profile
}

func activationShell(shell string) bool {
	switch strings.ToLower(filepath.Base(shell)) {
	case "bash", "zsh", "pwsh", "pwsh.exe", "powershell", "powershell.exe":
		return true
	}
	return false
}

func newACRActivation(ctx context.Context, c profile.Profile, services platformServices, directory, shell string) *acrActivation {
	ctx, cancel := context.WithCancel(ctx)
	filename := "acr-environment.sh"
	if strings.Contains(strings.ToLower(filepath.Base(shell)), "pwsh") || strings.HasPrefix(strings.ToLower(filepath.Base(shell)), "powershell") {
		filename = "acr-environment.json"
	}
	return &acrActivation{ctx: ctx, cancel: cancel, config: c, services: services, directory: directory, shell: shell, script: filepath.Join(directory, filename), done: make(chan struct{})}
}

func (a *acrActivation) enable() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return errors.New("Bivrost session has ended")
	}
	if a.session != nil {
		return nil
	}
	if err := a.config.ValidateACR(); err != nil {
		return err
	}
	if !a.config.SkipRegistryLogin && !profile.ValidResourceName(a.config.RegistrySubscription) {
		return errors.New("set registry_subscription to the subscription containing ACR")
	}
	finish := diagnostics.Step(a.ctx, diagnostics.EventPodmanSession)
	s, err := a.services.startPodman(a.ctx, a.config, a.directory)
	finish(err)
	if err != nil {
		return err
	}
	accepted := false
	defer func() {
		if !accepted {
			s.close()
		}
	}()
	if err = a.services.checkRegistry(a.ctx, a.config); err != nil {
		return err
	}
	if !a.config.SkipRegistryLogin {
		if err = a.services.loginPodman(a.ctx, a.config, s); err != nil {
			return err
		}
	}
	if activationShell(a.shell) {
		if err = os.WriteFile(a.script, []byte(activationScript(a.shell, s)), 0600); err != nil {
			return errors.New("cannot prepare ACR shell environment")
		}
	}
	a.session = s
	accepted = true
	if s.done != nil {
		go func() {
			select {
			case <-s.done:
				close(a.done)
			case <-a.ctx.Done():
			}
		}()
	}
	return nil
}

// Unix shells source fixed variable names and quoted values. PowerShell reads
// data-only JSON and assigns allowlisted environment variables without executing it.
func activationScript(shellName string, s *podmanSession) string {
	keys := []string{"CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINER_SSHKEY", "CONTAINERS_CONF_OVERRIDE"}
	values := map[string]string{"CONTAINERS_CONF_OVERRIDE": s.configuration}
	if s.wrapperDirectory != "" {
		keys = append(keys, "BIVROST_PODMAN_BIN")
		values["BIVROST_PODMAN_BIN"] = s.wrapperDirectory
	}
	if s.host != "" {
		values["CONTAINER_HOST"] = s.host
		values["CONTAINER_SSHKEY"] = s.identity
	}
	powershell := strings.Contains(strings.ToLower(filepath.Base(shellName)), "pwsh") || strings.HasPrefix(strings.ToLower(filepath.Base(shellName)), "powershell")
	if powershell {
		data, _ := json.Marshal(values)
		return string(data)
	}
	var b strings.Builder
	for _, key := range keys {
		value, ok := values[key]
		if ok {
			fmt.Fprintf(&b, "export %s='%s'\n", key, strings.ReplaceAll(value, "'", "'\"'\"'"))
		} else {
			fmt.Fprintf(&b, "unset %s\n", key)
		}
	}
	if s.wrapperDirectory != "" {
		b.WriteString(shellinit.UnixPodmanPathInit)
	}
	return b.String()
}

func (a *acrActivation) listen(env []string) ([]string, error) {
	if !activationShell(a.shell) {
		return env, nil
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("cannot open session control listener")
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		ln.Close()
		return nil, errors.New("cannot prepare session control capability")
	}
	control := activationControl{Address: ln.Addr().String(), Token: hex.EncodeToString(token)}
	data, _ := json.Marshal(control)
	path := filepath.Join(a.directory, "acr-control.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		ln.Close()
		return nil, errors.New("cannot write session control capability")
	}
	a.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 3 * time.Minute, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || (r.URL.Path != "/enable" && r.URL.Path != "/status" && r.URL.Path != "/switch") || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+control.Token)) != 1 {
			http.Error(w, "session request rejected", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/switch" {
			request, err := decodeSwitchRequest(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			a.mu.Lock()
			ended, pending := a.ctx.Err() != nil, a.pending != nil
			a.mu.Unlock()
			if ended {
				http.Error(w, "session ended", http.StatusGone)
				return
			}
			if pending {
				http.Error(w, "a session switch is already pending", http.StatusConflict)
				return
			}
			target, err := loadSwitchTarget(request)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.ctx.Err() != nil {
				http.Error(w, "session ended", http.StatusGone)
				return
			}
			if a.pending != nil {
				http.Error(w, "a session switch is already pending", http.StatusConflict)
				return
			}
			a.pending = &target
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintln(w, "session switch accepted")
			return
		}
		if r.URL.Path == "/status" {
			a.mu.Lock()
			defer a.mu.Unlock()
			state := doctorSessionStatus{Config: a.config, Kubeconfig: a.kubeconfig, KubernetesUnavailable: a.kubernetesUnavailable}
			if a.ctx.Err() != nil {
				http.Error(w, "session ended", http.StatusGone)
				return
			}
			if a.session != nil {
				select {
				case <-a.session.done:
					http.Error(w, "session disconnected", http.StatusGone)
					return
				default:
				}
				state.Enabled = true
				state.LoginRefreshed = !a.config.SkipRegistryLogin
				state.Machine = a.session.host != ""
				state.Environment = map[string]string{}
				for _, entry := range a.session.environment(nil) {
					key, value, _ := strings.Cut(entry, "=")
					state.Environment[key] = value
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(state)
			return
		}
		if err := a.enable(); err != nil {
			http.Error(w, "ACR activation failed; check Podman availability, Azure login, and registry access", http.StatusConflict)
			return
		}
		fmt.Fprintln(w, "ACR is enabled for this session.")
	})}
	go a.server.Serve(ln)
	exe, err := os.Executable()
	if err != nil {
		return nil, errors.New("cannot locate Bivrost executable")
	}
	env = shellinit.ReplaceEnvironment(env, "BIVROST_CONTROL_FILE", path)
	env = shellinit.ReplaceEnvironment(env, "BIVROST_ACR_ENV_FILE", a.script)
	env = shellinit.ReplaceEnvironment(env, "BIVROST_EXECUTABLE", exe)
	return env, nil
}

func (a *acrActivation) close() {
	a.cancel()
	if a.server != nil {
		a.server.Close()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session != nil {
		a.session.close()
	}
	os.Remove(a.script)
	os.Remove(filepath.Join(a.directory, "acr-control.json"))
}

func (a *acrActivation) pendingSwitch() (profile.Profile, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		return profile.Profile{}, false
	}
	return *a.pending, true
}

func sessionRequest(ctx context.Context, endpoint string) (*http.Response, error) {
	return sessionRequestBody(ctx, endpoint, nil)
}

func sessionRequestBody(ctx context.Context, endpoint string, body io.Reader) (*http.Response, error) {
	path := os.Getenv("BIVROST_CONTROL_FILE")
	if os.Getenv("BIVROST_SESSION") == "" || path == "" {
		return nil, errors.New("this command requires an active Bivrost Bash, Zsh, or PowerShell session")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("session control is unavailable; reconnect")
	}
	var control activationControl
	if json.Unmarshal(data, &control) != nil || len(control.Token) != 64 {
		return nil, errors.New("invalid session control capability")
	}
	host, _, err := net.SplitHostPort(control.Address)
	if err != nil || host != "127.0.0.1" {
		return nil, errors.New("invalid session control address")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://"+control.Address+endpoint, body)
	if err != nil {
		return nil, errors.New("cannot prepare session activation")
	}
	req.Header.Set("Authorization", "Bearer "+control.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 3 * time.Minute, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	// A short-lived client must not retain idle connections.
	req.Close = true
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("session activation unavailable or interrupted; retry inside the active shell")
	}
	return response, nil
}

func enableSessionACR(ctx context.Context) error {
	response, err := sessionRequest(ctx, "/enable")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return errors.New("ACR activation failed; check Podman availability, Azure login, and registry access, then retry")
	}
	fmt.Println("ACR is enabled for this session.")
	fmt.Println(podmanBuildGuidance)
	return nil
}
