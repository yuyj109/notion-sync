package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/yuyj109/notion-sync/internal/notion"
)

func addAuthCommands(root *cobra.Command) {
	auth := &cobra.Command{Use: "auth", Short: "Authenticate using the official Notion CLI"}
	auth.AddCommand(&cobra.Command{Use: "login", Short: "Start ntn's interactive login", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		process := exec.CommandContext(cmd.Context(), "ntn", "login")
		process.Stdin = os.Stdin
		process.Stdout = cmd.OutOrStdout()
		process.Stderr = cmd.ErrOrStderr()
		return process.Run()
	}}, &cobra.Command{Use: "status", Short: "Check the current ntn session", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		var user map[string]any
		if err := (notion.Runner{}).API(cmd.Context(), "GET", "v1/users/me", nil, &user); err != nil {
			return err
		}
		if user["object"] != "user" {
			return fmt.Errorf("unexpected authentication response")
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "Notion authentication: OK")
		return err
	}})
	root.AddCommand(auth)
}
