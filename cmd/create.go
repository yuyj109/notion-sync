package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yuyj109/notion-sync/internal/config"
	"github.com/yuyj109/notion-sync/internal/notion"
	syncer "github.com/yuyj109/notion-sync/internal/sync"
)

func titleProperty(title string) map[string]any {
	return map[string]any{"title": []any{map[string]any{"text": map[string]any{"content": title}}}}
}
func addPageCreate(parent *cobra.Command) {
	var title, content, destination string
	var asJSON bool
	c := &cobra.Command{Use: "create", Short: "Create an unmanaged page under a page", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		id, err := resolveID(cmd.Context(), destination, "page")
		if err != nil {
			return err
		}
		return createPage(cmd, id, "page_id", "title", title, content, asJSON)
	}}
	c.Flags().StringVar(&title, "title", "", "page title")
	c.Flags().StringVar(&content, "content", "", "Markdown body")
	c.Flags().StringVar(&destination, "parent", "", "parent page ID, URL, or unique title")
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	_ = c.MarkFlagRequired("title")
	_ = c.MarkFlagRequired("parent")
	parent.AddCommand(c)
}
func addDBCreate(parent *cobra.Command) {
	var title, content string
	var asJSON bool
	c := &cobra.Command{Use: "create <database>", Short: "Create an unmanaged database entry", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := resolveID(cmd.Context(), args[0], "data_source")
		if err != nil {
			return err
		}
		ds, property, err := syncer.ResolveDataSource(cmd.Context(), notion.Runner{}, id)
		if err != nil {
			return err
		}
		return createPage(cmd, ds, "data_source_id", property, title, content, asJSON)
	}}
	c.Flags().StringVarP(&title, "title", "t", "", "entry title")
	c.Flags().StringVar(&content, "content", "", "Markdown body")
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	_ = c.MarkFlagRequired("title")
	parent.AddCommand(c)
}
func createPage(cmd *cobra.Command, id, kind, property, title, content string, asJSON bool) error {
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("title must not be empty")
	}
	body := map[string]any{"parent": map[string]any{kind: id}, "properties": map[string]any{property: titleProperty(title)}}
	if content != "" {
		body["markdown"] = content
	}
	var result map[string]any
	if err := (notion.Runner{}).API(cmd.Context(), "POST", "v1/pages", body, &result); err != nil {
		return fmt.Errorf("creation failed or outcome is uncertain; inspect the destination before retrying: %w", err)
	}
	newID, ok := result["id"].(string)
	if !ok || !config.ValidID(newID) || result["object"] != "page" {
		return fmt.Errorf("unexpected create response; inspect destination before retrying")
	}
	if asJSON {
		return printJSON(cmd, result)
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), newID)
	return err
}
