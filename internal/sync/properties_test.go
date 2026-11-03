package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yuyj109/notion-sync/internal/config"
)

type propertyClient struct {
	pageID, sourceID string
	description      string
	status           string
	patches          int
}

func TestPropertyInputTypes(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		value any
		want  any
	}{
		{"rich_text", "Hello World", "Hello World"},
		{"number", "12.5", float64(12.5)},
		{"checkbox", "true", true},
		{"date", "2026-11-03", "2026-11-03"},
		{"multi_select", "Notion,Go", []string{"Go", "Notion"}},
		{"multi_select", "", []string{}},
	} {
		_, got, err := propertyInput(tc.kind, tc.value)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %#v, %v", tc.kind, got, err)
		}
	}
	for _, kind := range []string{"title", "formula", "relation"} {
		if _, _, err := propertyInput(kind, "value"); err == nil {
			t.Errorf("%s should be rejected", kind)
		}
	}
}

func (c *propertyClient) API(_ context.Context, method, endpoint string, body, out any) error {
	var result any
	switch {
	case method == "GET" && strings.HasPrefix(endpoint, "v1/data_sources/"):
		result = map[string]any{"object": "data_source", "id": c.sourceID, "properties": map[string]any{"Description": map[string]any{"type": "rich_text"}, "Status": map[string]any{"type": "status", "status": map[string]any{"options": []any{map[string]any{"name": "Done"}, map[string]any{"name": "Todo"}}}}}}
	case method == "GET" && strings.HasPrefix(endpoint, "v1/pages/"):
		result = c.page()
	case method == "PATCH" && strings.HasPrefix(endpoint, "v1/pages/"):
		c.patches++
		properties := body.(map[string]any)["properties"].(map[string]any)
		if v, ok := properties["Description"]; ok {
			c.description = v.(map[string]any)["rich_text"].([]any)[0].(map[string]any)["text"].(map[string]any)["content"].(string)
		}
		if v, ok := properties["Status"]; ok {
			c.status = v.(map[string]any)["status"].(map[string]any)["name"].(string)
		}
		result = c.page()
	default:
		return fmt.Errorf("unexpected request %s %s", method, endpoint)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (c *propertyClient) page() map[string]any {
	return map[string]any{"object": "page", "id": c.pageID, "parent": map[string]any{"type": "data_source_id", "data_source_id": c.sourceID}, "properties": map[string]any{
		"Description": map[string]any{"type": "rich_text", "rich_text": []any{map[string]any{"plain_text": c.description}}},
		"Status":      map[string]any{"type": "status", "status": map[string]any{"name": c.status}},
	}}
}

func TestPropertyPreviewAndApplyPreservesValuesWithSpaces(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "note.md")
	original := "---\ntitle: Keep\nnotion-properties:\n  Status: Todo\n---\nBody\n"
	if err := os.WriteFile(name, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := parse([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	f.Path = "note.md"
	id := "11111111-1111-1111-1111-111111111111"
	ds := "22222222-2222-2222-2222-222222222222"
	client := &propertyClient{pageID: id, sourceID: ds, description: "Old", status: "Todo"}
	var c config.Config
	c.SourcePath = dir
	c.Sync.Properties = map[string]string{"Status": "Done", "Description": "Hello World = yes"}
	p := Plan{Actions: []Action{{Key: "file:note.md", Path: "note.md", File: f, Entry: Entry{PageID: id, ParentID: ds, ParentType: "data_source_id"}}}}
	if err := planProperties(context.Background(), c, client, &p, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(p.Actions[0].propertyPlan.write) != 2 || client.patches != 0 || string(mustRead(t, name)) != original {
		t.Fatal("preview changed files or did not plan properties")
	}
	s := State{Documents: map[string]*File{"note.md": &f}}
	if err := applyProperties(context.Background(), c, client, &s, p.Actions[0], func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if client.description != "Hello World = yes" || client.status != "Done" || client.patches != 1 {
		t.Fatalf("unexpected properties: %+v", client)
	}
	updated := mustRead(t, name)
	parsed, err := parse(updated)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Properties["Description"] != "Hello World = yes" || parsed.Properties["Status"] != "Done" || !strings.Contains(string(updated), "title: Keep") || parsed.Body != "Body\n" {
		t.Fatalf("unexpected frontmatter: %s", updated)
	}
	p.Actions[0].File = parsed
	if err := planProperties(context.Background(), c, client, &p, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(p.Actions[0].propertyPlan.write) != 0 || len(p.Actions[0].propertyPlan.local) != 0 {
		t.Fatal("unchanged properties planned again")
	}
}

func TestPropertyConcurrentEditIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "note.md")
	data := []byte("---\nnotion-properties:\n  Description: New\n---\nBody\n")
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := parse(data)
	if err != nil {
		t.Fatal(err)
	}
	f.Path = "note.md"
	id := "11111111-1111-1111-1111-111111111111"
	ds := "22222222-2222-2222-2222-222222222222"
	client := &propertyClient{pageID: id, sourceID: ds, description: "Old", status: "Todo"}
	var c config.Config
	c.SourcePath = dir
	p := Plan{Actions: []Action{{Key: "file:note.md", Path: "note.md", File: f, Entry: Entry{PageID: id, ParentID: ds, ParentType: "data_source_id"}}}}
	if err := planProperties(context.Background(), c, client, &p, nil, nil); err != nil {
		t.Fatal(err)
	}
	client.description = "Concurrent"
	s := State{Documents: map[string]*File{"note.md": &f}}
	if err := applyProperties(context.Background(), c, client, &s, p.Actions[0], func(string, ...any) {}); err == nil || client.patches != 0 {
		t.Fatalf("concurrent change overwritten: %v", err)
	}
}

type propertySyncClient struct {
	*fakeAPI
	db, ds string
	values map[string]map[string]json.RawMessage
}

func (c *propertySyncClient) API(ctx context.Context, method, endpoint string, body, out any) error {
	encode := func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	if method == "GET" && strings.HasPrefix(endpoint, "v1/databases/") {
		return encode(map[string]any{"object": "database", "id": c.db, "data_sources": []any{map[string]any{"id": c.ds}}})
	}
	if method == "GET" && strings.HasPrefix(endpoint, "v1/data_sources/") {
		return encode(map[string]any{"object": "data_source", "id": c.ds, "properties": map[string]any{"Name": map[string]any{"type": "title"}, "Description": map[string]any{"type": "rich_text"}, "Status": map[string]any{"type": "status", "status": map[string]any{"options": []any{map[string]any{"name": "Done"}}}}}})
	}
	if method == "POST" && endpoint == "v1/pages" {
		original := body.(map[string]any)
		copyBody := make(map[string]any, len(original))
		maps.Copy(copyBody, original)
		copyBody["properties"] = map[string]any{"Name": original["properties"].(map[string]any)["Name"]}
		if err := c.fakeAPI.API(ctx, method, endpoint, copyBody, out); err != nil {
			return err
		}
		id := out.(*page).ID
		c.values[norm(id)] = map[string]json.RawMessage{}
		for name, v := range body.(map[string]any)["properties"].(map[string]any) {
			if name != "Name" {
				c.set(id, name, v)
			}
		}
		return nil
	}
	if method == "GET" && strings.HasPrefix(endpoint, "v1/pages/") && !strings.HasSuffix(endpoint, "/markdown") {
		if err := c.fakeAPI.API(ctx, method, endpoint, body, out); err != nil {
			return err
		}
		p := out.(*page)
		p.Properties = c.values[norm(p.ID)]
		return nil
	}
	if method == "PATCH" && strings.HasPrefix(endpoint, "v1/pages/") && !strings.HasSuffix(endpoint, "/markdown") {
		if m, ok := body.(map[string]any)["properties"].(map[string]any); ok {
			id := strings.TrimPrefix(endpoint, "v1/pages/")
			for name, value := range m {
				c.set(id, name, value)
			}
			return encode(map[string]any{"object": "page", "id": id, "properties": c.values[norm(id)]})
		}
	}
	return c.fakeAPI.API(ctx, method, endpoint, body, out)
}

func (c *propertySyncClient) set(id, name string, value any) {
	fields := value.(map[string]any)
	var result map[string]any
	if v, ok := fields["rich_text"]; ok {
		text := ""
		a := v.([]any)
		if len(a) > 0 {
			text = a[0].(map[string]any)["text"].(map[string]any)["content"].(string)
		}
		result = map[string]any{"type": "rich_text", "rich_text": []any{map[string]any{"plain_text": text}}}
	} else {
		result = map[string]any{"type": "status", "status": fields["status"]}
	}
	data, _ := json.Marshal(result)
	c.values[norm(id)][name] = data
}

func TestSyncNewDatabasePageWritesPropertiesAndFrontmatter(t *testing.T) {
	c, base, dir := setup(t)
	c.Notion.ParentDatabaseID = "11111111-1111-1111-1111-111111111111"
	c.Notion.ParentPageID = ""
	c.Sync.MetadataFormat = ""
	c.Sync.Properties = map[string]string{"Status": "Done", "Description": "Hello World"}
	ds := "22222222-2222-2222-2222-222222222222"
	client := &propertySyncClient{fakeAPI: base, db: c.Notion.ParentDatabaseID, ds: ds, values: map[string]map[string]json.RawMessage{}}
	file := filepath.Join(dir, "note.md")
	write(t, file, "# Hello\n")
	if err := Run(context.Background(), c, client, false, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, file)) != "# Hello\n" || base.creates != 0 {
		t.Fatal("dry-run changed data")
	}
	if err := Run(context.Background(), c, client, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	parsed, err := parse(mustRead(t, file))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Properties["Description"] != "Hello World" || parsed.Properties["Status"] != "Done" || parsed.Metadata == nil {
		t.Fatalf("unexpected result: %+v", parsed)
	}
	if !strings.HasPrefix(string(mustRead(t, file)), "<!-- notion-sync\n") || parsed.PropertiesFormat != "comment" {
		t.Fatalf("properties not saved in comment: %s", mustRead(t, file))
	}
	if len(client.values[norm(parsed.PageID)]) != 2 {
		t.Fatalf("page properties: %v", client.values)
	}
	before := string(mustRead(t, file))
	if err := Run(context.Background(), c, client, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, file)) != before {
		t.Fatal("repeated sync changed metadata")
	}
}

func TestPropertyFormatConversion(t *testing.T) {
	for _, initial := range []string{"frontmatter", "comment"} {
		t.Run(initial, func(t *testing.T) {
			c, base, dir := setup(t)
			c.Notion.ParentDatabaseID, c.Notion.ParentPageID = "11111111-1111-1111-1111-111111111111", ""
			c.Sync.MetadataFormat = initial
			c.Sync.Properties = map[string]string{"Status": "Done"}
			client := &propertySyncClient{fakeAPI: base, db: c.Notion.ParentDatabaseID, ds: "22222222-2222-2222-2222-222222222222", values: map[string]map[string]json.RawMessage{}}
			file := filepath.Join(dir, "note.md")
			write(t, file, "---\ntitle: Keep\n---\n# Hello\n")
			if err := Run(context.Background(), c, client, true, func(string, ...any) {}); err != nil {
				t.Fatal(err)
			}
			before := string(mustRead(t, file))
			if initial == "comment" {
				c.Sync.MetadataFormat = "frontmatter"
			} else {
				c.Sync.MetadataFormat = "comment"
			}
			var preview strings.Builder
			if err := Run(context.Background(), c, client, false, func(format string, args ...any) { fmt.Fprintf(&preview, format+"\n", args...) }); err != nil {
				t.Fatal(err)
			}
			if string(mustRead(t, file)) != before || !strings.Contains(preview.String(), "move local properties") {
				t.Fatalf("invalid preview: %s", preview.String())
			}
			if err := Run(context.Background(), c, client, true, func(string, ...any) {}); err != nil {
				t.Fatal(err)
			}
			got, err := parse(mustRead(t, file))
			if err != nil || got.PropertiesFormat != c.Sync.MetadataFormat || got.Properties["Status"] != "Done" || got.Body != "# Hello\n" {
				t.Fatalf("conversion: %+v %v", got, err)
			}
		})
	}
}

func TestPropertyCommentDuplicateAndUnsafeValue(t *testing.T) {
	if _, err := parse([]byte("---\nnotion-properties: {Status: Done}\n---\n<!-- notion-sync\nnotion-properties: {Status: Todo}\n-->\nBody\n")); err == nil {
		t.Fatal("duplicate properties accepted")
	}
	if _, err := renderPropertyMetadata([]byte("Body\n"), map[string]string{"Description": "unsafe --> content"}, "comment"); err == nil {
		t.Fatal("comment terminator accepted")
	}
}

func TestLegacySplitPropertyLocationIsMigrated(t *testing.T) {
	c, base, dir := setup(t)
	c.Notion.ParentDatabaseID, c.Notion.ParentPageID = "11111111-1111-1111-1111-111111111111", ""
	c.Sync.MetadataFormat = "comment"
	c.Sync.Properties = map[string]string{"Status": "Done"}
	client := &propertySyncClient{fakeAPI: base, db: c.Notion.ParentDatabaseID, ds: "22222222-2222-2222-2222-222222222222", values: map[string]map[string]json.RawMessage{}}
	file := filepath.Join(dir, "note.md")
	write(t, file, "---\ntitle: Keep\n---\n# Hello\n")
	if err := Run(context.Background(), c, client, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	original := string(mustRead(t, file))
	legacy := strings.Replace(original, "notion-properties:\n  Status: Done\n", "", 1)
	if legacy == original {
		t.Fatalf("missing properties: %s", original)
	}
	legacy = strings.Replace(legacy, "title: Keep\n", "title: Keep\nnotion-properties:\n  Status: Done\n", 1)
	write(t, file, legacy)
	var preview strings.Builder
	if err := Run(context.Background(), c, client, false, func(format string, args ...any) { fmt.Fprintf(&preview, format+"\n", args...) }); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, file)) != legacy || !strings.Contains(preview.String(), "move local properties") {
		t.Fatalf("legacy migration preview: %s", preview.String())
	}
	if err := Run(context.Background(), c, client, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	got, err := parse(mustRead(t, file))
	if err != nil || got.PropertiesFormat != "comment" || got.Properties["Status"] != "Done" {
		t.Fatalf("legacy migration: %+v %v", got, err)
	}
}

func TestResetPreservesCommentProperties(t *testing.T) {
	file := filepath.Join(t.TempDir(), "note.md")
	write(t, file, "<!-- notion-sync\nnotion-page-id: 11111111-1111-1111-1111-111111111111\nnotion-properties:\n  Status: Done\n-->\nBody\n")
	if err := Reset(file, false, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	got, err := parse(mustRead(t, file))
	if err != nil || got.PropertiesFormat != "comment" || got.Properties["Status"] != "Done" || got.PageID != "" || got.Body != "Body\n" {
		t.Fatalf("reset: %+v %v", got, err)
	}
}
