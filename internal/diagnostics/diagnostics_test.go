package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func testDiagnosticLog(t *testing.T) (context.Context, *Log) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx, log, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Error(err)
		}
	})
	return ctx, log
}

func readDiagnosticRecords(t *testing.T, log *Log) []diagnosticRecord {
	t.Helper()
	data, err := os.ReadFile(log.path)
	if err != nil {
		t.Fatal(err)
	}
	var records []diagnosticRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var record diagnosticRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestDiagnosticsOnlyFixedFields(t *testing.T) {
	ctx, log := testDiagnosticLog(t)
	Event(ctx, EventCommand)
	Event(ctx, EventName(255))
	Step(ctx, EventName(255))(errors.New("secret-unknown-event"))
	cases := []struct {
		err     error
		outcome string
	}{
		{nil, "success"},
		{errors.New("access_token=FAKE-TOKEN password=FAKE-PASSWORD"), "failure"},
		{fmt.Errorf("FAKE-TOKEN: %w", context.Canceled), "cancelled"},
		{fmt.Errorf("FAKE-TOKEN: %w", context.DeadlineExceeded), "timeout"},
	}
	for _, c := range cases {
		end := Step(ctx, EventAzureLogin)
		end(c.err)
		end(errors.New("double finish"))
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	records := readDiagnosticRecords(t, log)
	if len(records) != 1+len(cases)*2 {
		t.Fatalf("got %d records", len(records))
	}
	for i, c := range cases {
		end := records[2+i*2]
		if end.Outcome != c.outcome || end.Phase != "end" || end.ElapsedMS == nil {
			t.Fatalf("bad end record: %#v", end)
		}
	}
	data, _ := os.ReadFile(log.path)
	for _, forbidden := range []string{"FAKE", "secret", "password", "access_token", "double finish"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("diagnostics contained forbidden data %q", forbidden)
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.Split(string(data), "\n")[0]), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 {
		t.Fatalf("unexpected event fields: %v", fields)
	}
}

func TestDiagnosticsPrivateDistinctFiles(t *testing.T) {
	ctx, first := testDiagnosticLog(t)
	_, second, err := Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.path == second.path {
		t.Fatal("diagnostics reused a file")
	}
	if runtime.GOOS == "windows" {
		return
	}
	for path, mode := range map[string]os.FileMode{first.path: 0600, filepath.Dir(first.path): 0700} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("mode %o, want %o", info.Mode().Perm(), mode)
		}
	}
}

func TestDiagnosticsBoundedConcurrentWrites(t *testing.T) {
	ctx, log := testDiagnosticLog(t)
	var writers sync.WaitGroup
	for range 8 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for range 1000 {
				Event(ctx, EventProxy)
			}
		}()
	}
	writers.Wait()
	if err := log.Close(); err == nil || err.Error() != "diagnostics size limit reached; log is incomplete" {
		t.Fatalf("expected size limit error, got %v", err)
	}
	log.err = nil // Expected size error checked; shared cleanup may close again.
	info, err := os.Stat(log.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > diagnosticMaxBytes || info.Size() < diagnosticMaxBytes-1024 {
		t.Fatalf("unexpected bounded size: %d", info.Size())
	}
	_ = readDiagnosticRecords(t, log)
	Event(ctx, EventCleanup)
	info2, _ := os.Stat(log.path)
	if info2.Size() != info.Size() {
		t.Fatal("wrote after close")
	}
}

func TestDiagnosticsRetentionDoesNotDeleteOrOverwrite(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var paths []string
	for range diagnosticMaxFiles {
		ctx, log, err := Start(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		Event(ctx, EventCommand)
		if err := log.Close(); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, log.path)
	}
	if _, _, err := Start(context.Background()); err == nil {
		t.Fatal("expected retention limit")
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			t.Fatalf("prior log changed: %v", err)
		}
	}
}

func TestDiagnosticsDisabledContext(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	Event(context.Background(), EventCommand)
	Step(context.Background(), EventAzureLogin)(errors.New("FAKE-TOKEN"))
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("disabled diagnostics created files: %v", err)
	}
}

func TestDiagnosticsRejectUnsafeDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions and symlinks")
	}
	for _, mode := range []string{"symlink", "permissions"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			directory, err := diagnosticDirectory()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(directory), 0700); err != nil {
				t.Fatal(err)
			}
			if mode == "symlink" {
				if err := os.Symlink(t.TempDir(), directory); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(directory, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := Start(context.Background()); err == nil {
				t.Fatal("accepted unsafe directory")
			}
		})
	}
}

func TestDiagnosticsConcurrentQuota(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var starters sync.WaitGroup
	logs := make(chan *Log, diagnosticMaxFiles*3)
	for range diagnosticMaxFiles * 3 {
		starters.Add(1)
		go func() {
			defer starters.Done()
			_, log, err := Start(context.Background())
			if err == nil {
				logs <- log
			}
		}()
	}
	starters.Wait()
	close(logs)
	count := 0
	for log := range logs {
		count++
		if err := log.Close(); err != nil {
			t.Error(err)
		}
	}
	if count != diagnosticMaxFiles {
		t.Fatalf("created %d logs, want %d", count, diagnosticMaxFiles)
	}
}

func TestDiagnosticsDoNotFollowExistingLogSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	directory, err := diagnosticDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareDiagnosticDirectory(directory); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "existing")
	const content = "existing content"
	if err := os.WriteFile(target, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "log-0.jsonl")); err != nil {
		t.Fatal(err)
	}
	ctx, log, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	Event(ctx, EventCommand)
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(log.path) != "log-1.jsonl" {
		t.Fatal("did not skip occupied slot")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != content {
		t.Fatal("modified symlink target")
	}
}

func TestDiagnosticsWriteErrorsAreSanitized(t *testing.T) {
	_, log := testDiagnosticLog(t)
	// Force a filesystem error without supplying it as a serialized record.
	if err := log.file.Close(); err != nil {
		t.Fatal(err)
	}
	log.write(diagnosticRecord{Event: "command", Phase: "event"})
	err := log.Close()
	if err == nil || err.Error() != "cannot write diagnostics file" {
		t.Fatalf("unexpected error: %v", err)
	}
	// The expected diagnostic error has been checked; allow shared cleanup.
	log.err = nil
}

func TestDiagnosticsRejectRelativeXDGState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "relative-FAKE-TOKEN")
	_, _, err := Start(context.Background())
	if err == nil || err.Error() != "XDG_STATE_HOME must be an absolute path" {
		t.Fatalf("unexpected error: %v", err)
	}
}
