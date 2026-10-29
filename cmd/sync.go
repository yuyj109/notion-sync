package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/yuyj109/notion-sync/internal/config"
	"github.com/yuyj109/notion-sync/internal/notion"
	syncer "github.com/yuyj109/notion-sync/internal/sync"
)

func addSyncCommands(root *cobra.Command, opts *runtimeOptions) {
	for _, name := range []string{"diff", "sync"} {
		var dryRun bool
		c := &cobra.Command{Use: name + " [source]", Short: map[string]string{"diff": "Show a directory synchronization plan", "sync": "Synchronize a directory to Notion"}[name], Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			source := ""
			if len(args) > 0 {
				source = args[0]
			}
			c, err := opts.resolve(cmd, source, "", "")
			if err != nil {
				if errors.Is(err, config.ErrParentRequired) {
					return fmt.Errorf("%w\ndirectory sync requires a default parent page or database even when files specify notion-database-id; for a single file use page %s --parent-db <database>", err, name)
				}
				return err
			}
			return syncer.Run(cmd.Context(), c, notion.Runner{}, name == "sync" && !dryRun, func(format string, args ...any) { _, _ = fmt.Fprintf(cmd.OutOrStdout(), format+"\n", args...) })
		}}
		if name == "sync" {
			c.Flags().BoolVar(&dryRun, "dry-run", false, "show changes without writing")
		}
		root.AddCommand(c)
	}
}
