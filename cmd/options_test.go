package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/yuyj109/notion-sync/internal/config"
	"github.com/yuyj109/notion-sync/internal/notion"
)

type parentIDClient struct {
	responses map[string]any
	errors    map[string]error
	requests  []string
}

func (client *parentIDClient) API(_ context.Context, method, endpoint string, _ any, result any) error {
	client.requests = append(client.requests, method+" "+endpoint)
	if err := client.errors[endpoint]; err != nil {
		return err
	}
	value, ok := client.responses[endpoint]
	if !ok {
		return fmt.Errorf("unexpected endpoint %s", endpoint)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, result)
}

func TestResolveParentID(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	pagePath, dbPath := "v1/pages/"+id, "v1/databases/"+id
	for _, test := range []struct {
		name, parentType string
		responses        map[string]any
		errors           map[string]error
		page, db         string
		errorContains    string
		expectedRequests int
	}{
		{name: "page", responses: map[string]any{pagePath: map[string]any{"object": "page", "id": id}}, page: id, expectedRequests: 1},
		{name: "database", responses: map[string]any{dbPath: map[string]any{"object": "database", "id": id}}, errors: map[string]error{pagePath: &notion.APIError{Status: "404"}}, db: id, expectedRequests: 2},
		{name: "database HTTP 400", responses: map[string]any{dbPath: map[string]any{"object": "database", "id": id}}, errors: map[string]error{pagePath: &notion.APIError{Status: "400"}}, db: id, expectedRequests: 2},
		{name: "explicit page", parentType: "page", responses: map[string]any{pagePath: map[string]any{"object": "page", "id": id}}, page: id, expectedRequests: 1},
		{name: "explicit database", parentType: "db", responses: map[string]any{dbPath: map[string]any{"object": "database", "id": id}}, db: id, expectedRequests: 1},
		{name: "explicit page fails without database fallback", parentType: "page", errors: map[string]error{pagePath: &notion.APIError{Status: "400"}}, errorContains: "400", expectedRequests: 1},
		{name: "explicit database fails without page fallback", parentType: "db", errors: map[string]error{dbPath: &notion.APIError{Status: "404"}}, errorContains: "404", expectedRequests: 1},
		{name: "invalid type", parentType: "other", errorContains: "parent type", expectedRequests: 0},
		{name: "authentication error", errors: map[string]error{pagePath: &notion.APIError{Status: "401"}}, errorContains: "401", expectedRequests: 1},
		{name: "network error", errors: map[string]error{pagePath: fmt.Errorf("connection lost")}, errorContains: "connection lost", expectedRequests: 1},
		{name: "blocked request", errors: map[string]error{pagePath: &notion.APIError{Status: "404", Blocked: true}}, errorContains: "404", expectedRequests: 1},
		{name: "blocked bad request", errors: map[string]error{pagePath: &notion.APIError{Status: "400", Blocked: true}}, errorContains: "400", expectedRequests: 1},
		{name: "bad request and database unavailable", errors: map[string]error{pagePath: &notion.APIError{Status: "400"}, dbPath: &notion.APIError{Status: "404"}}, errorContains: "400", expectedRequests: 2},
		{name: "both missing", errors: map[string]error{pagePath: &notion.APIError{Status: "404"}, dbPath: &notion.APIError{Status: "404"}}, errorContains: "404", expectedRequests: 2},
		{name: "trashed page", responses: map[string]any{pagePath: map[string]any{"object": "page", "id": id, "in_trash": true}}, errorContains: "trashed", expectedRequests: 1},
		{name: "unexpected page", responses: map[string]any{pagePath: map[string]any{"object": "page", "id": "22222222222222222222222222222222"}}, errorContains: "unexpected", expectedRequests: 1},
		{name: "unexpected database", responses: map[string]any{dbPath: map[string]any{"object": "page", "id": id}}, errors: map[string]error{pagePath: &notion.APIError{Status: "404"}}, errorContains: "unexpected", expectedRequests: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &parentIDClient{responses: test.responses, errors: test.errors}
			var cfg config.Config
			cfg.Notion.ParentID = id
			cfg.Notion.ParentType = test.parentType
			err := resolveParentID(context.Background(), &cfg, client)
			if test.errorContains != "" {
				if err == nil || !strings.Contains(err.Error(), test.errorContains) {
					t.Fatalf("error = %v, want %q", err, test.errorContains)
				}
			} else if err != nil || cfg.Notion.ParentID != "" || cfg.Notion.ParentPageID != test.page || cfg.Notion.ParentDatabaseID != test.db {
				t.Fatalf("parent = %+v, error = %v", cfg.Notion, err)
			}
			if len(client.requests) != test.expectedRequests {
				t.Fatalf("requests = %v", client.requests)
			}
		})
	}
}

