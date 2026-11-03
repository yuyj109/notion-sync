package sync

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMetadataKeyOrder(t *testing.T) {
	input := "notion-properties: {}\nnotion-images: {}\nnotion-target: x\nnotion-parent: x\nnotion-page-id: x\nnotion-root-id: x\nnotion-remote-hash: x\nnotion-local-hash: x\n"
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(input), &root); err != nil {
		t.Fatal(err)
	}
	orderMetadataKeys(root.Content[0])
	var got []string
	for i := 0; i < len(root.Content[0].Content); i += 2 {
		got = append(got, root.Content[0].Content[i].Value)
	}
	want := []string{"notion-local-hash", "notion-remote-hash", "notion-root-id", "notion-page-id", "notion-parent", "notion-target", "notion-images", "notion-properties"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMetadataOrderOnResync(t *testing.T) {
	for _, format := range []string{"frontmatter", "comment"} {
		t.Run(format, func(t *testing.T) {
			c, api, dir := setup(t)
			c.Sync.MetadataFormat = format
			file := filepath.Join(dir, "note.md")
			write(t, file, "---\ntitle: Keep\n---\n# Body\n")
			if err := run(c, api, true); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(mustRead(t, file)), "\n")
			local, root := -1, -1
			for i, line := range lines {
				if strings.HasPrefix(line, "notion-local-hash:") {
					local = i
				}
				if strings.HasPrefix(line, "notion-root-id:") {
					root = i
				}
			}
			if local < 0 || root < 0 || local > root {
				t.Fatalf("incorrect initial order: %s", mustRead(t, file))
			}
			lines[local], lines[root] = lines[root], lines[local]
			original := strings.Join(lines, "\n")
			write(t, file, original)
			var preview strings.Builder
			if err := Run(context.Background(), c, api, false, func(format string, args ...any) { fmt.Fprintf(&preview, format+"\n", args...) }); err != nil {
				t.Fatal(err)
			}
			if string(mustRead(t, file)) != original || !strings.Contains(preview.String(), "normalize") {
				t.Fatalf("unexpected dry-run: %s", preview.String())
			}
			if err := run(c, api, true); err != nil {
				t.Fatal(err)
			}
			result := string(mustRead(t, file))
			if strings.Index(result, "notion-local-hash:") > strings.Index(result, "notion-root-id:") || !strings.Contains(result, "title: Keep") {
				t.Fatalf("incorrect order or missing ordinary field: %s", result)
			}
			if err := run(c, api, true); err != nil || string(mustRead(t, file)) != result {
				t.Fatalf("repeated sync changed file: %v", err)
			}
		})
	}
}

func TestMetadataOrderingRejectsUnsafeYAMLAlias(t *testing.T) {
	raw := []byte("---\nnotion-root-id: &id 11111111-1111-1111-1111-111111111111\nnotion-local-hash: *id\n---\nBody\n")
	root, body, nl, err := frontmatter(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := renderMetadata(root, body, nl, "frontmatter", raw); err == nil {
		t.Fatal("reordering moved an alias before its anchor")
	}
}
