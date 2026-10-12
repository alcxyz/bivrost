package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
)

// The registry of active sessions is a private directory with one record per
// session. Each owner holds an exclusive lock on its record for the session's
// lifetime, so a record whose lock can be taken belongs to a process that has
// exited. Reading, creating and changing records happens under registry.lock,
// so a record is never seen before its owner has locked it.
const (
	sessionRegistryLock    = "registry.lock"
	sessionRegistryWait    = 10 * time.Second
	sessionRecordPrefix    = "session-"
	sessionRecordSuffix    = ".json"
	maxSessionRecordSize   = 4096
	sessionRegistryRetries = 50 * time.Millisecond
)

// sessionRecord holds no credentials: only what session list shows and what
// other sessions need to apply the limit and the single registry session.
type sessionRecord struct {
	ID          string
	Environment string
	Command     string
	PID         int
	Started     time.Time
	MaxSessions int
	ProxyPort   int  `json:",omitempty"`
	SOCKSPort   int  `json:",omitempty"`
	ACR         bool `json:",omitempty"`
}

type activeSession struct {
	directory string
	path      string
	mu        sync.Mutex
	file      *os.File
	record    sessionRecord
}

func sessionRegistryRoot() (string, error) {
	return runtimeDirectory("sessions", "session registry")
}

// sessionLimit is the profile's max_sessions or, when unset, as many sessions
// as there are environments to choose from, and at least one.
func sessionLimit(c profile.Profile) int {
	if c.MaxSessions > 0 {
		return c.MaxSessions
	}
	count, err := profile.CountEnvironments()
	if err != nil || count < 1 {
		return 1
	}
	return count
}

func registerPlatformSession(ctx context.Context, c profile.Profile, command string) (*activeSession, error) {
	directory, err := sessionRegistryRoot()
	if err != nil {
		return nil, err
	}
	return registerSession(ctx, directory, c, command, sessionLimit(c))
}

// registerSession records a new session unless the active sessions already
// reach the lowest limit among them and the new one.
func registerSession(ctx context.Context, directory string, c profile.Profile, command string, limit int) (*activeSession, error) {
	guard, err := lockSessionRegistry(ctx, directory)
	if err != nil {
		return nil, err
	}
	defer releaseSessionRegistry(guard)
	active, err := scanSessions(directory)
	if err != nil {
		return nil, err
	}
	effective := limit
	if active := sessionLimitOf(active); active > 0 && active < effective {
		effective = active
	}
	if len(active) >= effective {
		return nil, fmt.Errorf("%d Bivrost sessions are active and the session limit is %d; exit one first (bivrost session list shows them)", len(active), effective)
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, errors.New("cannot create session identifier")
	}
	id := hex.EncodeToString(idBytes)
	path := filepath.Join(directory, sessionRecordPrefix+id+sessionRecordSuffix)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, errors.New("cannot create session record")
	}
	locked, err := tryLockFile(f)
	if err != nil || !locked {
		f.Close()
		os.Remove(path)
		return nil, errors.New("cannot lock session record")
	}
	s := &activeSession{directory: directory, path: path, file: f, record: sessionRecord{
		ID: id, Environment: c.Environment, Command: command, PID: os.Getpid(),
		Started: time.Now().UTC().Truncate(time.Second), MaxSessions: effective,
	}}
	if err := s.writeLocked(); err != nil {
		s.removeLocked()
		return nil, err
	}
	return s, nil
}

func (s *activeSession) setPorts(proxyPort, socksPort int) error {
	return s.update(func(record *sessionRecord, _ []sessionRecord) error {
		record.ProxyPort, record.SOCKSPort = proxyPort, socksPort
		return nil
	})
}

// claimACR marks this session as the one with registry access. Podman Machine
// is shared, so only one session at a time may have it.
func (s *activeSession) claimACR() error {
	return s.update(func(record *sessionRecord, active []sessionRecord) error {
		for _, other := range active {
			if other.ID != record.ID && other.ACR {
				environment := other.Environment
				if environment == "" {
					environment = "unknown"
				}
				return fmt.Errorf("another Bivrost session (%s) already has registry access; only one session at a time can use Podman registry access", environment)
			}
		}
		record.ACR = true
		return nil
	})
}

func (s *activeSession) releaseACR() {
	_ = s.update(func(record *sessionRecord, _ []sessionRecord) error {
		record.ACR = false
		return nil
	})
}

func (s *activeSession) update(change func(*sessionRecord, []sessionRecord) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return errors.New("session has ended")
	}
	guard, err := lockSessionRegistry(context.Background(), s.directory)
	if err != nil {
		return err
	}
	defer releaseSessionRegistry(guard)
	active, err := scanSessions(s.directory)
	if err != nil {
		return err
	}
	record := s.record
	if err := change(&record, active); err != nil {
		return err
	}
	s.record = record
	return s.writeLocked()
}

