package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
)

type diagnosticEventName uint8

const (
	eventCommand diagnosticEventName = iota + 1
	eventConfiguration
	eventAzureLogin
	eventProxy
	eventSSHCredentials
	eventBastion
	eventKubeconfig
	eventSSHForward
	eventShell
	eventCleanup
	eventProxyStopped
	eventSSHStopped
	eventBastionStopped
	eventForwardReady
	eventProxyRouteFailed
	eventACRLogin
	eventDoctor
	eventACRConnect
	eventPodmanBridge
	eventPodmanSession
)

func diagnosticName(event diagnosticEventName) string {
	switch event {
	case eventCommand:
		return "command"
	case eventConfiguration:
		return "configuration"
	case eventAzureLogin:
		return "azure_login"
	case eventProxy:
		return "proxy"
	case eventSSHCredentials:
		return "ssh_credentials"
	case eventBastion:
		return "bastion"
	case eventKubeconfig:
		return "kubeconfig"
	case eventSSHForward:
		return "ssh_forward"
	case eventShell:
		return "shell"
	case eventCleanup:
		return "cleanup"
	case eventProxyStopped:
		return "proxy_stopped"
	case eventSSHStopped:
		return "ssh_stopped"
	case eventBastionStopped:
		return "bastion_stopped"
	case eventForwardReady:
		return "forward_ready"
	case eventProxyRouteFailed:
		return "proxy_route_failed"
	case eventACRLogin:
		return "acr_login"
	case eventDoctor:
		return "doctor"
	case eventACRConnect:
		return "acr_connect"
	case eventPodmanSession:
		return "podman_session"
	case eventPodmanBridge:
		return "podman_bridge"
	default:
		return ""
	}
}

const diagnosticMaxBytes = 256 * 1024
const diagnosticMaxFiles = 10

type diagnosticContextKey struct{}

type diagnosticLog struct {
	path  string
	mu    sync.Mutex
	file  *os.File
	bytes int
	err   error
}

type diagnosticRecord struct {
	Timestamp string `json:"timestamp"`
	Event     string `json:"event"`
	Phase     string `json:"phase"`
	Outcome   string `json:"outcome,omitempty"`
	ElapsedMS *int64 `json:"elapsed_ms,omitempty"`
}

func diagnosticDirectory() (string, error) {
	if root := os.Getenv("XDG_STATE_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("XDG_STATE_HOME must be an absolute path")
		}
		return filepath.Join(root, "bivrost", "logs"), nil
	}
	if runtime.GOOS == "windows" {
		root := os.Getenv("LOCALAPPDATA")
		if !filepath.IsAbs(root) {
			var err error
			root, err = os.UserCacheDir()
			if err != nil {
				return "", errors.New("cannot locate diagnostics state directory")
			}
		}
		return filepath.Join(root, "bivrost", "logs"), nil
	}
	root, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(root) {
		return "", errors.New("cannot locate diagnostics state directory")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(root, "Library", "Logs", "bivrost"), nil
	}
	return filepath.Join(root, ".local", "state", "bivrost", "logs"), nil
}

// Only private directories are accepted. Existing permissions are never changed.
func prepareDiagnosticDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("cannot create diagnostics directory")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("diagnostics directory must be a real directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return errors.New("diagnostics directory must allow access only to its owner")
	}
	return nil
}

func startDiagnostics(ctx context.Context) (context.Context, *diagnosticLog, error) {
	directory, err := diagnosticDirectory()
	if err != nil {
		return ctx, nil, err
	}
	if err := prepareDiagnosticDirectory(directory); err != nil {
		return ctx, nil, err
	}
	// Reserve fixed slots atomically. O_EXCL refuses existing files and symlinks,
	// so simultaneous sessions cannot overwrite one another or exceed the quota.
	var file *os.File
	for slot := 0; slot < diagnosticMaxFiles; slot++ {
		path := filepath.Join(directory, "log-"+strconv.Itoa(slot)+".jsonl")
		file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return ctx, nil, errors.New("cannot create diagnostics file")
		}
	}
	if err != nil {
		return ctx, nil, errors.New("diagnostics limit reached; remove old log files before recording another session")
	}
	log := &diagnosticLog{path: file.Name(), file: file}
	return context.WithValue(ctx, diagnosticContextKey{}, log), log, nil
}

func (log *diagnosticLog) write(record diagnosticRecord) {
	if log == nil {
		return
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	data = append(data, '\n')
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.file == nil || log.err != nil {
		return
	}
	if log.bytes+len(data) > diagnosticMaxBytes {
		log.err = errors.New("diagnostics size limit reached; log is incomplete")
		return
	}
	n, err := log.file.Write(data)
	log.bytes += n
	if err != nil || n != len(data) {
		log.err = errors.New("cannot write diagnostics file")
	}
}

func (log *diagnosticLog) close() error {
	if log == nil {
		return nil
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.file != nil {
		if err := log.file.Close(); err != nil && log.err == nil {
			log.err = errors.New("cannot close diagnostics file")
		}
		log.file = nil
	}
	return log.err
}

func diagnosticEvent(ctx context.Context, event diagnosticEventName) {
	name := diagnosticName(event)
	if name == "" {
		return
	}
	log, _ := ctx.Value(diagnosticContextKey{}).(*diagnosticLog)
	log.write(diagnosticRecord{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Event: name, Phase: "event"})
}

// The error is classified, never formatted or serialized. Callers cannot attach
// arbitrary fields, endpoint names, command arguments, or subprocess output.
func diagnosticStep(ctx context.Context, event diagnosticEventName) func(error) {
	name := diagnosticName(event)
	log, _ := ctx.Value(diagnosticContextKey{}).(*diagnosticLog)
	if name == "" || log == nil {
		return func(error) {}
	}
	start := time.Now()
	log.write(diagnosticRecord{Timestamp: start.UTC().Format(time.RFC3339Nano), Event: name, Phase: "start"})
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			outcome := "success"
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				outcome = "timeout"
			case errors.Is(err, context.Canceled):
				outcome = "cancelled"
			case err != nil:
				outcome = "failure"
			}
			elapsed := time.Since(start).Milliseconds()
			log.write(diagnosticRecord{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Event: name, Phase: "end", Outcome: outcome, ElapsedMS: &elapsed})
		})
	}
}
