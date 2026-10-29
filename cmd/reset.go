package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	syncer "github.com/yuyj109/notion-sync/internal/sync"
)

func addResetCommand(root *cobra.Command) {
	var dryRun bool
	c := &cobra.Command{
		Use:   "reset <file-or-directory>",
		Short: "Clear local Notion destination and sync metadata",
		Long:  "Clear Notion destination and sync metadata from a Markdown file or all Markdown files under a directory. Does not change Notion. Pending creations must be resolved first.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return syncer.Reset(args[0], dryRun, func(format string, args ...any) { _, _ = fmt.Fprintf(cmd.OutOrStdout(), format+"\n", args...) })
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show files to clear without modifying them")
	root.AddCommand(c)
}
