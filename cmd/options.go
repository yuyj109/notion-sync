package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/yuyj109/notion-sync/internal/config"
)

type runtimeOptions struct {
	envFile, source, parent, parentDB string
	preserve                          bool
	include, exclude                  []string
}

func (o *runtimeOptions) addFlags(root *cobra.Command) {
	f := root.PersistentFlags()
	f.StringVar(&o.envFile, "env-file", config.DefaultEnvFile, "environment file")
	f.StringVar(&o.source, "source", "", "Markdown source directory (default ./docs)")
	f.StringVar(&o.parent, "parent-page-id", "", "default Notion parent page ID")
	f.StringVar(&o.parentDB, "parent-db-id", "", "default Notion parent database ID (directory sync is flat)")
	f.BoolVar(&o.preserve, "preserve-hierarchy", true, "reproduce directory hierarchy")
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
	if flags.Changed("parent-page-id") {
		opts.ParentPageID = &o.parent
	}
	if flags.Changed("parent-db-id") {
		opts.ParentDatabaseID = &o.parentDB
	}
	if flags.Changed("preserve-hierarchy") {
		opts.PreserveHierarchy = &o.preserve
	}
	if flags.Changed("include") {
		opts.Include = &o.include
	}
	if flags.Changed("exclude") {
		opts.Exclude = &o.exclude
	}
	return config.Resolve(opts)
}
