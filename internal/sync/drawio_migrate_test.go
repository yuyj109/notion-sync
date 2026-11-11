package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuyj109/notion-sync/internal/config"
)

type migratingDrawioClient struct {
	*fakeAPI
	pageID           string
	block            drawioEmbedBlock
	uploads, patches int
	onUpload         func()
	failure          string
}

func (c *migratingDrawioClient) Upload(_ context.Context, name, contentType, filename string) (string, error) {
	c.uploads++
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(name, ".svg.html") || contentType != "text/html" || !strings.HasPrefix(string(data), "<!doctype html>") {
		return "", fmt.Errorf("unexpected upload")
	}
	if c.onUpload != nil {
		c.onUpload()
	}
	if c.failure == "upload" {
		return "", fmt.Errorf("lost upload response")
	}
	return rootID, nil
}

func (c *migratingDrawioClient) API(ctx context.Context, method, endpoint string, body any, out any) error {
	copyResult := func(value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	if method == "GET" && strings.HasSuffix(endpoint, "/children?page_size=100") {
		return copyResult(map[string]any{"results": []any{map[string]any{"id": c.block.ID, "type": "embed"}}, "has_more": false})
	}
	if method == "PATCH" && strings.HasSuffix(endpoint, "/markdown") && body.(map[string]any)["type"] == "update_content" {
		c.updates++
		p := c.pages[norm(c.pageID)]
		for _, item := range body.(map[string]any)["update_content"].(map[string]any)["content_updates"].([]any) {
			update := item.(map[string]any)
			old := update["old_str"].(string)
			if strings.Count(p.content, old) != 1 {
				return fmt.Errorf("ambiguous text update")
			}
			p.content = strings.Replace(p.content, old, update["new_str"].(string), 1)
		}
		return copyResult(map[string]any{"object": "page_markdown", "id": c.pageID, "markdown": p.content, "truncated": false, "unknown_block_ids": []string{}})
	}
	if endpoint == "v1/blocks/"+c.block.ID {
		if method == "PATCH" {
			c.patches++
			embed := body.(map[string]any)["embed"].(map[string]any)
			if len(embed) != 1 || embed["file_upload"].(map[string]any)["id"] != rootID {
				return fmt.Errorf("unexpected embed update")
			}
			old := c.block.Embed.URL
			c.block.Embed.URL = "https://files.example/diagram.svg.html?token=new"
			c.block.LastEditedTime = "updated"
			if c.failure != "stale markdown" {
				p := c.pages[norm(c.pageID)]
				p.content = strings.Replace(p.content, `src="`+old+`"`, `src="`+c.block.Embed.URL+`"`, 1)
			}
			if c.failure == "other content" {
				c.pages[norm(c.pageID)].content += "\nconcurrent edit"
			}
			if c.failure == "patch" {
				return fmt.Errorf("lost update response")
			}
		}
		return copyResult(c.block)
	}
	return c.fakeAPI.API(ctx, method, endpoint, body, out)
}

func setupDrawioMigration(t *testing.T, legacyURL string) (config.Config, *migratingDrawioClient, string) {
	t.Helper()
	cfg, base, dir := setup(t)
	cfg.Sync.DrawioEmbed = true
	filename := filepath.Join(dir, "home.md")
	write(t, filename, "# Home\n\n![diagram](diagram.drawio.svg)\n\nTail\n")
	write(t, filepath.Join(dir, "diagram.drawio.svg"), `<svg content="&lt;mxGraphModel&gt;&lt;root/&gt;&lt;/mxGraphModel&gt;"/>`)
	s, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	id, blockID := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	baseline := "# Home\n<embed src=\"\"></embed>\nTail\n"
	base.pages[norm(id)] = &fakePage{parent: rootID, parentType: "page_id", content: strings.Replace(baseline, `src=""`, `src="`+legacyURL+`"`, 1), locked: true}
	image := s.Documents["home.md"].Images[0]
	s.Entries["file:home.md"] = Entry{PageID: id, ParentID: rootID, ParentType: "page_id", Target: "hierarchy:", LocalHash: s.Documents["home.md"].Hash, RemoteHash: remoteMarkdownHash(baseline), ImageBlocks: map[string]string{"image-1": blockID}, ImageHashes: map[string]string{"image-1": hash("drawio-embed:" + image.Hash)}}
	s.Active["home.md"] = true
	if err := saveState(cfg, s); err != nil {
		t.Fatal(err)
	}
	client := &migratingDrawioClient{fakeAPI: base, pageID: id}
	client.block.Object, client.block.ID, client.block.Type, client.block.LastEditedTime = "block", blockID, "embed", "before"
	client.block.Embed.URL = legacyURL
	client.block.Embed.Caption = []any{map[string]any{"type": "text", "text": map[string]any{"content": "keep caption"}}}
	return cfg, client, filename
}

func TestSyncAndPageSyncMigrateLegacyDrawioInPlace(t *testing.T) {
	for _, single := range []bool{false, true} {
		for _, legacy := range []string{"", "https://viewer.diagrams.net/?layers=1#Rdiagram"} {
			t.Run(fmt.Sprintf("single=%v/empty=%v", single, legacy == ""), func(t *testing.T) {
				cfg, client, filename := setupDrawioMigration(t, legacy)
				var log strings.Builder
				print := func(format string, args ...any) { fmt.Fprintf(&log, format+"\n", args...) }
				run := func(apply bool) error {
					if single {
						return RunFile(context.Background(), cfg, client, filename, apply, print)
					}
					return Run(context.Background(), cfg, client, apply, print)
				}
				original := mustRead(t, filename)
				if err := run(false); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(log.String(), "legacy URL embeds migrated") || client.uploads != 0 || client.patches != 0 || string(mustRead(t, filename)) != string(original) {
					t.Fatal("dry-run must preview migration without writes")
				}
				output := filepath.Join(filepath.Dir(testSharedStatePath(t, cfg, rootID)), "diagram.drawio.svg.html")
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatalf("dry-run created HTML: %v", err)
				}
				if err := run(true); err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(string(mustRead(t, output)), "<!doctype html>") {
					t.Fatal("migration did not export HTML")
				}
				if client.uploads != 1 || client.patches != 1 || client.updates != 0 || client.creates != 0 || len(client.imageBlocks) != 0 {
					t.Fatal("migration must update only the existing block")
				}
				s, err := loadState(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if s.Entries["file:home.md"].ImageBlocks["image-1"] != client.block.ID {
					t.Fatal("block ID changed")
				}
				if err := run(true); err != nil {
					t.Fatal(err)
				}
				if client.uploads != 1 || client.patches != 1 || strings.Contains(log.String(), "token=") || strings.Contains(log.String(), "#Rdiagram") {
					t.Fatal("rerun uploaded again or exposed embed data")
				}
			})
		}
	}
}

func TestDrawioMigrationStopsOnFailuresAndConcurrentChanges(t *testing.T) {
	for _, mode := range []string{"upload", "patch", "stale markdown", "other content", "block changed", "source changed", "remote changed"} {
		t.Run(mode, func(t *testing.T) {
			cfg, client, filename := setupDrawioMigration(t, "https://viewer.diagrams.net/#Rdiagram")
			client.failure = mode
			client.onUpload = func() {
				switch mode {
				case "block changed":
					client.block.LastEditedTime = "concurrent"
				case "source changed":
					write(t, filepath.Join(filepath.Dir(filename), "diagram.drawio.svg"), "changed")
				case "remote changed":
					client.pages[norm(client.pageID)].content += "\nconcurrent edit"
				}
			}
			err := RunFile(context.Background(), cfg, client, filename, true, func(string, ...any) {})
			if err == nil || client.uploads != 1 || client.patches > 1 || client.updates != 0 {
				t.Fatalf("failure must stop without retry or page replacement: %v", err)
			}
			if (mode == "upload" || strings.HasSuffix(mode, "changed")) && client.patches != 0 {
				t.Fatal("unsafe block update")
			}
			if mode == "patch" {
				// A lost response must not cause another upload when the first
				// update actually attached the HTML successfully.
				if err := RunFile(context.Background(), cfg, client, filename, true, func(string, ...any) {}); err != nil {
					t.Fatal(err)
				}
				if client.uploads != 1 || client.patches != 1 {
					t.Fatal("uncertain successful update was retried")
				}
			}
		})
	}
}

func TestDrawioMigrationPreservesOtherURLsAndHTML(t *testing.T) {
	for _, existing := range []string{"https://example.com/custom-viewer", "https://files.example/existing.html?token=old"} {
		t.Run(existing, func(t *testing.T) {
			cfg, client, filename := setupDrawioMigration(t, existing)
			if err := RunFile(context.Background(), cfg, client, filename, true, func(string, ...any) {}); err != nil {
				t.Fatal(err)
			}
			if client.uploads != 0 || client.patches != 0 || client.block.Embed.URL != existing {
				t.Fatal("existing non-legacy embed was modified")
			}
		})
	}
}

func TestDrawioMigrationWithTextChanges(t *testing.T) {
	cfg, client, filename := setupDrawioMigration(t, "https://viewer.diagrams.net/#Rdiagram")
	write(t, filename, strings.Replace(string(mustRead(t, filename)), "# Home", "# New", 1))
	if err := RunFile(context.Background(), cfg, client, filename, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if client.uploads != 1 || client.patches != 1 || client.updates != 1 || !strings.Contains(client.pages[norm(client.pageID)].content, "# New") {
		t.Fatal("text and HTML were not updated in place")
	}
}

func TestDrawioMigrationOutputCollisionStopsBeforeWrites(t *testing.T) {
	cfg, client, filename := setupDrawioMigration(t, "https://viewer.diagrams.net/#Rdiagram")
	output := filepath.Join(filepath.Dir(testSharedStatePath(t, cfg, rootID)), "diagram.drawio.svg.html")
	write(t, output, "user content")
	for _, apply := range []bool{false, true} {
		err := RunFile(context.Background(), cfg, client, filename, apply, func(string, ...any) {})
		if err == nil || !strings.Contains(err.Error(), "already exists with different content") || client.uploads != 0 || client.patches != 0 || string(mustRead(t, output)) != "user content" {
			t.Fatalf("collision must stop before migration without overwriting: %v", err)
		}
	}
}
