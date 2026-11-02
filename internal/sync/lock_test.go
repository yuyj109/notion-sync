package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLockUsesTemporaryDirectoryAndExcludesOtherCalls(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	work := t.TempDir()
	unlock, err := lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "notion-sync-") {
		t.Fatalf("expected temp lock: %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(work, ".notion-sync.lock")); !os.IsNotExist(err) {
		t.Fatalf("unexpected workdir lock: %v", err)
	}
	if _, err := lock(); err == nil || !strings.Contains(err.Error(), filepath.Join(tmp, entries[0].Name())) {
		t.Fatalf("expected lock conflict with path: %v", err)
	}
	unlock()
	release, err := lock()
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	release()
}
