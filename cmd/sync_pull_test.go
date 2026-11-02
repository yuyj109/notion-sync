package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestSyncPullFlags(t *testing.T) {
	for _, flag := range []string{"--push", "--overwrite"} {
		t.Run(flag, func(t *testing.T) {
			root := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
			opts := runtimeOptions{}
			addSyncCommands(root, &opts)
			root.SetArgs([]string{"sync", "--pull", flag, "--dry-run"})
			if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
				t.Fatalf("expected incompatible flags error, got %v", err)
			}
		})
	}
}

func TestSyncPullRejectsDrawioEmbedFromEnvironment(t *testing.T) {
	t.Setenv("NOTION_SYNC_DRAWIO_EMBED", "true")
	t.Setenv("NOTION_SYNC_PARENT_ID", "0123456789abcdef0123456789abcdef")
	root := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
	opts := runtimeOptions{}
	opts.addFlags(root)
	addSyncCommands(root, &opts)
	root.SetArgs([]string{"sync", "--pull", "--dry-run"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("expected incompatible environment error, got %v", err)
	}
}

func TestPushFlagAvailableForDirectoryAndPageSync(t *testing.T) {
	root := &cobra.Command{Use: "test"}
	opts := runtimeOptions{}
	addSyncCommands(root, &opts)
	addPageCommands(root, &opts)
	for _, args := range [][]string{{"sync"}, {"page", "sync"}} {
		command, _, err := root.Find(args)
		if err != nil {
			t.Fatal(err)
		}
		if command.Flags().Lookup("push") == nil || command.Flags().Lookup("overwrite") == nil {
			t.Fatalf("push or compatibility alias missing from %v", args)
		}
	}
}
