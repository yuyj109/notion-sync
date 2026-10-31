package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yuyj109/notion-sync/internal/config"
	"github.com/yuyj109/notion-sync/internal/notion"
	syncer "github.com/yuyj109/notion-sync/internal/sync"
)

var errMissingParameter = errors.New("missing required parameter")

func showHelpOnMissingParameters(root *cobra.Command) {
	for _, command := range root.Commands() {
		showHelpOnMissingParameters(command)
	}
	if root.RunE == nil && root.Run == nil {
		return
	}
	argsValidator := root.Args
	root.Args = func(cmd *cobra.Command, args []string) error {
		if argsValidator != nil {
			if err := argsValidator(cmd, args); err != nil {
				if len(args) == 0 && strings.Contains(cmd.Use, "<") {
					return fmt.Errorf("%w: %w", errMissingParameter, err)
				}
				return err
			}
		}
		if err := cmd.ValidateRequiredFlags(); err != nil {
			return fmt.Errorf("%w: %w", errMissingParameter, err)
		}
		return nil
	}
}

func execute(root *cobra.Command) error {
	command, err := root.ExecuteC()
	if err != nil && (errors.Is(err, errMissingParameter) || errors.Is(err, config.ErrParentRequired)) {
		return command.Help()
	}
	return err
}

func Execute() error {
	root := newRootCommand()
	root.SetContext(context.Background())
	return execute(root)
}

func newRootCommand() *cobra.Command {
	var opts runtimeOptions
	version := buildVersion()
	root := &cobra.Command{Use: "notion-sync", Short: "Synchronize local Markdown to Notion", Long: "Synchronize local Markdown to Notion\n\nVersion: " + version, Version: version, SilenceUsage: true, SilenceErrors: true}
	opts.addFlags(root)
	root.AddCommand(&cobra.Command{Use: "init", Short: "Create an .env file", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.InitEnv(opts.envFile); err != nil {
			return err
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Created", opts.envFile, "— set the parent page or database ID before use.")
		return err
	}})
	root.AddCommand(&cobra.Command{Use: "doctor", Short: "Check ntn and the configured parent page or database", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		r := notion.Runner{}
		c, err := opts.resolve(cmd, "", "", "")
		if err != nil {
			return err
		}
		version, err := r.Version(ctx)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), version); err != nil {
			return err
		}
		var user map[string]any
		if err = r.API(ctx, "GET", "v1/users/me", nil, &user); err != nil {
			return fmt.Errorf("authentication: %w", err)
		}
		if user["object"] != "user" {
			return fmt.Errorf("unexpected user response")
		}
		if err := resolveParentID(ctx, &c, r); err != nil {
			return err
		}
		if c.Notion.ParentDatabaseID != "" {
			if _, _, err = syncer.ResolveDataSource(ctx, r, c.Notion.ParentDatabaseID); err != nil {
				return fmt.Errorf("parent database: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Authentication and parent database read access: OK\nWrite access: unverified (doctor does not write)")
			return err
		}
		var page struct {
			Object  string `json:"object"`
			ID      string `json:"id"`
			InTrash bool   `json:"in_trash"`
		}
		if err = r.API(ctx, "GET", "v1/pages/"+c.Notion.ParentPageID, nil, &page); err != nil {
			return fmt.Errorf("parent page: %w", err)
		}
		if page.Object != "page" || config.NormalizeID(page.ID) != config.NormalizeID(c.Notion.ParentPageID) || page.InTrash {
			return fmt.Errorf("invalid or trashed parent page")
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Authentication and parent page read access: OK"); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "Write access: unverified (doctor does not write)")
		return err
	}})
	addSyncCommands(root, &opts)
	addRecoverCommand(root, &opts)
	addResetCommand(root)

	addPageCommands(root, &opts)
	addBrowseCommands(root)
	addAuthCommands(root)
	showHelpOnMissingParameters(root)
	return root
}
