package sync

import (
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuyj109/notion-sync/internal/config"
)

type embedTestClient struct {
	body    map[string]any
	uploads int
}

type managedEmbedClient struct {
	markdown string
	children []any
}

func (c *managedEmbedClient) API(_ context.Context, method, endpoint string, _ any, out any) error {
	var value any
	switch {
	case strings.Contains(endpoint, "/children"):
		value = map[string]any{"results": c.children, "has_more": false}
	case strings.HasSuffix(endpoint, "/markdown"):
		value = map[string]any{"object": "page_markdown", "id": rootID, "markdown": c.markdown, "truncated": false, "unknown_block_ids": []string{}}
	default:
		value = map[string]any{"object": "page", "id": rootID, "parent": map[string]any{"type": "page_id", "page_id": rootID}}
	}
	b, _ := json.Marshal(value)
	return json.Unmarshal(b, out)
}

func (c *embedTestClient) Upload(context.Context, string, string, string) (string, error) {
	c.uploads++
	return "upload", nil
}

func (c *embedTestClient) API(_ context.Context, _, _ string, body any, out any) error {
	c.body = body.(map[string]any)
	b, _ := json.Marshal(map[string]any{"results": []any{map[string]any{"id": rootID, "type": "embed"}}})
	return json.Unmarshal(b, out)
}

func TestDrawioExportAndEmptyEmbed(t *testing.T) {
	dir := t.TempDir()
	source := `<svg xmlns="http://www.w3.org/2000/svg" content="&lt;mxfile&gt;&lt;diagram&gt;&lt;mxGraphModel&gt;&lt;root/&gt;&lt;/mxGraphModel&gt;&lt;/diagram&gt;&lt;/mxfile&gt;"><rect/></svg>`
	name := "diagram.drawio.svg"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	image := Image{Name: name, Path: name, Hash: hash(source), ContentType: "image/svg+xml", Key: "image-1"}
	action := Action{File: File{Images: []Image{image}}}
	var cfg config.Config
	cfg.SourcePath, cfg.WorkDir, cfg.Sync.DrawioEmbed = dir, dir, true
	if err := exportDrawioURL(cfg, Plan{Actions: []Action{action}}, true, nil); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, ".notion-sync", name+".txt")
	html, err := os.ReadFile(output)
	if err != nil || !strings.HasPrefix(string(html), "https://viewer.diagrams.net/") || !strings.Contains(string(html), "%3Cmxfile%3E") {
		t.Fatalf("unexpected HTML: %s, %v", html, err)
	}
	client := &embedTestClient{}
	blocks, err := appendImages(context.Background(), client, rootID, action, cfg)
	if err != nil || client.uploads != 0 || blocks["image-1"] != rootID {
		t.Fatalf("embed: blocks=%v uploads=%d err=%v", blocks, client.uploads, err)
	}
	child := client.body["children"].([]any)[0].(map[string]any)
	if child["type"] != "embed" || child["embed"].(map[string]any)["url"] != "" {
		t.Fatalf("unexpected block: %v", child)
	}
	if err := os.WriteFile(output, []byte("user content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := exportDrawioURL(cfg, Plan{Actions: []Action{action}}, true, nil); err == nil {
		t.Fatal("expected collision error")
	}
	got, _ := os.ReadFile(output)
	if string(got) != "user content" {
		t.Fatal("overwrote user content")
	}
}

