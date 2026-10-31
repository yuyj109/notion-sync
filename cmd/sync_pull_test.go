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
