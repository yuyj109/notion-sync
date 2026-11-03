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
		var dryRun, push, overwrite, pull, replaceDrawio bool
		c := &cobra.Command{Use: name + " [source]", Short: map[string]string{"diff": "Show a directory synchronization plan", "sync": "Synchronize a directory to Notion"}[name], Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if pull && (push || overwrite) {
				return fmt.Errorf("--pull and --push cannot be combined")
			}
			if pull && cmd.Root().PersistentFlags().Changed("icon") {
				return fmt.Errorf("--icon cannot be combined with --pull")
			}
			if pull && cmd.Root().PersistentFlags().Changed("property") {
				return fmt.Errorf("--property cannot be combined with --pull")
			}
			if replaceDrawio && !push && !overwrite {
				return fmt.Errorf("--replace-drawio-embeds requires --push")
			}
			if pull && opts.drawioEmbed {
				return fmt.Errorf("--drawio-embed cannot be combined with --pull")
			}
			source := ""
			if len(args) > 0 {
				source = args[0]
			}
			c, err := opts.resolve(cmd, source, "", "")
			if err != nil {
				if errors.Is(err, config.ErrParentRequired) {
					return fmt.Errorf("%w\ndirectory sync requires a default parent page or database even when files specify notion-database-id", err)
				}
				return err
			}
			if pull && c.Sync.DrawioEmbed {
				return fmt.Errorf("draw.io embed mode cannot be combined with --pull; set --drawio-embed=false to disable it")
			}
			if replaceDrawio && !c.Sync.DrawioEmbed {
				return fmt.Errorf("--replace-drawio-embeds requires --drawio-embed")
			}
			c.Sync.ReplaceDrawioEmbeds = replaceDrawio
			printSync(cmd.OutOrStdout(), "checking parent destination")
			if err := resolveParentID(cmd.Context(), &c, notion.Runner{}); err != nil {
				return err
			}
			return withLockRecovery(cmd, func() error {
				if pull {
					return syncer.Pull(cmd.Context(), c, notion.Runner{}, !dryRun, func(format string, args ...any) { printSync(cmd.OutOrStdout(), format, args...) })
				}
				return syncer.Run(cmd.Context(), c, notion.Runner{}, name == "sync" && !dryRun, func(format string, args ...any) { printSync(cmd.OutOrStdout(), format, args...) }, push || overwrite)
			})
		}}
		if name == "sync" {
			c.Flags().BoolVar(&dryRun, "dry-run", false, "show changes without writing")
			c.Flags().BoolVar(&push, "push", false, "overwrite conflicting Notion content")
			c.Flags().BoolVar(&replaceDrawio, "replace-drawio-embeds", false, "with --push and --drawio-embed, recreate embeds empty on updated pages")
			c.Flags().BoolVar(&overwrite, "overwrite", false, "alias for --push")
			_ = c.Flags().MarkHidden("overwrite")
			c.Flags().BoolVar(&pull, "pull", false, "import supported changes from conflicting Notion pages into local files")
		}
		root.AddCommand(c)
	}
}
