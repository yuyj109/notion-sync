package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

const DefaultEnvFile = ".env"

var ErrParentRequired = errors.New("parent page or database ID is required; use --parent-id or NOTION_SYNC_PARENT_ID")

const EnvExample = `# Set a parent page ID or database ID for sync and doctor
NOTION_SYNC_PARENT_ID=
NOTION_SYNC_PARENT_TYPE=auto
NOTION_SYNC_SOURCE=./docs
NOTION_SYNC_PRESERVE_HIERARCHY=true
NOTION_SYNC_METADATA_FORMAT=frontmatter
NOTION_SYNC_INCLUDE=**/*.md
NOTION_SYNC_EXCLUDE=**/node_modules/**
`

type Options struct {
	EnvFile           string
	EnvFileExplicit   bool
	Source            *string
	ParentID          *string
	ParentType        *string
	PreserveHierarchy *bool
	MetadataFormat    *string
	Include           *[]string
	Exclude           *[]string
	FallbackParentID  string
	FallbackSource    string
}

func Resolve(o Options) (Config, error) {
	var c Config
	cwd, err := os.Getwd()
	if err != nil {
		return c, err
	}
	c.WorkDir = cwd
	c.Source = "./docs"
	c.Sync.Direction = "push"
	c.Notion.ParentType = "auto"
	c.Sync.MetadataFormat = "frontmatter"
	c.Sync.PreserveHierarchy = true
	c.Sync.Include = []string{"**/*.md"}
	c.Sync.Exclude = []string{"**/node_modules/**"}
	envFile := o.EnvFile
	if envFile == "" {
		envFile = DefaultEnvFile
	}
	values, err := godotenv.Read(envFile)
	if err != nil {
		if !os.IsNotExist(err) || o.EnvFileExplicit {
			return c, fmt.Errorf("load env file: %w", err)
		}
		values = map[string]string{}
	}
	get := func(key string) (string, bool) {
		if value, ok := os.LookupEnv(key); ok {
			return value, true
		}
		value, ok := values[key]
		return value, ok
	}
	_, sourceSet := get("NOTION_SYNC_SOURCE")
	if !sourceSet && o.Source == nil && o.FallbackSource != "" {
		c.Source = o.FallbackSource
	}
	if v, ok := get("NOTION_SYNC_SOURCE"); ok {
		c.Source = v
	}
	if v, ok := get("NOTION_SYNC_PRESERVE_HIERARCHY"); ok && o.PreserveHierarchy == nil {
		b, e := strconv.ParseBool(v)
		if e != nil {
			return c, fmt.Errorf("invalid NOTION_SYNC_PRESERVE_HIERARCHY: %w", e)
		}
		c.Sync.PreserveHierarchy = b
	}
	if v, ok := get("NOTION_SYNC_INCLUDE"); ok {
		c.Sync.Include = splitList(v)
	}
	if v, ok := get("NOTION_SYNC_EXCLUDE"); ok {
		c.Sync.Exclude = splitList(v)
	}
	if v, ok := get("NOTION_SYNC_METADATA_FORMAT"); ok {
		c.Sync.MetadataFormat = v
	}
	if o.Source != nil {
		c.Source = *o.Source
	}
	if o.ParentID != nil {
		c.Notion.ParentID = *o.ParentID
	} else if value, ok := get("NOTION_SYNC_PARENT_ID"); ok && value != "" {
		c.Notion.ParentID = value
	} else {
		c.Notion.ParentID = o.FallbackParentID
	}
	if o.ParentType != nil {
		c.Notion.ParentType = *o.ParentType
	} else if value, ok := get("NOTION_SYNC_PARENT_TYPE"); ok && value != "" {
		c.Notion.ParentType = value
	}
	if c.Notion.ParentType != "auto" && c.Notion.ParentType != "page" && c.Notion.ParentType != "db" {
		return c, fmt.Errorf("parent type must be auto, page, or db")
	}
	if o.PreserveHierarchy != nil {
		c.Sync.PreserveHierarchy = *o.PreserveHierarchy
	}
	if o.MetadataFormat != nil {
		c.Sync.MetadataFormat = *o.MetadataFormat
	}
	if c.Sync.MetadataFormat != "frontmatter" && c.Sync.MetadataFormat != "comment" {
		return c, fmt.Errorf("metadata format must be frontmatter or comment")
	}
	if o.Include != nil {
		c.Sync.Include = *o.Include
	}
	if o.Exclude != nil {
		c.Sync.Exclude = *o.Exclude
	}
	if strings.TrimSpace(c.Source) == "" {
		return c, fmt.Errorf("source is required")
	}
	if c.Notion.ParentID == "" {
		return c, ErrParentRequired
	}
	if c.Notion.ParentID != "" && !ValidID(c.Notion.ParentID) {
		return c, fmt.Errorf("invalid parent ID")
	}
	c.Notion.ParentID = FormatID(c.Notion.ParentID)
	if len(c.Sync.Include) == 0 {
		return c, fmt.Errorf("include patterns must not be empty")
	}
	// Relative source paths are interpreted relative to the current working directory.
	if filepath.IsAbs(c.Source) {
		c.SourcePath = filepath.Clean(c.Source)
	} else {
		c.SourcePath = filepath.Join(cwd, c.Source)
	}
	return c, nil
}
func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
func InitEnv(name string) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create env file: %w", err)
	}
	if _, err = f.WriteString(EnvExample); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
