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
	for _, key := range []string{"NOTION_SYNC_SOURCE", "NOTION_SYNC_PARENT_ID", "NOTION_SYNC_PARENT_TYPE", "NOTION_SYNC_PARENT_PAGE_ID", "NOTION_SYNC_PARENT_DB_ID", "NOTION_SYNC_PRESERVE_HIERARCHY", "NOTION_SYNC_METADATA_FORMAT", "NOTION_SYNC_INCLUDE", "NOTION_SYNC_EXCLUDE"} {
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

func TestUnifiedParentID(t *testing.T) {
	clearSyncEnv(t)
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("NOTION_SYNC_PARENT_ID=0123456789abcdef0123456789abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id := "11111111111111111111111111111111"
	t.Setenv("NOTION_SYNC_PARENT_ID", id)
	c, err := Resolve(Options{EnvFile: file})
	if err != nil || c.Notion.ParentID != FormatID(id) || c.Notion.ParentPageID != "" || c.Notion.ParentDatabaseID != "" {
		t.Fatalf("unified parent = %+v, error = %v", c.Notion, err)
	}
	page := "22222222222222222222222222222222"
	c, err = Resolve(Options{EnvFile: file, ParentID: &page})
	if err != nil || c.Notion.ParentID != FormatID(page) || c.Notion.ParentPageID != "" {
		t.Fatalf("unified flag did not override environment: %+v, %v", c.Notion, err)
	}
	invalid := "invalid"
	if _, err := Resolve(Options{EnvFile: file, ParentID: &invalid}); err == nil || !strings.Contains(err.Error(), "invalid parent ID") {
		t.Fatalf("invalid parent ID accepted: %v", err)
	}
}

func TestParentTypePrecedence(t *testing.T) {
	clearSyncEnv(t)
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("NOTION_SYNC_PARENT_TYPE=db\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{EnvFile: file, FallbackParentID: "0123456789abcdef0123456789abcdef"}
	c, err := Resolve(opts)
	if err != nil || c.Notion.ParentType != "db" {
		t.Fatalf("dotenv type = %q, err = %v", c.Notion.ParentType, err)
	}
	t.Setenv("NOTION_SYNC_PARENT_TYPE", "page")
	c, err = Resolve(opts)
	if err != nil || c.Notion.ParentType != "page" {
		t.Fatalf("environment type = %q, err = %v", c.Notion.ParentType, err)
	}
	parentType := "auto"
	opts.ParentType = &parentType
	c, err = Resolve(opts)
	if err != nil || c.Notion.ParentType != "auto" {
		t.Fatalf("flag type = %q, err = %v", c.Notion.ParentType, err)
	}
	parentType = "invalid"
	if _, err := Resolve(opts); err == nil || !strings.Contains(err.Error(), "parent type") {
		t.Fatalf("invalid parent type accepted: %v", err)
	}
}

func TestLegacyParentEnvironmentIgnored(t *testing.T) {
	clearSyncEnv(t)
	file := filepath.Join(t.TempDir(), ".env")
	id := "11111111111111111111111111111111"
	if err := os.WriteFile(file, []byte("NOTION_SYNC_PARENT_PAGE_ID="+id+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOTION_SYNC_PARENT_DB_ID", id)
	if _, err := Resolve(Options{EnvFile: file}); !errors.Is(err, ErrParentRequired) {
		t.Fatalf("legacy environment must not set the parent: %v", err)
	}
}

func TestUnifiedParentDotenv(t *testing.T) {
	clearSyncEnv(t)
	file := filepath.Join(t.TempDir(), ".env")
	id := "11111111111111111111111111111111"
	if err := os.WriteFile(file, []byte("NOTION_SYNC_PARENT_ID="+id+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(Options{EnvFile: file})
	if err != nil || c.Notion.ParentID != FormatID(id) {
		t.Fatalf("dotenv parent = %+v, error = %v", c.Notion, err)
	}
}
func TestMetadataFormatPrecedence(t *testing.T) {
	clearSyncEnv(t)
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("NOTION_SYNC_METADATA_FORMAT=comment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{EnvFile: file, FallbackParentID: "0123456789abcdef0123456789abcdef"}
	c, err := Resolve(opts)
	if err != nil || c.Sync.MetadataFormat != "comment" {
		t.Fatalf("dotenv format = %q, err = %v", c.Sync.MetadataFormat, err)
	}
	t.Setenv("NOTION_SYNC_METADATA_FORMAT", "frontmatter")
	c, err = Resolve(opts)
	if err != nil || c.Sync.MetadataFormat != "frontmatter" {
		t.Fatalf("environment format = %q, err = %v", c.Sync.MetadataFormat, err)
	}
	format := "comment"
	opts.MetadataFormat = &format
	c, err = Resolve(opts)
	if err != nil || c.Sync.MetadataFormat != "comment" {
		t.Fatalf("option format = %q, err = %v", c.Sync.MetadataFormat, err)
	}
	format = "invalid"
	if _, err := Resolve(opts); err == nil {
		t.Fatal("invalid metadata format accepted")
	}
}
func TestResolvePrecedence(t *testing.T) {
	clearSyncEnv(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	content := "NOTION_SYNC_PARENT_ID=0123456789abcdef0123456789abcdef\nNOTION_SYNC_SOURCE=from-dotenv\nNOTION_SYNC_PRESERVE_HIERARCHY=false\nNOTION_SYNC_INCLUDE=**/*.md,**/*.MD\nNOTION_SYNC_EXCLUDE=private/**\nNOTION_API_TOKEN=not-a-real-token\n"
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
	if c.Source != "from-option" || !c.Sync.PreserveHierarchy || len(c.Sync.Include) != 1 || c.Sync.Include[0] != includes[0] || c.Sync.Exclude[0] != "private/**" || c.Notion.ParentID != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("unexpected resolved settings: %+v", c)
	}
	if os.Getenv("NOTION_API_TOKEN") == "not-a-real-token" {
		t.Fatal("dotenv was exported to process environment")
	}
}
func TestResolveEnvAndDefaults(t *testing.T) {
	clearSyncEnv(t)
	file := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(file, []byte("NOTION_SYNC_PARENT_ID=0123456789abcdef0123456789abcdef\nNOTION_SYNC_SOURCE=dotenv-dir\n"), 0600); err != nil {
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
	if err != nil || c.Notion.ParentID != "01234567-89ab-cdef-0123-456789abcdef" || c.Source != "notes" {
		t.Fatalf("fallback failed: %+v %v", c, err)
	}
	t.Setenv("NOTION_SYNC_PRESERVE_HIERARCHY", "invalid")
	if _, err := Resolve(Options{EnvFile: missing, FallbackParentID: id}); err == nil || !strings.Contains(err.Error(), "PRESERVE_HIERARCHY") {
		t.Fatalf("expected invalid bool, got %v", err)
	}
}

func TestParentEnvironmentDestination(t *testing.T) {
	page := "0123456789abcdef0123456789abcdef"
	db := "11111111111111111111111111111111"
	otherDB := "22222222222222222222222222222222"
	for _, tc := range []struct {
		name, dotenv                string
		env                         map[string]string
		flag                        *string
		fallback, wantID, wantError string
	}{
		{name: "dotenv", dotenv: "NOTION_SYNC_PARENT_ID=" + db, wantID: db},
		{name: "environment overrides dotenv", dotenv: "NOTION_SYNC_PARENT_ID=" + db, env: map[string]string{"NOTION_SYNC_PARENT_ID": otherDB}, wantID: otherDB},
		{name: "environment overrides fallback", env: map[string]string{"NOTION_SYNC_PARENT_ID": db}, fallback: page, wantID: db},
		{name: "empty environment masks dotenv", dotenv: "NOTION_SYNC_PARENT_ID=" + db, env: map[string]string{"NOTION_SYNC_PARENT_ID": ""}, wantError: "required"},
		{name: "empty environment uses fallback", dotenv: "NOTION_SYNC_PARENT_ID=" + db, env: map[string]string{"NOTION_SYNC_PARENT_ID": ""}, fallback: page, wantID: page},
		{name: "flag overrides environment", env: map[string]string{"NOTION_SYNC_PARENT_ID": db}, flag: &page, wantID: page},
		{name: "invalid parent", env: map[string]string{"NOTION_SYNC_PARENT_ID": "invalid"}, wantError: "invalid parent ID"},
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
			c, err := Resolve(Options{EnvFile: file, ParentID: tc.flag, FallbackParentID: tc.fallback})
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || c.Notion.ParentID != FormatID(tc.wantID) || c.Notion.ParentPageID != "" || c.Notion.ParentDatabaseID != "" {
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
