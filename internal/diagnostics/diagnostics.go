// Package diagnostics records bounded, allowlisted lifecycle events.
package diagnostics

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

type EventName uint8

const (
	EventCommand EventName = iota + 1
	EventConfiguration
	EventAzureLogin
	EventProxy
	EventSSHCredentials
	EventBastion
	EventKubeconfig
	EventSSHForward
	EventShell
	EventCleanup
	EventProxyStopped
	EventSSHStopped
	EventBastionStopped
	EventForwardReady
	EventProxyRouteFailed
	EventACRLogin
	EventDoctor
	EventACRConnect
	EventPodmanBridge
	EventPodmanSession
)

func diagnosticName(event EventName) string {
	switch event {
	case EventCommand:
		return "command"
	case EventConfiguration:
		return "configuration"
	case EventAzureLogin:
		return "azure_login"
	case EventProxy:
		return "proxy"
	case EventSSHCredentials:
		return "ssh_credentials"
	case EventBastion:
		return "bastion"
	case EventKubeconfig:
		return "kubeconfig"
	case EventSSHForward:
		return "ssh_forward"
	case EventShell:
		return "shell"
	case EventCleanup:
		return "cleanup"
	case EventProxyStopped:
		return "proxy_stopped"
	case EventSSHStopped:
		return "ssh_stopped"
	case EventBastionStopped:
		return "bastion_stopped"
	case EventForwardReady:
		return "forward_ready"
	case EventProxyRouteFailed:
		return "proxy_route_failed"
	case EventACRLogin:
		return "acr_login"
	case EventDoctor:
		return "doctor"
	case EventACRConnect:
		return "acr_connect"
	case EventPodmanSession:
		return "podman_session"
	case EventPodmanBridge:
		return "podman_bridge"
	default:
		return ""
	}
}

const diagnosticMaxBytes = 256 * 1024
const diagnosticMaxFiles = 10

type diagnosticContextKey struct{}

type Log struct {
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

func Start(ctx context.Context) (context.Context, *Log, error) {
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
	log := &Log{path: file.Name(), file: file}
	return context.WithValue(ctx, diagnosticContextKey{}, log), log, nil
}

func (log *Log) write(record diagnosticRecord) {
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

func (log *Log) Close() error {
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

func Event(ctx context.Context, event EventName) {
	name := diagnosticName(event)
	if name == "" {
		return
	}
	log, _ := ctx.Value(diagnosticContextKey{}).(*Log)
	log.write(diagnosticRecord{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Event: name, Phase: "event"})
}

// The error is classified, never formatted or serialized. Callers cannot attach
// arbitrary fields, endpoint names, command arguments, or subprocess output.
func Step(ctx context.Context, event EventName) func(error) {
	name := diagnosticName(event)
	log, _ := ctx.Value(diagnosticContextKey{}).(*Log)
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

// Path returns the local diagnostic log path.
func (log *Log) Path() string { return log.path }