func TestDrawioMissingMetadata(t *testing.T) {
	if _, err := drawioViewerURL([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "diagram.drawio.svg"); err == nil {
		t.Fatal("expected error for missing editable diagram")
	}
}

func TestDrawioViewerUsesCompressedModel(t *testing.T) {
	model := `<mxGraphModel><root><mxCell id="0" value="A &amp; B"/></root></mxGraphModel>`
	input := `<mxfile host="app.diagrams.net"><diagram name="図" id="p1">` + model + `</diagram></mxfile>`
	encoded, err := encodeDrawioXML(input)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Diagrams []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"id,attr"`
			Data string `xml:",chardata"`
		} `xml:"diagram"`
	}
	if err := xml.Unmarshal([]byte(encoded), &output); err != nil || len(output.Diagrams) != 1 {
		t.Fatalf("invalid output XML: %v", err)
	}
	page := output.Diagrams[0]
	if page.Name != "図" || page.ID != "p1" {
		t.Fatalf("lost diagram identity: %+v", page)
	}
	data, err := base64.StdEncoding.DecodeString(page.Data)
	if err != nil {
		t.Fatal(err)
	}
	r := flate.NewReader(strings.NewReader(string(data)))
	defer r.Close()
	compressed, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := url.QueryUnescape(string(compressed))
	if err != nil || decoded != model {
		t.Fatalf("model did not round-trip: %q, %v", decoded, err)
	}
}

func TestDrawioExportKeepsLongURL(t *testing.T) {
	dir := t.TempDir()
	source := `<svg xmlns="http://www.w3.org/2000/svg" content="&lt;mxfile&gt;&lt;diagram&gt;` + strings.Repeat("a", 2200) + `&lt;/diagram&gt;&lt;/mxfile&gt;"/>`
	name := "large.drawio.svg"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	image := Image{Name: name, Path: name, Hash: hash(source)}
	var cfg config.Config
	cfg.SourcePath, cfg.WorkDir = dir, dir
	var warning string
	err := exportDrawioURL(cfg, Plan{Actions: []Action{{File: File{Images: []Image{image}}}}}, true, func(format string, args ...any) { warning = fmt.Sprintf(format, args...) })
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".notion-sync", name+".txt"))
	if err != nil || len(data) <= 2000 {
		t.Fatalf("long URL not exported: length=%d err=%v", len(data), err)
	}
	if !strings.Contains(warning, "Notion may reject") {
		t.Fatalf("missing length warning: %q", warning)
	}
}

func TestDrawioModeSwitchPlansPageUpdate(t *testing.T) {
	cfg, api, dir := setup(t)
	write(t, filepath.Join(dir, "diagram.drawio.svg"), `<svg xmlns="http://www.w3.org/2000/svg" content="&lt;mxfile/&gt;"/>`)
	write(t, filepath.Join(dir, "guide.md"), "# Guide\n![Diagram](diagram.drawio.svg)\n")
	files, err := Scan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state.Active["guide.md"] = true
	plan, err := Build(context.Background(), cfg, api, files, state)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), cfg, &fakeUploader{fakeAPI: api}, &state, plan, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	files, err = Scan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state, err = loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sync.DrawioEmbed = true
	plan, err = Build(context.Background(), cfg, api, files, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != "update" {
		t.Fatalf("expected page update when switching mode: %+v", plan.Actions)
	}
}

func TestManagedEmbedURLIsIgnoredButOtherEditsAreNot(t *testing.T) {
	managedID := "11111111-1111-1111-1111-111111111111"
	otherID := "22222222-2222-2222-2222-222222222222"
	baseline := "before\n<embed src=\"\"></embed>\n<embed src=\"https://other.example\"></embed>"
	client := &managedEmbedClient{markdown: strings.Replace(baseline, `src=""`, `src="https://viewer.diagrams.net/diagram"`, 1), children: []any{map[string]any{"id": managedID, "type": "embed"}, map[string]any{"id": otherID, "type": "embed"}}}
	entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", RemoteHash: remoteMarkdownHash(baseline), managedEmbeds: map[string]bool{norm(managedID): true}}
	if _, err := validateExisting(context.Background(), client, entry); err != nil {
		t.Fatalf("managed URL should not conflict: %v", err)
	}
	client.markdown += "\n<empty-block/>"
	if _, err := validateExisting(context.Background(), client, entry); err != nil {
		t.Fatalf("trailing empty Notion block should not cause a conflict: %v", err)
	}
	client.markdown = strings.TrimSuffix(client.markdown, "\n<empty-block/>")
	client.markdown = strings.Replace(client.markdown, "https://other.example", "https://changed.example", 1)
	if _, err := validateExisting(context.Background(), client, entry); err == nil {
		t.Fatal("unmanaged embed change should conflict")
	}
	client.markdown = strings.Replace(client.markdown, "before", "changed", 1)
	if _, err := validateExisting(context.Background(), client, entry); err == nil {
		t.Fatal("text change should conflict")
	}
}

func TestDrawioConflictDiffHidesManagedEmbed(t *testing.T) {
	managedID := "11111111-1111-1111-1111-111111111111"
	remote := "before\n<embed src=\"https://viewer.diagrams.net/large-secret\"></embed>\nold text"
	client := &managedEmbedClient{children: []any{map[string]any{"id": managedID, "type": "embed"}}}
	image := Image{Key: "image-1", SourceLine: "![diagram](images/d.drawio.svg)"}
	entry := Entry{PageID: rootID, ImageBlocks: map[string]string{"image-1": managedID}, managedEmbeds: map[string]bool{norm(managedID): true}}
	local := "before\n![diagram](images/d.drawio.svg)\nnew text"
	prepared, err := drawioDiffMarkdown(context.Background(), client, entry, File{Images: []Image{image}}, remote)
	if err != nil {
		t.Fatal(err)
	}
	diff := makeContentDiff("page.md", prepared, local, 0, 0).Plain
	if strings.Contains(diff, "large-secret") || strings.Contains(diff, "<embed") || strings.Contains(diff, "+![diagram]") || !strings.Contains(diff, "-old text") || !strings.Contains(diff, "+new text") {
		t.Fatalf("unexpected diff: %s", diff)
	}
}

func TestAppendedTextPreservesExistingDrawioEmbed(t *testing.T) {
	imageID := "11111111-1111-1111-1111-111111111111"
	embedID := "22222222-2222-2222-2222-222222222222"
	images := []Image{
		{Key: "image-1", Name: "photo.svg", Hash: "photo-hash", SourceLine: "![photo](photo.svg)"},
		{Key: "image-2", Name: "diagram.drawio.svg", Hash: "diagram-hash", StateHash: "embed-hash", SourceLine: "![diagram](diagram.drawio.svg)"},
	}
	entry := Entry{PageID: rootID, ImageBlocks: map[string]string{"image-1": imageID, "image-2": embedID}, ImageHashes: map[string]string{"image-1": "photo-hash", "image-2": "embed-hash"}, managedEmbeds: map[string]bool{norm(embedID): true}}
	client := &managedEmbedClient{children: []any{map[string]any{"id": imageID, "type": "image"}, map[string]any{"id": embedID, "type": "embed"}}}
	remote := "# Guide\n![photo](https://files.example/photo.svg?sig=x#notion_record=block." + imageID + ".space)\n<embed src=\"https://viewer.example\"></embed>\n<empty-block/>"
	f := File{Images: images, SyncBody: "# Guide\n![photo](photo.svg)\n![diagram](diagram.drawio.svg)\n\n.\ntest\n"}
	added, err := appendedDrawioText(context.Background(), client, entry, f, remote)
	if err != nil || added != ".\ntest" {
		t.Fatalf("expected appended paragraphs, got %q, %v", added, err)
	}
	f.SyncBody = strings.Replace(f.SyncBody, "# Guide", "# Changed", 1)
	added, err = appendedDrawioText(context.Background(), client, entry, f, remote)
	if err != nil || added != "" {
		t.Fatalf("must not append when earlier text changed: %q, %v", added, err)
	}
	f.SyncBody = strings.Replace(f.SyncBody, "# Changed", "# Guide", 1)
	f.Images[1].StateHash = "changed"
	added, err = appendedDrawioText(context.Background(), client, entry, f, remote)
	if err != nil || added != "" {
		t.Fatalf("must not append when the diagram changed: %q, %v", added, err)
	}
}

func TestPageUpdateProtectsFilledManagedEmbed(t *testing.T) {
	managedID := "11111111-1111-1111-1111-111111111111"
	client := &managedEmbedClient{markdown: `<embed src="https://example.com"></embed>`, children: []any{map[string]any{"id": managedID, "type": "embed"}}}
	entry := Entry{PageID: rootID, managedEmbeds: map[string]bool{norm(managedID): true}}
	var cfg config.Config
	cfg.WorkDir = t.TempDir()
	cfg.Sync.DrawioEmbed = true
	state := &State{}
	image := Image{Name: "diagram.drawio.svg"}
	var warnings []string
	err := Apply(context.Background(), cfg, client, state, Plan{Actions: []Action{{Kind: "update", Key: "file:page.md", Path: "page.md", Entry: entry, File: File{Images: []Image{image}}}}}, func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) })
	if err != nil || !state.Skipped["page.md"] {
		t.Fatalf("expected protected page to be skipped, got %v", err)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "page not synchronized") || !strings.Contains(joined, filepath.Join(cfg.WorkDir, ".notion-sync", "diagram.drawio.svg.txt")) {
		t.Fatalf("missing manual instructions: %s", joined)
	}
}
