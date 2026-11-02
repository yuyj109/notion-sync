package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if err := os.Remove(sharedStatePath(c, rootID)); err != nil {
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
	if _, err := os.Stat(sharedStatePath(c, rootID)); !os.IsNotExist(err) {
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
	if _, err := os.Stat(sharedStatePath(c, rootID)); !os.IsNotExist(err) {
		t.Fatalf("failed migration was checkpointed: %v", err)
	}
	write(t, file, "ordinary document")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
}
