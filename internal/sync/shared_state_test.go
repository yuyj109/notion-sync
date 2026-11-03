package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuyj109/notion-sync/internal/config"
)

func TestSyncShowsStateLocation(t *testing.T) {
	for _, mode := range []string{"sync", "page sync", "pull", "page pull"} {
		t.Run(mode, func(t *testing.T) {
			c, api, root := setup(t)
			file := filepath.Join(root, "note.md")
			write(t, file, "# Note\n")
			dir, err := sharedStateDir(root)
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			print := func(format string, args ...any) { fmt.Fprintf(&output, format+"\n", args...) }
			switch mode {
			case "sync":
				err = Run(context.Background(), c, api, false, print)
			case "page sync":
				err = RunFile(context.Background(), c, api, file, false, print)
			case "pull":
				err = Pull(context.Background(), c, api, false, print)
			case "page pull":
				err = PullFile(context.Background(), c, api, file, false, print)
			}
			if err != nil && (mode != "page pull" || !strings.Contains(err.Error(), "no synchronization baseline")) {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "config directory: "+filepath.Dir(dir)) || !strings.Contains(output.String(), "sync data directory: "+dir) {
				t.Fatalf("missing location: %s", output.String())
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("dry-run created data directory: %v", err)
			}
		})
	}
}

func testSharedStatePath(t *testing.T, c config.Config, root string) string {
	t.Helper()
	name, err := sharedStatePath(c, root)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func TestStateConfigDir(t *testing.T) {
	for _, tc := range []struct{ goos, home, xdg, want string }{
		{"darwin", "/home/user", "/custom", "/home/user/.notion-sync"},
		{"linux", "/home/user", "/custom", "/custom/notion-sync"},
		{"linux", "/home/user", "relative", "/home/user/.config/notion-sync"},
	} {
		got, err := stateConfigDir(tc.goos, tc.home, tc.xdg)
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %q, %v; want %q", tc.goos, got, err, tc.want)
		}
	}
}

func TestLegacyPendingMigrationAndReset(t *testing.T) {
	c, api, root := setup(t)
	owner := filepath.Join(root, "note.md")
	write(t, owner, "body")
	legacy := legacySharedStatePath(c, rootID)
	write(t, legacy, `{"version":1,"root_id":"`+rootID+`","pending":{"key":"file:note.md","parentId":"`+rootID+`","title":"note"},"pending_owner":"note.md","legacy_scan_complete":true}`)
	newPath := testSharedStatePath(t, c, rootID)
	if err := run(c, api, false); err == nil || !strings.Contains(err.Error(), "earlier create") {
		t.Fatalf("dry-run did not stop for legacy pending: %v", err)
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote state: %v", err)
	}
	if err := Reset(owner, true, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("reset did not detect legacy pending: %v", err)
	}
	if _, err := Recover(context.Background(), c, api, owner, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("recovery did not save new state: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy state was removed: %v", err)
	}
	if err := Reset(owner, true, func(string, ...any) {}); err != nil {
		t.Fatalf("stale legacy pending overrode recovered state: %v", err)
	}
}

func TestSharedStateIsScopedBySource(t *testing.T) {
	c, _, root := setup(t)
	other := filepath.Join(filepath.Dir(root), "other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	first := testSharedStatePath(t, c, rootID)
	c.SourcePath = other
	if second := testSharedStatePath(t, c, rootID); first == second {
		t.Fatal("different sources share the same state file")
	}
}

func TestSharedDirectorySurvivesExcludedOrDeletedFile(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "excluded", true: "deleted"}[remove], func(t *testing.T) {
			c, api, root := setup(t)
			old := filepath.Join(root, "nested", "old.md")
			write(t, old, "old")
			if err := run(c, api, true); err != nil {
				t.Fatal(err)
			}
			if remove {
				if err := os.Remove(old); err != nil {
					t.Fatal(err)
				}
			} else {
				c.Sync.Exclude = append(c.Sync.Exclude, "**/old.md")
			}
			write(t, filepath.Join(root, "nested", "new.md"), "new")
			if err := run(c, api, true); err != nil {
				t.Fatal(err)
			}
			if api.creates != 3 {
				t.Fatalf("directory was recreated: %d creations", api.creates)
			}
		})
	}
}

