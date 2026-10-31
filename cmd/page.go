package cmd

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yuyj109/notion-sync/internal/config"
	"github.com/yuyj109/notion-sync/internal/notion"
	syncer "github.com/yuyj109/notion-sync/internal/sync"
)

var idAtEnd = regexp.MustCompile(`(?i)([0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

func parseID(value string) (string, error) {
	if config.ValidID(value) {
		return config.FormatID(value), nil
	}
	if strings.HasPrefix(value, "https://") {
		base, _, _ := strings.Cut(strings.SplitN(value, "?", 2)[0], "#")
		m := idAtEnd.FindStringSubmatch(base)
		if len(m) > 1 {
			return config.FormatID(m[1]), nil
		}
	}
	return "", fmt.Errorf("expected a Notion ID or URL: %s", value)
}
func addPageCommands(root *cobra.Command, opts *runtimeOptions) {
	page := &cobra.Command{Use: "page", Short: "Work with Notion pages"}
	root.AddCommand(page)
	for _, name := range []string{"sync", "diff"} {
		var dry, push, overwrite bool
		c := &cobra.Command{Use: name + " <file>", Args: cobra.ExactArgs(1), Short: "Plan or synchronize one Markdown file", RunE: func(cmd *cobra.Command, args []string) error {
			fallback, err := syncer.FileRootID(args[0])
			if err != nil {
				return err
			}
			cfg, err := opts.resolve(cmd, "", fallback, filepath.Dir(args[0]))
			if err != nil {
				return err
			}
			if err := resolveParentID(cmd.Context(), &cfg, notion.Runner{}); err != nil {
				return err
			}
			return withLockRecovery(cmd, func() error {
				return syncer.RunFile(cmd.Context(), cfg, notion.Runner{}, args[0], name == "sync" && !dry, func(format string, args ...any) { printSync(cmd.OutOrStdout(), format, args...) }, push || overwrite)
			})
		}}
		if name == "sync" {
			c.Flags().BoolVar(&dry, "dry-run", false, "show changes without writing")
			c.Flags().BoolVar(&push, "push", false, "overwrite pages changed in Notion with local content")
			c.Flags().BoolVar(&overwrite, "overwrite", false, "alias for --push")
			_ = c.Flags().MarkHidden("overwrite")
		}
		page.AddCommand(c)
	}
	page.AddCommand(listCommand("page"))
	var asJSON bool
	view := &cobra.Command{Use: "view <page>", Short: "Show a page as Markdown", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := resolveID(cmd.Context(), args[0], "page")
		if err != nil {
			return err
		}
		var result struct {
			Object    string   `json:"object"`
			ID        string   `json:"id"`
			Markdown  string   `json:"markdown"`
			Truncated *bool    `json:"truncated"`
			Unknown   []string `json:"unknown_block_ids"`
		}
		if err = (notion.Runner{}).API(cmd.Context(), "GET", "v1/pages/"+id+"/markdown", nil, &result); err != nil {
			return err
		}
		if result.Object != "page_markdown" || config.NormalizeID(result.ID) != config.NormalizeID(id) || result.Truncated == nil {
			return fmt.Errorf("unexpected Markdown response")
		}
		if asJSON {
			return printJSON(cmd, result)
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), result.Markdown); err != nil {
			return err
		}
		if *result.Truncated || len(result.Unknown) > 0 {
			if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: content is incomplete or contains unknown blocks"); err != nil {
				return err
			}
		}
		return nil
	}}
	view.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	page.AddCommand(view)
	addPageCreate(page)
}
