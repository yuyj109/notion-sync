package sync

import (
	"path/filepath"
	"testing"

	"github.com/yuyj109/notion-sync/internal/config"
)

func TestScanUnicodeIncludeExclude(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "資料", "公開.md"), "public")
	write(t, filepath.Join(root, "資料", "非公開.md"), "private")
	write(t, filepath.Join(root, "other.md"), "other")
	var c config.Config
	c.SourcePath = root
	c.Sync.Include = []string{"資料/**/*.md"}
	c.Sync.Exclude = []string{"資料/**/非公開.md"}
	files, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "資料/公開.md" {
		t.Fatalf("unexpected matched files: %+v", files)
	}
}

func TestExcludedTree(t *testing.T) {
	for _, tc := range []struct {
		pattern, dir string
		want         bool
	}{
		{"**/node_modules/**", "node_modules", true},
		{"**/node_modules/**", "docs/node_modules", true},
		{"docs/*.md", "docs", false},
		{"docs/**", "other", false},
	} {
		got, err := excludedTree([]string{tc.pattern}, tc.dir)
		if err != nil || got != tc.want {
			t.Errorf("excludedTree(%q, %q) = %v, %v", tc.pattern, tc.dir, got, err)
		}
	}
}
