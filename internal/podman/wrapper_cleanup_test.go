package podman

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWrapperCleanupRetriesTransientLockAndBoundsPersistentFailure(t *testing.T) {
	locked := errors.New("file is in use")
	for _, unlock := range []int{1, 3, 0} {
		calls := 0
		err := removeWrapperWithRetry("owned", 3, 0, func(path string) error {
			if path != "owned" {
				t.Fatal("cleanup changed its target")
			}
			calls++
			if calls == unlock {
				return nil
			}
			return locked
		})
		if unlock == 0 {
			if !errors.Is(err, locked) || calls != 3 {
				t.Fatalf("persistent lock: calls=%d err=%v", calls, err)
			}
		} else if err != nil || calls != unlock {
			t.Fatalf("transient lock: calls=%d err=%v", calls, err)
		}
	}
}

func TestWrapperCleanupRemovesOnlyOwnedDirectory(t *testing.T) {
	parent := t.TempDir()
	owned := filepath.Join(parent, "bin")
	if err := os.Mkdir(owned, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owned, "podman"), []byte("owned"), 0700); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(parent, "keep")
	if err := os.WriteFile(sibling, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	CleanupWrapper(owned, &out)
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("directory remains: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("successful cleanup warned")
	}
}
