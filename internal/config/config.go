package config

import (
	"regexp"
	"strings"
)

type Config struct {
	Source string
	Notion struct{ ParentPageID, ParentDatabaseID string }
	Sync   struct {
		Direction         string
		PreserveHierarchy bool
		Delete            bool
		Include           []string
		Exclude           []string
	}
	WorkDir    string
	SourcePath string
}

var idPattern = regexp.MustCompile(`(?i)^(?:[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

func ValidID(s string) bool       { return idPattern.MatchString(s) }
func NormalizeID(s string) string { return strings.ToLower(strings.ReplaceAll(s, "-", "")) }

// FormatID returns the canonical hyphenated form of a valid Notion ID.
func FormatID(s string) string {
	if !ValidID(s) {
		return s
	}
	id := NormalizeID(s)
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}