func TestParentIDCommands(t *testing.T) {
	const parentID = "11111111-1111-1111-1111-111111111111"
	const dataSourceID = "22222222-2222-2222-2222-222222222222"
	for _, kind := range []string{"page", "database"} {
		for _, command := range []string{"sync", "sync env", "sync typed", "sync typed env", "diff", "pull", "page sync", "page sync typed", "page sync env", "page sync fallback", "page diff", "doctor", "recover"} {
			t.Run(kind+"/"+command, func(t *testing.T) {
				dir := t.TempDir()
				bin := filepath.Join(dir, "ntn")
				script := `#!/bin/sh
case "$*" in
  --version) printf '%s\n' 'ntn test' ;;
  *v1/users/me*) printf '%s\n' '{"object":"user"}' ;;
  *v1/pages/*)
    if [ "$TEST_PARENT_KIND" = database ]; then
	printf '%s\n' 'error (400 Bad Request validation_error): Provided ID is a database, not a page.' >&2
      exit 1
    fi
    printf '{"object":"page","id":"%s","in_trash":false}\n' "$TEST_PARENT_ID" ;;
  *v1/databases/*) printf '{"object":"database","id":"%s","data_sources":[{"id":"%s"}]}\n' "$TEST_PARENT_ID" "$TEST_SOURCE_ID" ;;
  *v1/data_sources/*) printf '{"object":"data_source","id":"%s","properties":{"Name":{"type":"title"}}}\n' "$TEST_SOURCE_ID" ;;
  *) printf '%s\n' "unexpected command: $*" >&2; exit 1 ;;
esac
`
				if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("TEST_PARENT_KIND", kind)
				t.Setenv("TEST_PARENT_ID", parentID)
				t.Setenv("TEST_SOURCE_ID", dataSourceID)
				t.Setenv("NOTION_SYNC_PARENT_ID", "")
				t.Setenv("NOTION_SYNC_PARENT_TYPE", "")
				t.Setenv("NOTION_SYNC_PARENT_PAGE_ID", "")
				t.Setenv("NOTION_SYNC_PARENT_DB_ID", "")
				source := filepath.Join(dir, "docs")
				if err := os.MkdirAll(filepath.Join(source, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(source, "nested", "note.md")
				body := "# Note\n"
				if command == "page sync fallback" {
					body = "---\nnotion-root-id: " + parentID + "\n---\n" + body
				}
				if command == "recover" {
					pendingParent := parentID
					if kind == "database" {
						pendingParent = dataSourceID
					}
					body = "---\nnotion-root-id: " + parentID + "\nnotion-pending-key: file:note.md\nnotion-pending-parent-id: " + pendingParent + "\nnotion-pending-title: Note\n---\n" + body
				}
				if err := os.WriteFile(file, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				args := []string{"--env-file", filepath.Join(dir, "settings.env")}
				if command == "sync env" || command == "sync typed env" || command == "page sync env" {
					t.Setenv("NOTION_SYNC_PARENT_ID", parentID)
				} else if command != "page sync" && command != "page sync typed" && command != "page sync fallback" && command != "page diff" {
					args = append(args, "--parent-id", parentID)
				}
				parentType := kind
				if kind == "database" {
					parentType = "db"
				}
				if command == "sync typed" || command == "page sync typed" {
					args = append(args, "--parent-type", parentType)
				} else if command == "sync typed env" {
					t.Setenv("NOTION_SYNC_PARENT_TYPE", parentType)
				}
				if err := os.WriteFile(filepath.Join(dir, "settings.env"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				switch command {
				case "sync", "sync env", "sync typed", "sync typed env":
					args = append(args, "sync", source, "--dry-run")
				case "diff":
					args = append(args, "diff", source)
				case "pull":
					args = append(args, "sync", source, "--pull", "--dry-run")
				case "page sync", "page sync typed":
					args = append(args, "page", "sync", file, "--parent-id", parentID, "--dry-run")
				case "page sync env", "page sync fallback":
					args = append(args, "page", "sync", file, "--dry-run")
				case "page diff":
					args = append(args, "page", "diff", file, "--parent-id", parentID)
				case "doctor":
					args = append(args, "doctor")
				case "recover":
					args = append(args, "recover", file, "--not-created")
				}
				root := newRootCommand()
				var output bytes.Buffer
				root.SetOut(&output)
				root.SetErr(&output)
				root.SetArgs(args)
				if err := root.Execute(); err != nil {
					t.Fatalf("%v: %v; output: %s", args, err, output.String())
				}
				if command == "sync" || command == "sync env" || command == "sync typed" || command == "sync typed env" {
					gotDirectory := strings.Contains(output.String(), "directory-create nested")
					if gotDirectory != (kind == "page") {
						t.Fatalf("unexpected hierarchy: %s", output.String())
					}
				}
			})
		}
	}
}

func TestLegacyParentFlagsRejected(t *testing.T) {
	id := "11111111111111111111111111111111"
	for _, args := range [][]string{
		{"--parent-id", id, "--parent-page-id", id, "sync", "."},
		{"--parent-id", id, "--parent-db-id", id, "sync", "."},
		{"--parent-id", id, "page", "sync", "note.md", "--parent", id},
		{"--parent-id", id, "page", "sync", "note.md", "--parent-db", id},
		{"page", "diff", "note.md", "--parent", id},
		{"page", "diff", "note.md", "--parent-db", id},
	} {
		root := newRootCommand()
		root.SetArgs(args)
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("args %v: expected unknown flag, got %v", args, err)
		}
	}
}

func TestCommandOptionsAndSourceArgument(t *testing.T) {
	var opts runtimeOptions
	root := &cobra.Command{Use: "test"}
	opts.addFlags(root)
	if err := root.PersistentFlags().Parse([]string{"--parent-id", "0123456789abcdef0123456789abcdef", "--source", "notes", "--preserve-hierarchy=false", "--include", "*.md", "--metadata-format", "comment"}); err != nil {
		t.Fatal(err)
	}
	opts.envFile = filepath.Join(t.TempDir(), "missing")
	c, err := opts.resolve(root, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != "notes" || c.Sync.PreserveHierarchy || c.Sync.MetadataFormat != "comment" || len(c.Sync.Include) != 1 || c.Sync.Include[0] != "*.md" {
		t.Fatalf("options ignored: %+v", c)
	}
	if _, err = opts.resolve(root, "other", "", ""); err == nil || !strings.Contains(err.Error(), "either") {
		t.Fatalf("expected source conflict, got %v", err)
	}
}

func TestHelpOnMissingParameters(t *testing.T) {
	t.Setenv("NOTION_SYNC_PARENT_ID", "")
	t.Setenv("NOTION_SYNC_PARENT_PAGE_ID", "")
	t.Setenv("NOTION_SYNC_PARENT_DB_ID", "")
	dir := t.TempDir()
	for _, tc := range []struct {
		args     []string
		wantHelp bool
	}{
		{[]string{"sync", dir}, true},
		{[]string{"page", "sync"}, true},
		{[]string{"page", "create"}, true},
		{[]string{"page", "view", "one", "two"}, false},
	} {
		var opts runtimeOptions
		root := &cobra.Command{Use: "notion-sync", SilenceErrors: true, SilenceUsage: true}
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		opts.addFlags(root)
		addSyncCommands(root, &opts)
		addPageCommands(root, &opts)
		showHelpOnMissingParameters(root)
		opts.envFile = filepath.Join(dir, "missing.env")
		root.SetArgs(tc.args)
		err := execute(root)
		if (err == nil) != tc.wantHelp {
			t.Fatalf("%v: error = %v, want help = %v", tc.args, err, tc.wantHelp)
		}
		if got := strings.Contains(stdout.String(), "Usage:"); got != tc.wantHelp {
			t.Errorf("%v: help = %v, want %v; stdout: %q", tc.args, got, tc.wantHelp, stdout.String())
		}
		if tc.wantHelp && stderr.Len() != 0 {
			t.Errorf("%v: unexpected stderr: %q", tc.args, stderr.String())
		}
	}
}

func TestMissingParentGuidance(t *testing.T) {
	t.Setenv("NOTION_SYNC_PARENT_ID", "")
	t.Setenv("NOTION_SYNC_PARENT_PAGE_ID", "")
	t.Setenv("NOTION_SYNC_PARENT_DB_ID", "")
	dir := t.TempDir()
	file := filepath.Join(dir, "note.md")
	if err := os.WriteFile(file, []byte("# Note\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"sync", dir}, []string{"--parent-id", "notion-database-id"}},
		{[]string{"diff", dir}, []string{"--parent-id", "notion-database-id"}},
		{[]string{"page", "sync", file}, []string{"--parent-id"}},
		{[]string{"page", "diff", file}, []string{"--parent-id"}},
	} {
		var opts runtimeOptions
		root := &cobra.Command{Use: "notion-sync", SilenceErrors: true, SilenceUsage: true}
		opts.addFlags(root)
		addSyncCommands(root, &opts)
		addPageCommands(root, &opts)
		root.SetArgs(append([]string{"--env-file", filepath.Join(dir, "empty.env")}, tc.args...))
		if err := os.WriteFile(filepath.Join(dir, "empty.env"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		err := root.Execute()
		if !errors.Is(err, config.ErrParentRequired) {
			t.Fatalf("%v: expected missing parent error, got %v", tc.args, err)
		}
		if tc.args[0] != "page" && strings.Count(err.Error(), "\n") != 1 {
			t.Errorf("%v: expected two lines, got %q", tc.args, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: error %q does not mention %q", tc.args, err, want)
			}
		}
	}
}
