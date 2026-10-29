package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearSyncEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"NOTION_SYNC_SOURCE", "NOTION_SYNC_PARENT_PAGE_ID", "NOTION_SYNC_PARENT_DB_ID", "NOTION_SYNC_PRESERVE_HIERARCHY", "NOTION_SYNC_INCLUDE", "NOTION_SYNC_EXCLUDE"} {
		value, exists := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}
func TestResolvePrecedence(t *testing.T) {
	clearSyncEnv(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	content := "NOTION_SYNC_PARENT_PAGE_ID=0123456789abcdef0123456789abcdef\nNOTION_SYNC_SOURCE=from-dotenv\nNOTION_SYNC_PRESERVE_HIERARCHY=false\nNOTION_SYNC_INCLUDE=**/*.md,**/*.MD\nNOTION_SYNC_EXCLUDE=private/**\nNOTION_API_TOKEN=not-a-real-token\n"
	if err := os.WriteFile(envFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOTION_SYNC_SOURCE", "from-environment")
	t.Setenv("NOTION_SYNC_PRESERVE_HIERARCHY", "invalid-but-overridden")
	source := "from-option"
	preserve := true
	includes := []string{"notes/**/*.md"}
	c, err := Resolve(Options{EnvFile: envFile, EnvFileExplicit: true, Source: &source, PreserveHierarchy: &preserve, Include: &includes})
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != "from-option" || !c.Sync.PreserveHierarchy || len(c.Sync.Include) != 1 || c.Sync.Include[0] != includes[0] || c.Sync.Exclude[0] != "private/**" || c.Notion.ParentPageID != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("unexpected resolved settings: %+v", c)
	}
	if os.Getenv("NOTION_API_TOKEN") == "not-a-real-token" {
		t.Fatal("dotenv was exported to process environment")
	}
}
func TestResolveEnvAndDefaults(t *testing.T) {
	clearSyncEnv(t)
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("NOTION_SYNC_PARENT_PAGE_ID=0123456789abcdef0123456789abcdef\nNOTION_SYNC_SOURCE=dotenv-dir\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(Options{EnvFile: file})
	if err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	if c.SourcePath != filepath.Join(cwd, "dotenv-dir") || !c.Sync.PreserveHierarchy || len(c.Sync.Include) == 0 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	t.Setenv("NOTION_SYNC_SOURCE", "environment-dir")
	c, err = Resolve(Options{EnvFile: file})
	if err != nil || c.Source != "environment-dir" {
		t.Fatalf("environment did not override .env: %+v %v", c, err)
	}
}
func TestResolveFailuresAndFallback(t *testing.T) {
	clearSyncEnv(t)
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Resolve(Options{EnvFile: missing, EnvFileExplicit: true}); err == nil {
		t.Fatal("explicit missing env file must fail")
	}
	if _, err := Resolve(Options{EnvFile: missing}); !errors.Is(err, ErrParentRequired) {
		t.Fatalf("expected parent required, got %v", err)
	}
	id := "0123456789abcdef0123456789abcdef"
	c, err := Resolve(Options{EnvFile: missing, FallbackParentID: id, FallbackSource: "notes"})
	if err != nil || c.Notion.ParentPageID != "01234567-89ab-cdef-0123-456789abcdef" || c.Source != "notes" {
		t.Fatalf("fallback failed: %+v %v", c, err)
	}
	t.Setenv("NOTION_SYNC_PRESERVE_HIERARCHY", "invalid")
	if _, err := Resolve(Options{EnvFile: missing, FallbackParentID: id}); err == nil || !strings.Contains(err.Error(), "PRESERVE_HIERARCHY") {
		t.Fatalf("expected invalid bool, got %v", err)
	}
}

func TestDatabaseDestination(t *testing.T) {
	clearSyncEnv(t)
	t.Setenv("NOTION_SYNC_PARENT_PAGE_ID", "0123456789abcdef0123456789abcdef")
	db := "11111111111111111111111111111111"
	c, err := Resolve(Options{EnvFile: filepath.Join(t.TempDir(), "missing"), ParentDatabaseID: &db})
	if err != nil || c.Notion.ParentPageID != "" || c.Notion.ParentDatabaseID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("database did not override configured page: %+v, %v", c.Notion, err)
	}
	page := "0123456789abcdef0123456789abcdef"
	if _, err := Resolve(Options{ParentPageID: &page, ParentDatabaseID: &db}); err == nil {
		t.Fatal("expected conflicting flags to fail")
	}
	invalid := "invalid"
	if _, err := Resolve(Options{ParentDatabaseID: &invalid}); err == nil {
		t.Fatal("expected invalid database ID to fail")
	}
}

func TestDatabaseEnvironmentDestination(t *testing.T) {
	page := "0123456789abcdef0123456789abcdef"
	db := "11111111111111111111111111111111"
	otherDB := "22222222222222222222222222222222"
	for _, tc := range []struct {
		name, dotenv                          string
		env                                   map[string]string
		pageFlag, dbFlag                      *string
		fallback, wantPage, wantDB, wantError string
	}{
		{name: "dotenv database", dotenv: "NOTION_SYNC_PARENT_DB_ID=" + db, wantDB: db},
		{name: "environment overrides dotenv database", dotenv: "NOTION_SYNC_PARENT_DB_ID=" + db, env: map[string]string{"NOTION_SYNC_PARENT_DB_ID": otherDB}, wantDB: otherDB},
		{name: "environment database overrides dotenv page", dotenv: "NOTION_SYNC_PARENT_PAGE_ID=" + page, env: map[string]string{"NOTION_SYNC_PARENT_DB_ID": db}, wantDB: db},
		{name: "database prevents page fallback", env: map[string]string{"NOTION_SYNC_PARENT_DB_ID": db}, fallback: page, wantDB: db},
		{name: "environment page overrides dotenv database", dotenv: "NOTION_SYNC_PARENT_DB_ID=" + db, env: map[string]string{"NOTION_SYNC_PARENT_PAGE_ID": page}, wantPage: page},
		{name: "empty environment masks dotenv database", dotenv: "NOTION_SYNC_PARENT_DB_ID=" + db, env: map[string]string{"NOTION_SYNC_PARENT_DB_ID": ""}, wantError: "required"},
		{name: "both dotenv destinations", dotenv: "NOTION_SYNC_PARENT_PAGE_ID=" + page + "\nNOTION_SYNC_PARENT_DB_ID=" + db, wantError: "mutually exclusive"},
		{name: "both environment destinations", env: map[string]string{"NOTION_SYNC_PARENT_PAGE_ID": page, "NOTION_SYNC_PARENT_DB_ID": db}, wantError: "mutually exclusive"},
		{name: "page flag overrides environment database", env: map[string]string{"NOTION_SYNC_PARENT_DB_ID": db}, pageFlag: &page, wantPage: page},
		{name: "database flag overrides environment page", env: map[string]string{"NOTION_SYNC_PARENT_PAGE_ID": page}, dbFlag: &db, wantDB: db},
		{name: "flag overrides conflicting environment", env: map[string]string{"NOTION_SYNC_PARENT_PAGE_ID": page, "NOTION_SYNC_PARENT_DB_ID": db}, dbFlag: &otherDB, wantDB: otherDB},
		{name: "invalid database", env: map[string]string{"NOTION_SYNC_PARENT_DB_ID": "invalid"}, wantError: "invalid parent database ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearSyncEnv(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			file := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(file, []byte(tc.dotenv+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Resolve(Options{EnvFile: file, ParentPageID: tc.pageFlag, ParentDatabaseID: tc.dbFlag, FallbackParentID: tc.fallback})
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || c.Notion.ParentPageID != FormatID(tc.wantPage) || c.Notion.ParentDatabaseID != FormatID(tc.wantDB) {
				t.Fatalf("destination = %+v, error = %v", c.Notion, err)
			}
		})
	}
}
func TestInitEnvDoesNotOverwrite(t *testing.T) {
	name := filepath.Join(t.TempDir(), ".env")
	if err := InitEnv(name); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(name)
	if err := InitEnv(name); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected os.ErrExist, got %v", err)
	}
	after, _ := os.ReadFile(name)
	if string(before) != string(after) {
		t.Fatal("overwrote .env")
	}
}
