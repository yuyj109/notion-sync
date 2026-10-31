package cmd

import (
	"errors"
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
		var parent, database string
		var dry, push, overwrite bool
		c := &cobra.Command{Use: name + " <file>", Args: cobra.ExactArgs(1), Short: "Plan or synchronize one Markdown file", RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			if cmd.Root().PersistentFlags().Changed("parent-db-id") && (parent != "" || database != "") {
				return fmt.Errorf("--parent-db-id cannot be combined with --parent or --parent-db")
			}
			if parent != "" {
				parent, err = resolveID(cmd.Context(), parent, "page")
				if err != nil {
					return err
				}
			}
			if database != "" {
				database, err = resolveID(cmd.Context(), database, "data_source")
				if err != nil {
					return err
				}
			}
			fallback, err := syncer.FileRootID(args[0])
			if err != nil {
				return err
			}
			if fallback == "" {
				fallback = parent
			}
			cfg, err := opts.resolve(cmd, "", fallback, filepath.Dir(args[0]))
			if errors.Is(err, config.ErrParentRequired) && database != "" {
				var db struct {
					Object string `json:"object"`
					ID     string `json:"id"`
					Parent struct {
						Type   string `json:"type"`
						PageID string `json:"page_id"`
					} `json:"parent"`
				}
				if err := (notion.Runner{}).API(cmd.Context(), "GET", "v1/databases/"+database, nil, &db); err != nil {
					return err
				}
				if db.Object != "database" || config.NormalizeID(db.ID) != config.NormalizeID(database) || db.Parent.Type != "page_id" || !config.ValidID(db.Parent.PageID) {
					return fmt.Errorf("database parent is not a page; specify --parent-page-id")
				}
				cfg, err = opts.resolve(cmd, "", db.Parent.PageID, filepath.Dir(args[0]))
			}
			if err != nil {
				if errors.Is(err, config.ErrParentRequired) && database == "" {
					return fmt.Errorf("%w; for a single file, use --parent <page> or --parent-db <database>", err)
				}
				return err
			}
			return withLockRecovery(cmd, func() error {
				return syncer.RunFile(cmd.Context(), cfg, notion.Runner{}, args[0], parent, database, name == "sync" && !dry, func(format string, args ...any) { printSync(cmd.OutOrStdout(), format, args...) }, push || overwrite)
			})
		}}
		c.Flags().StringVar(&parent, "parent", "", "parent page ID, URL, or unique title")
		c.Flags().StringVar(&database, "parent-db", "", "parent database ID, URL, or unique title")
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
