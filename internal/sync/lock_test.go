package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuyj109/notion-sync/internal/config"
)

func TestLockUsesTemporaryDirectoryAndExcludesSameWorkDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	work := t.TempDir()
	var c config.Config
	c.WorkDir = work
	unlock, err := lock(c)
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
	if _, err := lock(c); err == nil || !strings.Contains(err.Error(), filepath.Join(tmp, entries[0].Name())) {
		t.Fatalf("expected lock conflict with path: %v", err)
	}
	other := c
	other.WorkDir = t.TempDir()
	releaseOther, err := lock(other)
	if err != nil {
		t.Fatal(err)
	}
	releaseOther()
	unlock()
	release, err := lock(c)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	release()
}

func TestLockResolvesWorkDirSymlink(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "work-link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	var c config.Config
	c.WorkDir = dir
	release, err := lock(c)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	c.WorkDir = link
	if _, err := lock(c); err == nil {
		t.Fatal("symlink bypassed lock")
	}
}
