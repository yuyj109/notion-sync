package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yuyj109/notion-sync/internal/notion"
	syncer "github.com/yuyj109/notion-sync/internal/sync"
)

type listResponse struct {
	Object     string           `json:"object"`
	Results    []map[string]any `json:"results"`
	HasMore    bool             `json:"has_more"`
	NextCursor *string          `json:"next_cursor"`
}

func fetchList(ctx context.Context, api syncer.Client, endpoint string, body map[string]any, limit int) ([]map[string]any, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("--limit must be between 1 and 1000")
	}
	var all []map[string]any
	cursor := ""
	seen := map[string]bool{}
	for len(all) < limit {
		size := min(limit-len(all), 100)
		body["page_size"] = size
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		var response listResponse
		if err := api.API(ctx, "POST", endpoint, body, &response); err != nil {
			return nil, err
		}
		if response.Object != "list" {
			return nil, fmt.Errorf("unexpected list response")
		}
		all = append(all, response.Results...)
		if !response.HasMore {
			break
		}
		if response.NextCursor == nil || *response.NextCursor == "" || seen[*response.NextCursor] {
			return nil, fmt.Errorf("invalid pagination cursor")
		}
		cursor = *response.NextCursor
		seen[cursor] = true
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}
func printJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func titleOf(obj map[string]any) string {
	extract := func(v any) string {
		a, ok := v.([]any)
		if !ok {
			return ""
		}
		var b strings.Builder
		for _, x := range a {
			if m, ok := x.(map[string]any); ok {
				if t, ok := m["plain_text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	if x := extract(obj["title"]); x != "" {
		return x
	}
	if p, ok := obj["properties"].(map[string]any); ok {
		for _, v := range p {
			if m, ok := v.(map[string]any); ok && m["type"] == "title" {
				return extract(m["title"])
			}
		}
	}
	return ""
}

func resolveID(ctx context.Context, value, kind string) (string, error) {
	if id, err := parseID(value); err == nil {
		return id, nil
	}
	if strings.Contains(value, "://") {
		return "", fmt.Errorf("invalid Notion URL")
	}
	items, err := fetchList(ctx, notion.Runner{}, "v1/search", map[string]any{"query": value, "filter": map[string]any{"property": "object", "value": kind}}, 1000)
	if err != nil {
		return "", err
	}
	if len(items) >= 1000 {
		return "", fmt.Errorf("too many results to resolve %q safely; specify an ID", value)
	}
	ids := map[string]string{}
	for _, item := range items {
		if titleOf(item) != value {
			continue
		}
		id, _ := item["id"].(string)
		if kind == "data_source" {
			p, _ := item["parent"].(map[string]any)
			id, _ = p["database_id"].(string)
		}
		if id != "" {
			ids[strings.ReplaceAll(strings.ToLower(id), "-", "")] = id
		}
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("%q matches %d %s objects; specify an ID", value, len(ids), kind)
	}
	for _, id := range ids {
		return id, nil
	}
	panic("unreachable")
}
func printList(cmd *cobra.Command, items []map[string]any, asJSON bool) error {
	if asJSON {
		return printJSON(cmd, items)
	}
	for _, item := range items {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%v\t%v\t%s\n", item["object"], item["id"], titleOf(item)); err != nil {
			return err
		}
	}
	return nil
}
func listCommand(filter string) *cobra.Command {
	var limit int
	var asJSON bool
	var query string
	c := &cobra.Command{Use: "list", Short: "List accessible " + filter + " objects", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]any{"filter": map[string]any{"property": "object", "value": filter}}
		if query != "" {
			body["query"] = query
		}
		items, err := fetchList(cmd.Context(), notion.Runner{}, "v1/search", body, limit)
		if err != nil {
			return err
		}
		if filter == "data_source" {
			for _, item := range items {
				parent, ok := item["parent"].(map[string]any)
				if !ok || parent["type"] != "database_id" || parent["database_id"] == nil {
					return fmt.Errorf("data source has an unexpected database parent")
				}
				item["data_source_id"] = item["id"]
				item["id"] = parent["database_id"]
				item["object"] = "database"
			}
		}
		return printList(cmd, items, asJSON)
	}}
	c.Flags().IntVar(&limit, "limit", 50, "maximum results (1-1000)")
	c.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	c.Flags().StringVarP(&query, "query", "q", "", "filter by title")
	return c
}
func addBrowseCommands(root *cobra.Command) {
	var limit int
	var asJSON bool
	search := &cobra.Command{Use: "search <query>", Short: "Search pages and data sources by title", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		items, err := fetchList(cmd.Context(), notion.Runner{}, "v1/search", map[string]any{"query": args[0]}, limit)
		if err != nil {
			return err
		}
		return printList(cmd, items, asJSON)
	}}
	search.Flags().IntVar(&limit, "limit", 50, "maximum results (1-1000)")
	search.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	root.AddCommand(search)
	db := &cobra.Command{Use: "db", Short: "Work with Notion databases"}
	root.AddCommand(db)
	db.AddCommand(listCommand("data_source"))
	var qLimit int
	var qJSON bool
	query := &cobra.Command{Use: "query <database>", Short: "List entries in a single-source database", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := resolveID(cmd.Context(), args[0], "data_source")
		if err != nil {
			return err
		}
		api := notion.Runner{}
		ds, _, err := syncer.ResolveDataSource(cmd.Context(), api, id)
		if err != nil {
			return err
		}
		items, err := fetchList(cmd.Context(), api, "v1/data_sources/"+ds+"/query", map[string]any{}, qLimit)
		if err != nil {
			return err
		}
		return printList(cmd, items, qJSON)
	}}
	query.Flags().IntVar(&qLimit, "limit", 50, "maximum results (1-1000)")
	query.Flags().BoolVar(&qJSON, "json", false, "output JSON")
	db.AddCommand(query)
	addDBCreate(db)
}
