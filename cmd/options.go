package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/yuyj109/notion-sync/internal/config"
	"github.com/yuyj109/notion-sync/internal/notion"
	syncer "github.com/yuyj109/notion-sync/internal/sync"
)

type runtimeOptions struct {
	envFile, source, parentID, parentType, metadataFormat string
	preserve                                              bool
	include, exclude                                      []string
}

func (o *runtimeOptions) addFlags(root *cobra.Command) {
	f := root.PersistentFlags()
	f.StringVar(&o.envFile, "env-file", config.DefaultEnvFile, "environment file")
	f.StringVar(&o.source, "source", "", "Markdown source directory (default ./docs)")
	f.StringVar(&o.parentID, "parent-id", "", "default Notion parent page or database ID")
	f.StringVar(&o.parentType, "parent-type", "auto", "parent type: auto, page, or db")
	f.BoolVar(&o.preserve, "preserve-hierarchy", true, "reproduce directory hierarchy")
	f.StringVar(&o.metadataFormat, "metadata-format", "frontmatter", "metadata format: frontmatter or comment")
	f.StringSliceVar(&o.include, "include", nil, "include patterns (repeatable or comma-separated)")
	f.StringSliceVar(&o.exclude, "exclude", nil, "exclude patterns (repeatable or comma-separated)")
}
func (o *runtimeOptions) resolve(cmd *cobra.Command, positionalSource, fallbackParent, fallbackSource string) (config.Config, error) {
	flags := cmd.Root().PersistentFlags()
	opts := config.Options{EnvFile: o.envFile, EnvFileExplicit: flags.Changed("env-file"), FallbackParentID: fallbackParent, FallbackSource: fallbackSource}
	if flags.Changed("source") {
		if positionalSource != "" {
			return config.Config{}, fmt.Errorf("use either a source argument or --source")
		}
		opts.Source = &o.source
	}
	if positionalSource != "" {
		opts.Source = &positionalSource
	}
	if flags.Changed("parent-id") {
		opts.ParentID = &o.parentID
	}
	if flags.Changed("parent-type") {
		opts.ParentType = &o.parentType
	}
	if flags.Changed("preserve-hierarchy") {
		opts.PreserveHierarchy = &o.preserve
	}
	if flags.Changed("metadata-format") {
		opts.MetadataFormat = &o.metadataFormat
	}
	if flags.Changed("include") {
		opts.Include = &o.include
	}
	if flags.Changed("exclude") {
		opts.Exclude = &o.exclude
	}
	return config.Resolve(opts)
}

func resolveParentID(ctx context.Context, cfg *config.Config, api syncer.Client) error {
	id := cfg.Notion.ParentID
	if id == "" {
		return nil
	}
	if cfg.Notion.ParentType != "" && cfg.Notion.ParentType != "auto" && cfg.Notion.ParentType != "page" && cfg.Notion.ParentType != "db" {
		return fmt.Errorf("parent type must be auto, page, or db")
	}
	var pageErr error
	if cfg.Notion.ParentType != "db" {
		var page struct {
			Object  string `json:"object"`
			ID      string `json:"id"`
			InTrash bool   `json:"in_trash"`
		}
		pageErr = api.API(ctx, "GET", "v1/pages/"+id, nil, &page)
		if pageErr == nil {
			if page.Object != "page" || config.NormalizeID(page.ID) != config.NormalizeID(id) {
				return fmt.Errorf("unexpected parent page response")
			}
			if page.InTrash {
				return fmt.Errorf("parent page is trashed")
			}
			cfg.Notion.ParentPageID = id
			cfg.Notion.ParentID = ""
			return nil
		}
		var apiErr *notion.APIError
		if cfg.Notion.ParentType == "page" || !errors.As(pageErr, &apiErr) || apiErr.Blocked || (apiErr.Status != "404" && apiErr.Status != "400") {
			return fmt.Errorf("resolve parent page: %w", pageErr)
		}
	}
	var database struct {
		Object  string `json:"object"`
		ID      string `json:"id"`
		InTrash bool   `json:"in_trash"`
	}
	if err := api.API(ctx, "GET", "v1/databases/"+id, nil, &database); err != nil {
		if pageErr == nil {
			return fmt.Errorf("resolve parent database: %w", err)
		}
		return fmt.Errorf("cannot resolve parent ID: page lookup: %w; database lookup: %w", pageErr, err)
	}
	if database.Object != "database" || config.NormalizeID(database.ID) != config.NormalizeID(id) {
		return fmt.Errorf("unexpected parent database response")
	}
	if database.InTrash {
		return fmt.Errorf("parent database is trashed")
	}
	cfg.Notion.ParentDatabaseID = id
	cfg.Notion.ParentID = ""
	return nil
}