// close removes the record. A record left by a crash is unlocked and is
// removed by the next registry scan.
func (s *activeSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return
	}
	guard, err := lockSessionRegistry(context.Background(), s.directory)
	if err == nil {
		defer releaseSessionRegistry(guard)
	}
	s.removeLocked()
}

func (s *activeSession) removeLocked() {
	_ = unlockFile(s.file)
	_ = s.file.Close()
	s.file = nil
	_ = os.Remove(s.path)
}

func (s *activeSession) writeLocked() error {
	data, err := json.Marshal(s.record)
	if err != nil {
		return errors.New("cannot encode session record")
	}
	if err := s.file.Truncate(0); err != nil {
		return errors.New("cannot write session record")
	}
	if _, err := s.file.WriteAt(data, 0); err != nil {
		return errors.New("cannot write session record")
	}
	return nil
}

func lockSessionRegistry(ctx context.Context, directory string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(directory, sessionRegistryLock), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, errors.New("cannot open the session registry")
	}
	deadline := time.Now().Add(sessionRegistryWait)
	for {
		locked, err := tryLockFile(f)
		if err != nil {
			f.Close()
			return nil, errors.New("cannot lock the session registry")
		}
		if locked {
			return f, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, errors.New("timed out waiting for the session registry")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(sessionRegistryRetries):
		}
	}
}

func releaseSessionRegistry(f *os.File) {
	_ = unlockFile(f)
	_ = f.Close()
}

// scanSessions returns the records of live sessions and removes records whose
// owner has exited. The caller holds the registry lock. A live record that
// cannot be read still counts against the limit.
func scanSessions(directory string) ([]sessionRecord, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, errors.New("cannot inspect the session registry")
	}
	var active []sessionRecord
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, sessionRecordPrefix) || !strings.HasSuffix(name, sessionRecordSuffix) || !entry.Type().IsRegular() {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, sessionRecordPrefix), sessionRecordSuffix)
		path := filepath.Join(directory, name)
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			active = append(active, sessionRecord{ID: id})
			continue
		}
		locked, err := tryLockFile(f)
		if err == nil && locked {
			// The owner has exited. Windows cannot remove an open file.
			_ = unlockFile(f)
			f.Close()
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return nil, errors.New("cannot remove stale session record")
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxSessionRecordSize+1))
		f.Close()
		var record sessionRecord
		if err != nil || readErr != nil || len(data) > maxSessionRecordSize || json.Unmarshal(data, &record) != nil || record.ID != id {
			record = sessionRecord{ID: id}
		}
		active = append(active, record)
	}
	return active, nil
}

func cleanSessionRegistry(ctx context.Context) error {
	directory, err := sessionRegistryRoot()
	if err != nil {
		return err
	}
	guard, err := lockSessionRegistry(ctx, directory)
	if err != nil {
		return err
	}
	defer releaseSessionRegistry(guard)
	_, err = scanSessions(directory)
	return err
}

func listSessions(ctx context.Context, w io.Writer) error {
	directory, err := sessionRegistryRoot()
	if err != nil {
		return err
	}
	guard, err := lockSessionRegistry(ctx, directory)
	if err != nil {
		return err
	}
	active, err := scanSessions(directory)
	releaseSessionRegistry(guard)
	if err != nil {
		return err
	}
	return writeSessionList(w, active)
}

// sessionLimitOf returns the lowest limit among active sessions, or 0.
func sessionLimitOf(active []sessionRecord) int {
	limit := 0
	for _, record := range active {
		if record.MaxSessions > 0 && (limit == 0 || record.MaxSessions < limit) {
			limit = record.MaxSessions
		}
	}
	return limit
}

func writeSessionList(w io.Writer, active []sessionRecord) error {
	if len(active) == 0 {
		_, err := fmt.Fprintln(w, "No active Bivrost sessions.")
		return err
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Started.Before(active[j].Started) })
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ENVIRONMENT\tCOMMAND\tSTARTED\tPID\tPROXY\tSOCKS\tACR")
	for _, record := range active {
		environment, command, started, pid, proxyAddress, socks, acr := "unknown", "-", "-", "-", "-", "-", "-"
		if record.Environment != "" {
			environment = record.Environment
		}
		if record.Command != "" {
			command = record.Command
		}
		if !record.Started.IsZero() {
			started = record.Started.Local().Format("2006-01-02 15:04")
		}
		if record.PID > 0 {
			pid = fmt.Sprint(record.PID)
		}
		if record.ProxyPort > 0 {
			proxyAddress = profile.Loopback(record.ProxyPort)
		}
		if record.SOCKSPort > 0 {
			socks = profile.Loopback(record.SOCKSPort)
		}
		if record.ACR {
			acr = "enabled"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", environment, command, started, pid, proxyAddress, socks, acr)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if limit := sessionLimitOf(active); limit > 0 {
		_, err := fmt.Fprintf(w, "Session limit: %d\n", limit)
		return err
	}
	return nil
}
