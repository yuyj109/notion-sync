package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/yuyj109/notion-sync/internal/config"
)

func TestCommandOptionsAndSourceArgument(t *testing.T) {
	var opts runtimeOptions
	root := &cobra.Command{Use: "test"}
	opts.addFlags(root)
	if err := root.PersistentFlags().Parse([]string{"--parent-page-id", "0123456789abcdef0123456789abcdef", "--source", "notes", "--preserve-hierarchy=false", "--include", "*.md"}); err != nil {
		t.Fatal(err)
	}
	opts.envFile = filepath.Join(t.TempDir(), "missing")
	c, err := opts.resolve(root, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != "notes" || c.Sync.PreserveHierarchy || len(c.Sync.Include) != 1 || c.Sync.Include[0] != "*.md" {
		t.Fatalf("options ignored: %+v", c)
	}
	if _, err = opts.resolve(root, "other", "", ""); err == nil || !strings.Contains(err.Error(), "either") {
		t.Fatalf("expected source conflict, got %v", err)
	}
}

func TestHelpOnMissingParameters(t *testing.T) {
	t.Setenv("NOTION_SYNC_PARENT_PAGE_ID", "")
	t.Setenv("NOTION_SYNC_PARENT_DB_ID", "")
	dir := t.TempDir()
	for _, tc := range []struct {
		args     []string
		wantHelp bool
	}{
		{[]string{"sync", dir}, true},
		{[]string{"page", "sync"}, true},
		{[]string{"page", "create"}, true},
		{[]string{"page", "view", "one", "two"}, false},
	} {
		var opts runtimeOptions
		root := &cobra.Command{Use: "notion-sync", SilenceErrors: true, SilenceUsage: true}
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		opts.addFlags(root)
		addSyncCommands(root, &opts)
		addPageCommands(root, &opts)
		showHelpOnMissingParameters(root)
		opts.envFile = filepath.Join(dir, "missing.env")
		root.SetArgs(tc.args)
		err := execute(root)
		if (err == nil) != tc.wantHelp {
			t.Fatalf("%v: error = %v, want help = %v", tc.args, err, tc.wantHelp)
		}
		if got := strings.Contains(stdout.String(), "Usage:"); got != tc.wantHelp {
			t.Errorf("%v: help = %v, want %v; stdout: %q", tc.args, got, tc.wantHelp, stdout.String())
		}
		if tc.wantHelp && stderr.Len() != 0 {
			t.Errorf("%v: unexpected stderr: %q", tc.args, stderr.String())
		}
	}
}

func TestMissingParentGuidance(t *testing.T) {
	t.Setenv("NOTION_SYNC_PARENT_PAGE_ID", "")
	t.Setenv("NOTION_SYNC_PARENT_DB_ID", "")
	dir := t.TempDir()
	file := filepath.Join(dir, "note.md")
	if err := os.WriteFile(file, []byte("# Note\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"sync", dir}, []string{"--parent-page-id", "notion-database-id", "page sync --parent-db"}},
		{[]string{"diff", dir}, []string{"--parent-page-id", "notion-database-id", "page diff --parent-db"}},
		{[]string{"page", "sync", file}, []string{"--parent <page>", "--parent-db <database>"}},
		{[]string{"page", "diff", file}, []string{"--parent <page>", "--parent-db <database>"}},
	} {
		var opts runtimeOptions
		root := &cobra.Command{Use: "notion-sync", SilenceErrors: true, SilenceUsage: true}
		opts.addFlags(root)
		addSyncCommands(root, &opts)
		addPageCommands(root, &opts)
		root.SetArgs(append([]string{"--env-file", filepath.Join(dir, "empty.env")}, tc.args...))
		if err := os.WriteFile(filepath.Join(dir, "empty.env"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		err := root.Execute()
		if !errors.Is(err, config.ErrParentRequired) {
			t.Fatalf("%v: expected missing parent error, got %v", tc.args, err)
		}
		if tc.args[0] != "page" && strings.Count(err.Error(), "\n") != 1 {
			t.Errorf("%v: expected two lines, got %q", tc.args, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: error %q does not mention %q", tc.args, err, want)
			}
		}
	}
}