func TestPendingSurvivesExcludedOrDeletedOwner(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "excluded", true: "deleted"}[remove], func(t *testing.T) {
			c, api, root := setup(t)
			owner := filepath.Join(root, "nested", "old.md")
			write(t, owner, "old")
			api.failCreate = true
			if err := run(c, api, true); err == nil {
				t.Fatal("expected uncertain creation")
			}
			api.failCreate = false
			if remove {
				if err := os.Remove(owner); err != nil {
					t.Fatal(err)
				}
			} else {
				c.Sync.Exclude = append(c.Sync.Exclude, "**/old.md")
			}
			write(t, filepath.Join(root, "nested", "new.md"), "new")
			if err := run(c, api, true); err == nil || !strings.Contains(err.Error(), "earlier create") {
				t.Fatalf("missing pending stop: %v", err)
			}
			if api.creates != 1 {
				t.Fatal("retried uncertain creation")
			}
			if !remove {
				if _, err := Recover(context.Background(), c, api, owner, "", true); err != nil {
					t.Fatal(err)
				}
				if err := run(c, api, true); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestExcludedLegacyMetadataIsDiscovered(t *testing.T) {
	c, api, root := setup(t)
	old := filepath.Join(root, "nested", "old.md")
	write(t, old, "old")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(testSharedStatePath(t, c, rootID)); err != nil {
		t.Fatal(err)
	}
	c.Sync.Exclude = append(c.Sync.Exclude, "**/old.md")
	write(t, filepath.Join(root, "nested", "new.md"), "new")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.creates != 3 {
		t.Fatalf("legacy directory was recreated: %d creations", api.creates)
	}
}

func TestPendingJournalSurvivesBeforeMetadataWrite(t *testing.T) {
	c, api, root := setup(t)
	owner := filepath.Join(root, "file.md")
	write(t, owner, "body")
	s := State{RootID: rootID, Entries: map[string]Entry{}, Pending: &Pending{Key: "file:file.md", ParentID: rootID, Title: "file"}, PendingOwner: "file.md"}
	if err := saveSharedState(c, s); err != nil {
		t.Fatal(err)
	}
	if err := run(c, api, true); err == nil || !strings.Contains(err.Error(), "earlier create") {
		t.Fatalf("missing pending stop: %v", err)
	}
	if _, err := Recover(context.Background(), c, api, owner, "", true); err != nil {
		t.Fatal(err)
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
}

func TestResetRejectsSharedPendingWithoutFileMetadata(t *testing.T) {
	c, api, root := setup(t)
	owner := filepath.Join(root, "nested", "file.md")
	other := filepath.Join(root, "other.md")
	write(t, owner, "body")
	write(t, other, "---\nnotion-page-id: "+rootID+"\n---\nother\n")
	s := State{RootID: rootID, Entries: map[string]Entry{}, Pending: &Pending{Key: "file:nested/file.md", ParentID: rootID, Title: "file"}, PendingOwner: "nested/file.md"}
	if err := saveSharedState(c, s); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		if err := Reset(root, dry, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "pending") {
			t.Fatalf("dry=%v: expected pending error, got %v", dry, err)
		}
	}
	if !strings.Contains(string(mustRead(t, other)), "notion-page-id") {
		t.Fatal("reset modified another file")
	}
	if _, err := Recover(context.Background(), c, api, owner, "", true); err != nil {
		t.Fatal(err)
	}
	if err := Reset(root, false, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
}

func TestRunFileIgnoresOtherFilesMissingImages(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(map[bool]string{false: "preview", true: "sync"}[apply], func(t *testing.T) {
			c, api, root := setup(t)
			target := filepath.Join(root, "target.md")
			write(t, target, "# Target\n")
			write(t, filepath.Join(root, "other.md"), "![missing](missing.png)\n")
			if err := RunFile(context.Background(), c, api, target, apply, func(string, ...any) {}); err != nil {
				t.Fatalf("unrelated image blocked single-file operation: %v", err)
			}
			if err := run(c, api, false); err == nil {
				t.Fatal("directory preview must still validate all images")
			}
		})
	}
}

func TestCompletedMigrationSkipsExcludedFiles(t *testing.T) {
	c, api, root := setup(t)
	c.Sync.Exclude = append(c.Sync.Exclude, "excluded.md")
	write(t, filepath.Join(root, "note.md"), "body")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	// This would fail the legacy metadata parser if it were read.
	write(t, filepath.Join(root, "excluded.md"), "---\nnotion-root-id: [broken\n---\n")
	if err := run(c, api, true); err != nil {
		t.Fatalf("excluded file was inspected after migration: %v", err)
	}
}

func TestMigrationIsCheckpointedWithoutActions(t *testing.T) {
	c, api, root := setup(t)
	c.Sync.Exclude = append(c.Sync.Exclude, "excluded.md")
	if err := run(c, api, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(testSharedStatePath(t, c, rootID)); !os.IsNotExist(err) {
		t.Fatalf("dry run persisted state: %v", err)
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "excluded.md"), "---\nnotion-root-id: [broken\n---\n")
	if err := run(c, api, true); err != nil {
		t.Fatalf("empty sync did not checkpoint migration: %v", err)
	}
}

func TestFailedMigrationDoesNotCheckpoint(t *testing.T) {
	c, api, root := setup(t)
	c.Sync.Exclude = append(c.Sync.Exclude, "excluded.md")
	file := filepath.Join(root, "excluded.md")
	write(t, file, "---\nnotion-root-id: [broken\n---\n")
	if err := run(c, api, true); err == nil {
		t.Fatal("expected migration error")
	}
	if _, err := os.Stat(testSharedStatePath(t, c, rootID)); !os.IsNotExist(err) {
		t.Fatalf("failed migration was checkpointed: %v", err)
	}
	write(t, file, "ordinary document")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
}
