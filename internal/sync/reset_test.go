package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResetAllowsCreatingAtNewDestination(t *testing.T) {
	c, api, dir := setup(t)
	file := filepath.Join(dir, "note.md")
	write(t, file, "# Example\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	oldPage := "00000000000000000000000000000065"
	newRoot := "11111111-1111-1111-1111-111111111111"
	api.pages[norm(newRoot)] = &fakePage{parent: newRoot}
	if err := Reset(file, false, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	c.Notion.ParentPageID = newRoot
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if api.creates != 2 || api.updates != 2 || api.pages[oldPage] == nil {
		t.Fatalf("expected new page and preserved old page: creates=%d updates=%d", api.creates, api.updates)
	}
}

func TestResetFilePreservesOtherContent(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "note.md")
	original := "---\ntitle: Keep me\nnotion-page-id: 01234567-89ab-cdef-0123-456789abcdef\nnotion-parent-page-id: 01234567-89ab-cdef-0123-456789abcdef\nnotion-root-id: 01234567-89ab-cdef-0123-456789abcdef\nnotion-local-hash: old\nnotion-remote-hash: old\n---\n# Body\n"
	write(t, filename, original)
	var output []string
	print := func(format string, args ...any) { output = append(output, format) }
	if err := Reset(filename, true, print); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, filename)) != original || len(output) != 1 {
		t.Fatal("dry-run modified file or omitted plan")
	}
	if err := Reset(filename, false, print); err != nil {
		t.Fatal(err)
	}
	data := string(mustRead(t, filename))
	if !strings.Contains(data, "title: Keep me") || !strings.HasSuffix(data, "# Body\n") || strings.Contains(data, "notion-") {
		t.Fatalf("unexpected result: %s", data)
	}
	if err := Reset(filename, false, print); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, filename)) != data {
		t.Fatal("second reset changed file")
	}
}

func TestResetDirectoryStopsBeforeAnyWriteOnPending(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.md")
	pending := filepath.Join(dir, "sub", "b.md")
	original := "---\nnotion-page-id: 01234567-89ab-cdef-0123-456789abcdef\n---\nfirst\n"
	write(t, first, original)
	write(t, pending, "---\nnotion-root-id: 01234567-89ab-cdef-0123-456789abcdef\nnotion-pending-key: file:b.md\nnotion-pending-parent-id: 01234567-89ab-cdef-0123-456789abcdef\nnotion-pending-title: title\n---\npending\n")
	if err := Reset(dir, false, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("expected pending error: %v", err)
	}
	if string(mustRead(t, first)) != original {
		t.Fatal("modified file before validating all files")
	}
}

func TestResetDirectoryAndSymlink(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "nested", "note.md")
	write(t, name, "---\nnotion-database-id: 01234567-89ab-cdef-0123-456789abcdef\n---\nbody\n")
	outside := filepath.Join(t.TempDir(), "outside.md")
	write(t, outside, "---\nnotion-page-id: 01234567-89ab-cdef-0123-456789abcdef\n---\noutside\n")
	if err := os.Symlink(outside, filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := Reset(dir, false, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, name)) != "body\n" {
		t.Fatal("nested metadata not cleared")
	}
	if !strings.Contains(string(mustRead(t, outside)), "notion-page-id") {
		t.Fatal("followed symlink")
	}
}
