package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/yuyj109/notion-sync/internal/notion"
	syncer "github.com/yuyj109/notion-sync/internal/sync"
)

func addRecoverCommand(root *cobra.Command, opts *runtimeOptions) {
	var pageID string
	var notCreated bool
	c := &cobra.Command{Use: "recover <file>", Short: "Resolve an interrupted creation after inspecting Notion", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		fallback, err := syncer.FileRootID(args[0])
		if err != nil {
			return err
		}
		cfg, err := opts.resolve(cmd, "", fallback, filepath.Dir(args[0]))
		if err != nil {
			return err
		}
		if pageID != "" {
			pageID, err = parseID(pageID)
			if err != nil {
				return err
			}
		}
		message, err := syncer.Recover(cmd.Context(), cfg, notion.Runner{}, args[0], pageID, notCreated)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), message)
		return err
	}}
	c.Flags().StringVar(&pageID, "page-id", "", "verified existing page ID or URL")
	c.Flags().BoolVar(&notCreated, "not-created", false, "confirm no page was created")
	root.AddCommand(c)
}
