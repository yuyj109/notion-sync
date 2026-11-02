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

type updatingEmbedClient struct {
	*managedEmbedClient
	patches  int
	ranges   int
	warnings []jsonWarning
}

type embedAndPagesClient struct {
	*fakeAPI
	embedID string
}

func (c *embedAndPagesClient) API(ctx context.Context, method, endpoint string, body any, out any) error {
	if method == "GET" && strings.Contains(endpoint, "/children") {
		data, _ := json.Marshal(map[string]any{"results": []any{map[string]any{"id": c.embedID, "type": "embed"}}, "has_more": false})
		return json.Unmarshal(data, out)
	}
	return c.fakeAPI.API(ctx, method, endpoint, body, out)
}

func (c *updatingEmbedClient) API(ctx context.Context, method, endpoint string, body any, out any) error {
	if method == "PATCH" && strings.HasSuffix(endpoint, "/markdown") {
		request := body.(map[string]any)
		switch request["type"] {
		case "update_content":
			for _, item := range request["update_content"].(map[string]any)["content_updates"].([]any) {
				update := item.(map[string]any)
				old := update["old_str"].(string)
				if strings.Count(c.markdown, old) != 1 {
					return fmt.Errorf("ambiguous update")
				}
				c.markdown = strings.Replace(c.markdown, old, update["new_str"].(string), 1)
			}
		case "replace_content_range":
			update := request["replace_content_range"].(map[string]any)
			start, end, ok := strings.Cut(update["content_range"].(string), "...")
			if !ok || strings.Count(c.markdown, start) != 1 || strings.Count(c.markdown, end) != 1 {
				return fmt.Errorf("ambiguous range")
			}
			startAt, endAt := strings.Index(c.markdown, start), strings.Index(c.markdown, end)+len(end)
			if endAt <= startAt {
				return fmt.Errorf("invalid range")
			}
			c.markdown = c.markdown[:startAt] + update["content"].(string) + c.markdown[endAt:]
			c.ranges++
		default:
			return fmt.Errorf("unexpected markdown update: %v", request["type"])
		}
		c.patches++
	}
	if err := c.managedEmbedClient.API(ctx, method, endpoint, body, out); err != nil {
		return err
	}
	if method == "PATCH" && strings.HasSuffix(endpoint, "/markdown") {
		out.(*markdown).Warnings = c.warnings
	}
	return nil
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
	if err := exportDrawioURL(cfg, Plan{Actions: []Action{action}}, false, nil); err == nil {
		t.Fatal("dry-run must detect collision")
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
	defer func() {
		if err := r.Close(); err != nil {
			t.Errorf("close compressed diagram: %v", err)
		}
	}()
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

func TestDisabledDrawioModeProtectsExistingEmbeds(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	image := Image{Key: "image-1", Path: "diagram.drawio.svg", Hash: hash("diagram")}
	entry := Entry{PageID: rootID, ImageHashes: map[string]string{image.Key: hash("drawio-embed:" + image.Hash)}, ImageBlocks: map[string]string{image.Key: id}}
	client := &managedEmbedClient{children: []any{map[string]any{"id": id, "type": "embed"}}}
	for _, images := range [][]Image{{image}, nil} {
		if err := rejectDisabledManagedEmbeds(context.Background(), client, entry, images); err == nil || !strings.Contains(err.Error(), "--drawio-embed") {
			t.Fatalf("expected embed protection with images %v: %v", images, err)
		}
	}
	client.children = []any{map[string]any{"id": id, "type": "image"}}
	if err := rejectDisabledManagedEmbeds(context.Background(), client, entry, []Image{image}); err != nil {
		t.Fatalf("ordinary image should remain updatable: %v", err)
	}
}

func TestReplacedDrawioReferencePreservesManagedEmbed(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	baseline := "before\n<embed src=\"\"></embed>"
	client := &managedEmbedClient{markdown: "before\n<embed src=\"https://viewer.diagrams.net/diagram\"></embed>", children: []any{map[string]any{"id": id, "type": "embed"}}}
	image := Image{Key: "image-1", Name: "replacement.png", Path: "replacement.png", Hash: hash("png"), SourceLine: "![diagram](replacement.png)"}
	file := File{Path: "guide.md", Body: "before\n![diagram](replacement.png)", SyncBody: "before\n![diagram](replacement.png)", Hash: hash("changed"), PageID: rootID, Images: []Image{image}}
	entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", Target: "hierarchy:", LocalHash: hash("old"), RemoteHash: remoteMarkdownHash(baseline), ImageHashes: map[string]string{"image-1": hash("drawio-embed:old")}, ImageBlocks: map[string]string{"image-1": id}}
	state := State{Entries: map[string]Entry{"file:guide.md": entry}}
	var cfg config.Config
	cfg.Notion.ParentPageID = rootID
	cfg.Sync.DrawioEmbed = true
	plan, err := Build(context.Background(), cfg, client, []File{file}, state, true)
	if err != nil {
		t.Fatal(err)
	}
	skipped, _, _, err := inspectActions(context.Background(), cfg, client, plan)
	if err != nil || !skipped["file:guide.md"] {
		t.Fatalf("replacement must be skipped to preserve embed: skipped=%v err=%v", skipped, err)
	}
}

func TestBuildRejectsDisabledDrawioEmbedEvenWithPush(t *testing.T) {
	cfg, api, dir := setup(t)
	write(t, filepath.Join(dir, "diagram.drawio.svg"), `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	write(t, filepath.Join(dir, "guide.md"), "# Guide\n![Diagram](diagram.drawio.svg)\n")
	files, err := Scan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := "11111111-1111-1111-1111-111111111111"
	entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", Target: "hierarchy:", LocalHash: files[0].Hash, RemoteHash: hash("baseline"), ImageHashes: map[string]string{"image-1": hash("drawio-embed:" + files[0].Images[0].Hash)}, ImageBlocks: map[string]string{"image-1": id}}
	state := State{Entries: map[string]Entry{"file:guide.md": entry}}
	client := &embedAndPagesClient{fakeAPI: api, embedID: id}
	if _, err := Build(context.Background(), cfg, client, files, state, true); err == nil || !strings.Contains(err.Error(), "--drawio-embed") {
		t.Fatalf("expected embed protection before push: %v", err)
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
	if err != nil || added != ".\ntest" {
		t.Fatalf("diagram changes should not block text append: %q, %v", added, err)
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
	if !strings.Contains(joined, "page not synchronized") || !strings.Contains(joined, "cannot be preserved safely") {
		t.Fatalf("missing manual instructions: %s", joined)
	}
	if err := skippedError(*state); err == nil || !strings.Contains(err.Error(), "page.md") {
		t.Fatalf("expected partial failure: %v", err)
	}
	var preview []string
	err = previewActions(context.Background(), cfg, client, Plan{Actions: []Action{{Kind: "update", Key: "file:page.md", Path: "page.md", Entry: entry, File: File{Images: []Image{image}}}}}, func(format string, args ...any) { preview = append(preview, fmt.Sprintf(format, args...)) })
	if err == nil || !strings.Contains(strings.Join(preview, "\n"), "would be skipped") {
		t.Fatalf("preview missed skip: %v %v", err, preview)
	}
}

func TestUpdateTextAroundExistingDrawioEmbed(t *testing.T) {
	cfg, _, dir := setup(t)
	cfg.Sync.DrawioEmbed = true
	write(t, filepath.Join(dir, "home.md"), "# New heading\n\n![diagram](diagram.drawio.svg)\n\nNew body\n")
	write(t, filepath.Join(dir, "diagram.drawio.svg"), "changed diagram")
	files, err := Scan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	file := files[0]
	file.Images[0].StateHash = hash("drawio-embed:" + file.Images[0].Hash)
	parts, err := imageTextParts(file.SyncBody)
	if err != nil {
		t.Fatal(err)
	}
	blockID := "11111111-1111-1111-1111-111111111111"
	remote := "# Old heading\n<embed src=\"https://example.com/manual\"></embed>\nOld body"
	client := &updatingEmbedClient{managedEmbedClient: &managedEmbedClient{markdown: remote, children: []any{map[string]any{"id": blockID, "type": "embed"}}}}
	entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", RemoteHash: remoteMarkdownHash(strings.Replace(remote, "https://example.com/manual", "", 1)), ImageBlocks: map[string]string{"image-1": blockID}, ImageHashes: map[string]string{"image-1": "old-hash"}, managedEmbeds: map[string]bool{norm(blockID): true}}
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state.Active["home.md"] = true
	state.Entries["file:home.md"] = entry
	action := Action{Kind: "update", Key: "file:home.md", Path: "home.md", Entry: entry, File: file, TextParts: parts}
	if err := exportDrawioURL(cfg, Plan{Actions: []Action{action}}, true, nil); err != nil {
		t.Fatalf("existing embed should skip diagram export: %v", err)
	}
	if err := Apply(context.Background(), cfg, client, &state, Plan{Actions: []Action{action}}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if client.patches != 2 || client.markdown != "# New heading\n<embed src=\"https://example.com/manual\"></embed>\nNew body" {
		t.Fatalf("unexpected update: patches=%d markdown=%q", client.patches, client.markdown)
	}
	if len(state.Skipped) != 0 || state.Entries[action.Key].ImageBlocks["image-1"] != blockID || state.Entries[action.Key].ImageHashes["image-1"] != "old-hash" {
		t.Fatalf("embed state not preserved: %+v", state.Entries[action.Key])
	}
}

func TestDrawioUpdatePreservesMixedImages(t *testing.T) {
	cfg, _, dir := setup(t)
	cfg.Sync.DrawioEmbed = true
	write(t, filepath.Join(dir, "home.md"), "# New heading\n\n![photo](photo.svg)\n\nNew middle\n\n![diagram](diagram.drawio.svg)\n\nNew end\n")
	write(t, filepath.Join(dir, "photo.svg"), "photo")
	write(t, filepath.Join(dir, "diagram.drawio.svg"), "diagram")
	files, err := Scan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	file := files[0]
	parts, err := imageTextParts(file.SyncBody)
	if err != nil {
		t.Fatal(err)
	}
	imageID, embedID := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	photo := "![photo](https://files.example/photo.svg?sig=x#notion_record=block." + imageID + ".space)"
	embed := `<embed src="https://example.com/manual"></embed>`
	remote := "# Old heading\n" + photo + "\nOld middle\n" + embed + "\nOld end"
	client := &updatingEmbedClient{managedEmbedClient: &managedEmbedClient{markdown: remote, children: []any{map[string]any{"id": imageID, "type": "image"}, map[string]any{"id": embedID, "type": "embed"}}}}
	entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", RemoteHash: remoteMarkdownHash(strings.Replace(remote, "https://example.com/manual", "", 1)), ImageBlocks: map[string]string{"image-1": imageID, "image-2": embedID}, ImageHashes: map[string]string{"image-1": file.Images[0].Hash, "image-2": "old-hash"}, managedEmbeds: map[string]bool{norm(embedID): true}}
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state.Active["home.md"] = true
	state.Entries["file:home.md"] = entry
	action := Action{Kind: "update", Key: "file:home.md", Path: "home.md", Entry: entry, File: file, TextParts: parts}
	if err := Apply(context.Background(), cfg, client, &state, Plan{Actions: []Action{action}}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	want := "# New heading\n" + photo + "\nNew middle\n" + embed + "\nNew end"
	if client.markdown != want || client.patches != 3 || len(state.Skipped) != 0 {
		t.Fatalf("unexpected update: patches=%d markdown=%q skipped=%v", client.patches, client.markdown, state.Skipped)
	}
	file.Images[0].Hash = "changed"
	action.File = file
	if _, safe, err := drawioTextUpdates(context.Background(), client, action, client.markdown); err != nil || safe {
		t.Fatalf("changed image must be rejected: safe=%v err=%v", safe, err)
	}
	file.Images[0].Hash = entry.ImageHashes["image-1"]
	action.File = file
	client.children[0] = map[string]any{"id": embedID, "type": "image"}
	if _, safe, err := drawioTextUpdates(context.Background(), client, action, client.markdown); err != nil || safe {
		t.Fatalf("unmatched image must be rejected: safe=%v err=%v", safe, err)
	}
}

func TestMixedImageOnlyUpdateDoesNotReplaceChangedEmbed(t *testing.T) {
	cfg, base, dir := setup(t)
	cfg.Sync.DrawioEmbed = true
	write(t, filepath.Join(dir, "home.md"), "![photo](photo.svg)\n\n![diagram](diagram.drawio.svg)\n")
	write(t, filepath.Join(dir, "photo.svg"), "new photo")
	write(t, filepath.Join(dir, "diagram.drawio.svg"), "new diagram")
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	file := *state.Documents["home.md"]
	file.Images[1].StateHash = hash("drawio-embed:" + file.Images[1].Hash)
	photoID, embedID := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	oldURL, newURL := "https://files.example/old.svg", "https://files.example/new.svg"
	base.pages[norm(rootID)].content = "![photo](" + oldURL + ")\n<embed src=\"https://example.com/manual\"></embed>"
	base.imageFiles = map[string]imageBlock{norm(photoID): testImageBlock(photoID, oldURL, "old")}
	base.nextImageURL = newURL
	entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", RemoteHash: remoteMarkdownHash(strings.Replace(base.pages[norm(rootID)].content, "https://example.com/manual", "", 1)), ImageBlocks: map[string]string{"image-1": photoID, "image-2": embedID}, ImageHashes: map[string]string{"image-1": "old photo", "image-2": "old diagram"}, managedEmbeds: map[string]bool{norm(embedID): true}}
	state.Entries["file:home.md"] = entry
	state.Active["home.md"] = true
	client := &imageReadbackClient{fakeUploader: &fakeUploader{fakeAPI: base}, pageID: rootID, oldURL: oldURL, newURL: newURL, embedID: embedID}
	action := Action{Kind: "images-update", Key: "file:home.md", Path: "home.md", Entry: entry, File: file}
	if err := Apply(context.Background(), cfg, client, &state, Plan{Actions: []Action{action}}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if client.uploads != 1 || base.blockUpdates != 1 || state.Entries[action.Key].ImageHashes["image-2"] != "old diagram" {
		t.Fatalf("embed must remain untouched: uploads=%d updates=%d state=%+v", client.uploads, base.blockUpdates, state.Entries[action.Key])
	}
}

func TestReplaceEmbedsDoesNotForceOrdinaryPage(t *testing.T) {
	cfg, api, dir := setup(t)
	cfg.Sync.DrawioEmbed, cfg.Sync.ReplaceDrawioEmbeds = true, true
	write(t, filepath.Join(dir, "home.md"), "updated\n")
	files, err := Scan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := "11111111-1111-1111-1111-111111111111"
	api.pages[norm(id)] = &fakePage{parent: rootID, parentType: "page_id", content: "old"}
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state.Entries["file:home.md"] = Entry{PageID: id, ParentID: rootID, ParentType: "page_id", Target: "hierarchy:", LocalHash: "old", RemoteHash: remoteMarkdownHash("old"), ImageHashes: map[string]string{"image-1": "old image"}}
	plan, err := Build(context.Background(), cfg, api, files, state, true)
	if err != nil || len(plan.Actions) != 1 || plan.Actions[0].ForceReplace {
		t.Fatalf("ordinary page was forced: %+v, %v", plan, err)
	}
	if err := previewActions(context.Background(), cfg, api, plan, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "removing local images") {
		t.Fatalf("image removal accepted: %v", err)
	}
}

func TestDrawioUpdateUsesBoundedRangeForNotionTable(t *testing.T) {
	cfg, _, dir := setup(t)
	cfg.Sync.DrawioEmbed = true
	write(t, filepath.Join(dir, "home.md"), "# Guide\n\n![diagram](diagram.drawio.svg)\n\n- Updated\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\nEnd\n")
	write(t, filepath.Join(dir, "diagram.drawio.svg"), "diagram")
	files, err := Scan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	file := files[0]
	parts, err := imageTextParts(file.SyncBody)
	if err != nil {
		t.Fatal(err)
	}
	blockID := "11111111-1111-1111-1111-111111111111"
	remote := "# Guide\n<embed src=\"https://example.com/manual\"></embed>\n- Old\n<table header-row=\"true\">\n<tr><td>A</td><td>B</td></tr>\n<tr><td>1</td><td>2</td></tr>\n</table>\nEnd"
	client := &updatingEmbedClient{managedEmbedClient: &managedEmbedClient{markdown: remote, children: []any{map[string]any{"id": blockID, "type": "embed"}}}}
	entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", RemoteHash: remoteMarkdownHash(strings.Replace(remote, "https://example.com/manual", "", 1)), ImageBlocks: map[string]string{"image-1": blockID}, ImageHashes: map[string]string{"image-1": "old-hash"}, managedEmbeds: map[string]bool{norm(blockID): true}}
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state.Active["home.md"] = true
	state.Entries["file:home.md"] = entry
	action := Action{Kind: "update", Key: "file:home.md", Path: "home.md", Entry: entry, File: file, TextParts: parts}
	if err := Apply(context.Background(), cfg, client, &state, Plan{Actions: []Action{action}}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	want := "# Guide\n<embed src=\"https://example.com/manual\"></embed>\n" + parts[1]
	if client.patches != 1 || client.ranges != 1 || client.markdown != want {
		t.Fatalf("range update failed: patches=%d ranges=%d markdown=%q", client.patches, client.ranges, client.markdown)
	}
}

func TestDrawioTextRangeRejectsAmbiguousBoundaries(t *testing.T) {
	old := "same\n<table>\n<tr>value</tr>\n</table>\nend"
	remote := old + "\n<embed src=\"url\"></embed>\n" + old
	if selection, ok := drawioTextRange(remote, old); ok {
		t.Fatalf("ambiguous range accepted: %q", selection)
	}
	old = "- same\n\t- unique child\n<table>value</table>\nunique end"
	remote = "- same\nother\n<embed src=\"url\"></embed>\n" + old
	selection, ok := drawioTextRange(remote, old)
	if !ok || !strings.HasPrefix(selection, "- same\n\t- unique child...") || !strings.HasSuffix(selection, "unique end") || strings.Contains(selection, "<embed") {
		t.Fatalf("incorrect bounded range: %q (ok=%v)", selection, ok)
	}
}

func TestDrawioEmbedChangesDoNotTriggerPageUpdate(t *testing.T) {
	blockID := "11111111-1111-1111-1111-111111111111"
	remote := "# Guide\n<embed src=\"https://example.com/manual\"></embed>"
	client := &managedEmbedClient{markdown: remote, children: []any{map[string]any{"id": blockID, "type": "embed"}}}
	image := Image{Key: "image-1", Path: "diagram.drawio.svg", Name: "diagram.drawio.svg", Hash: "new-diagram-hash", SourceLine: "![diagram](diagram.drawio.svg)"}
	file := File{Path: "page.md", SyncBody: "# Guide\n![diagram](diagram.drawio.svg)", Hash: "unchanged-body", Images: []Image{image}}
	entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", Target: "hierarchy:", LocalHash: file.Hash, RemoteHash: remoteMarkdownHash(strings.Replace(remote, "https://example.com/manual", "", 1)), ImageBlocks: map[string]string{"image-1": blockID}, ImageHashes: map[string]string{"image-1": "previous-diagram-hash"}}
	state := State{Entries: map[string]Entry{"file:page.md": entry}}
	var cfg config.Config
	cfg.Notion.ParentPageID, cfg.Sync.DrawioEmbed = rootID, true
	plan, err := Build(context.Background(), cfg, client, []File{file}, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != "unchanged" {
		t.Fatalf("diagram change triggered an update: %+v", plan.Actions)
	}
}

func TestDrawioModeSwitchStillUpdatesOrdinaryImage(t *testing.T) {
	blockID := "11111111-1111-1111-1111-111111111111"
	client := &managedEmbedClient{children: []any{map[string]any{"id": blockID, "type": "image"}}}
	image := Image{Key: "image-1", Path: "diagram.drawio.svg", Name: "diagram.drawio.svg", Hash: "new-diagram-hash", StateHash: hash("drawio-embed:new-diagram-hash")}
	entry := Entry{PageID: rootID, ImageBlocks: map[string]string{"image-1": blockID}, ImageHashes: map[string]string{"image-1": "previous-image-hash"}}
	if err := identifyManagedDrawioEmbeds(context.Background(), client, &entry, []Image{image}); err != nil {
		t.Fatal(err)
	}
	if len(entry.managedEmbeds) != 0 || len(changedUnmanagedImages([]Image{image}, entry)) != 1 {
		t.Fatal("ordinary image must still be converted when enabling embed mode")
	}
}

func TestDrawioUpdateReportsWarningsWithoutSavingBaseline(t *testing.T) {
	cfg, _, dir := setup(t)
	cfg.Sync.DrawioEmbed = true
	write(t, filepath.Join(dir, "home.md"), "# New\n\n![diagram](diagram.drawio.svg)\n\nNew body\n")
	write(t, filepath.Join(dir, "diagram.drawio.svg"), "diagram")
	files, err := Scan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	file := files[0]
	parts, err := imageTextParts(file.SyncBody)
	if err != nil {
		t.Fatal(err)
	}
	blockID := "11111111-1111-1111-1111-111111111111"
	remote := "# Old\n<embed src=\"https://example.com/manual\"></embed>\nOld body"
	client := &updatingEmbedClient{managedEmbedClient: &managedEmbedClient{markdown: remote, children: []any{map[string]any{"id": blockID, "type": "embed"}}}, warnings: []jsonWarning{{Code: "no_match", Message: "a replacement did not match"}}}
	entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", LocalHash: "old-local", RemoteHash: remoteMarkdownHash(strings.Replace(remote, "https://example.com/manual", "", 1)), ImageBlocks: map[string]string{"image-1": blockID}, managedEmbeds: map[string]bool{norm(blockID): true}}
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state.Active["home.md"] = true
	state.Entries["file:home.md"] = entry
	action := Action{Kind: "update", Key: "file:home.md", Path: "home.md", Entry: entry, File: file, TextParts: parts}
	err = Apply(context.Background(), cfg, client, &state, Plan{Actions: []Action{action}}, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "no_match: a replacement did not match") || !strings.Contains(err.Error(), "partially applied") {
		t.Fatalf("missing warning details: %v", err)
	}
	if client.patches != 1 || state.Entries[action.Key].LocalHash != entry.LocalHash || state.Entries[action.Key].RemoteHash != entry.RemoteHash {
		t.Fatalf("baseline changed after a warning: patches=%d entry=%+v", client.patches, state.Entries[action.Key])
	}
}

func TestMarkdownUpdateIssueReportsIncompleteResponse(t *testing.T) {
	truncated := true
	issue := markdownUpdateIssue(markdown{Object: "page_markdown", ID: rootID, Truncated: &truncated, Unknown: []string{"block"}}, rootID)
	if !strings.Contains(issue, "truncated") || !strings.Contains(issue, "1 unknown blocks") {
		t.Fatalf("missing incomplete response details: %s", issue)
	}
}

func TestDrawioTextUpdatesRejectAmbiguousOrReorderedEmbeds(t *testing.T) {
	first := "11111111-1111-1111-1111-111111111111"
	second := "22222222-2222-2222-2222-222222222222"
	client := &managedEmbedClient{children: []any{map[string]any{"id": first, "type": "embed"}, map[string]any{"id": second, "type": "embed"}}}
	action := Action{Entry: Entry{PageID: rootID, ImageBlocks: map[string]string{"a": first, "b": second}, managedEmbeds: map[string]bool{norm(first): true, norm(second): true}}, File: File{Images: []Image{{Key: "a", Name: "a.drawio.svg"}, {Key: "b", Name: "b.drawio.svg"}}}, TextParts: []string{"changed", "middle", "end"}}
	remote := "same\n<embed src=\"a\"></embed>\nmiddle\n<embed src=\"b\"></embed>\nsame"
	if _, safe, err := drawioTextUpdates(context.Background(), client, action, remote); err != nil || safe {
		t.Fatalf("ambiguous text must be rejected: safe=%v err=%v", safe, err)
	}
	action.TextParts[0] = "same"
	client.children[0], client.children[1] = client.children[1], client.children[0]
	if _, safe, err := drawioTextUpdates(context.Background(), client, action, remote); err != nil || safe {
		t.Fatalf("reordered embeds must be rejected: safe=%v err=%v", safe, err)
	}
}

func TestSkippedEmbedPageDoesNotBlockOtherPages(t *testing.T) {
	cfg, base, dir := setup(t)
	cfg.Sync.DrawioEmbed = true
	write(t, filepath.Join(dir, "home.md"), "# Home\n")
	write(t, filepath.Join(dir, "other.md"), "# New\n\n[home](./home.md#home)\n")
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state.Active["home.md"], state.Active["other.md"] = true, true
	embedID := "11111111-1111-1111-1111-111111111111"
	otherID := "22222222-2222-2222-2222-222222222222"
	base.pages[norm(rootID)].content = `<embed src="https://example.com/manual"></embed>`
	base.pages[norm(otherID)] = &fakePage{parent: rootID, content: "# Old"}
	home := Entry{PageID: rootID, managedEmbeds: map[string]bool{norm(embedID): true}}
	other := Entry{PageID: otherID, ParentID: rootID, ParentType: "page_id", RemoteHash: remoteMarkdownHash("# Old")}
	state.Entries["file:home.md"], state.Entries["file:other.md"] = home, other
	plan := Plan{Actions: []Action{
		{Kind: "update", Key: "file:home.md", Path: "home.md", Entry: home, File: File{Images: []Image{{Name: "diagram.drawio.svg"}}}},
		{Kind: "update", Key: "file:other.md", Path: "other.md", Entry: other, File: *state.Documents["other.md"], Body: "# New"},
	}}
	client := &embedAndPagesClient{fakeAPI: base, embedID: embedID}
	if err := Apply(context.Background(), cfg, client, &state, plan, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if !state.Skipped["home.md"] || state.Skipped["other.md"] || base.pages[norm(otherID)].content != "# New" {
		t.Fatalf("unrelated page was blocked: skipped=%v content=%q", state.Skipped, base.pages[norm(otherID)].content)
	}
}

func TestSkippedPageDoesNotExportURLOrBlockOtherUpdates(t *testing.T) {
	cfg, base, dir := setup(t)
	cfg.Sync.DrawioEmbed = true
	diagram := `<svg xmlns="http://www.w3.org/2000/svg" content="&lt;mxfile&gt;&lt;diagram&gt;&lt;mxGraphModel&gt;&lt;root/&gt;&lt;/mxGraphModel&gt;&lt;/diagram&gt;&lt;/mxfile&gt;"/>`
	write(t, filepath.Join(dir, "old.drawio.svg"), diagram)
	write(t, filepath.Join(dir, "new.drawio.svg"), diagram)
	home, other := filepath.Join(dir, "home.md"), filepath.Join(dir, "other.md")
	write(t, home, "# Home\n\n![old](old.drawio.svg)\n")
	write(t, other, "# Old\n")
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	homeID, otherID, embedID := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333"
	remote := "# Home\n<embed src=\"https://example.com/manual\"></embed>"
	base.pages[norm(homeID)] = &fakePage{parent: rootID, parentType: "page_id", content: remote}
	base.pages[norm(otherID)] = &fakePage{parent: rootID, parentType: "page_id", content: "# Old\n"}
	state.Active["home.md"], state.Active["other.md"] = true, true
	state.Entries["file:home.md"] = Entry{PageID: homeID, ParentID: rootID, ParentType: "page_id", Target: "hierarchy:", LocalHash: state.Documents["home.md"].Hash, RemoteHash: remoteMarkdownHash(strings.Replace(remote, "https://example.com/manual", "", 1)), ImageBlocks: map[string]string{"image-1": embedID}, ImageHashes: map[string]string{"image-1": hash("drawio-embed:" + hash(diagram))}}
	state.Entries["file:other.md"] = Entry{PageID: otherID, ParentID: rootID, ParentType: "page_id", Target: "hierarchy:", LocalHash: state.Documents["other.md"].Hash, RemoteHash: remoteMarkdownHash("# Old\n")}
	if err := saveState(cfg, state); err != nil {
		t.Fatal(err)
	}
	write(t, home, string(mustRead(t, home))+"\n![new](new.drawio.svg)\n")
	write(t, other, strings.Replace(string(mustRead(t, other)), "# Old\n", "# New\n", 1))
	output := filepath.Join(cfg.WorkDir, ".notion-sync", "new.drawio.svg.txt")
	write(t, output, "do not overwrite")
	client := &embedAndPagesClient{fakeAPI: base, embedID: embedID}
	for _, apply := range []bool{false, true} {
		err := Run(context.Background(), cfg, client, apply, func(string, ...any) {})
		if err == nil || !strings.Contains(err.Error(), "skipped") {
			t.Fatalf("apply=%v: expected skipped page, got %v", apply, err)
		}
	}
	if base.pages[norm(otherID)].content != "# New\n" || string(mustRead(t, output)) != "do not overwrite" {
		t.Fatalf("other page or output wrong: %q %q", base.pages[norm(otherID)].content, mustRead(t, output))
	}
}

type forceReplaceEmbedClient struct{ *fakeUploader }

type linkPreviewClient struct {
	*forceReplaceEmbedClient
	embedID string
}

func (c *linkPreviewClient) API(ctx context.Context, method, endpoint string, body any, out any) error {
	if method == "GET" && strings.HasSuffix(endpoint, "/children?page_size=100") {
		data, _ := json.Marshal(map[string]any{"results": []any{map[string]any{"id": c.embedID, "type": "embed"}}, "has_more": false})
		return json.Unmarshal(data, out)
	}
	return c.forceReplaceEmbedClient.API(ctx, method, endpoint, body, out)
}

type imageReadbackClient struct {
	*fakeUploader
	pageID, oldURL, newURL, embedID string
	addText, stale, concurrentBlock bool
}

func (c *imageReadbackClient) API(ctx context.Context, method, endpoint string, body any, out any) error {
	if c.embedID != "" && method == "GET" && strings.Contains(endpoint, "/children") {
		data, _ := json.Marshal(map[string]any{"results": []any{map[string]any{"id": c.embedID, "type": "embed"}}, "has_more": false})
		return json.Unmarshal(data, out)
	}
	if err := c.fakeUploader.API(ctx, method, endpoint, body, out); err != nil {
		return err
	}
	if method == "PATCH" && strings.HasPrefix(endpoint, "v1/blocks/") && !strings.HasSuffix(endpoint, "/children") {
		p := c.pages[norm(c.pageID)]
		if !c.stale {
			p.content = strings.Replace(p.content, c.oldURL, c.newURL, 1)
		}
		if c.addText {
			p.content += "\nconcurrent change"
		}
		if c.concurrentBlock {
			id := strings.TrimPrefix(endpoint, "v1/blocks/")
			c.imageFiles[norm(id)] = testImageBlock(id, "https://files.example/other.svg", "concurrent")
		}
	}
	return nil
}

func TestImageUpdateReadbackRebaselinesAndDetectsOtherEdits(t *testing.T) {
	for _, tc := range []struct {
		name                                                                             string
		fragment, concurrent, stale, blockChange, unchangedBlock, alternateURL, wrongRef bool
		want                                                                             string
	}{
		{name: "with fragment", fragment: true},
		{name: "without fragment"},
		{name: "different URL with block ID", fragment: true, alternateURL: true},
		{name: "unrelated edit", concurrent: true, want: "other page content changed"},
		{name: "stale markdown", stale: true, want: "cannot be verified in Markdown"},
		{name: "same block changed", blockChange: true, want: "changed or could not be verified"},
		{name: "update returned old file", unchangedBlock: true, want: "does not identify a new file"},
		{name: "wrong URL after update", fragment: true, wrongRef: true, want: "cannot be verified in Markdown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, base, dir := setup(t)
			write(t, filepath.Join(dir, "home.md"), "![photo](photo.svg)\n")
			write(t, filepath.Join(dir, "photo.svg"), "new")
			state, err := loadState(cfg)
			if err != nil {
				t.Fatal(err)
			}
			file := *state.Documents["home.md"]
			blockID := "11111111-1111-1111-1111-111111111111"
			oldURL := "https://files.example/old.svg"
			newURL := "https://files.example/new.svg"
			blockOld, blockNew := oldURL, newURL
			if tc.alternateURL {
				blockOld, blockNew = "https://storage.example/old.svg", "https://storage.example/new.svg"
				oldURL = "https://www.notion.so/image/" + url.PathEscape(blockOld)
				newURL = "https://www.notion.so/image/" + url.PathEscape(blockNew)
			}
			fragment := ""
			if tc.fragment {
				fragment = "#notion_record=block." + blockID + ".space"
			}
			base.pages[norm(rootID)].content = "![photo](" + oldURL + fragment + ")"
			base.imageFiles = map[string]imageBlock{norm(blockID): testImageBlock(blockID, blockOld, "old")}
			base.nextImageURL = blockNew
			if tc.unchangedBlock {
				base.nextImageURL = oldURL
			}
			entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", RemoteHash: remoteMarkdownHash(base.pages[norm(rootID)].content), ImageBlocks: map[string]string{"image-1": blockID}, ImageHashes: map[string]string{"image-1": "old"}}
			state.Entries["file:home.md"], state.Active["home.md"] = entry, true
			readbackURL := newURL
			if tc.wrongRef {
				readbackURL = "https://cdn.example/wrong.svg"
			}
			client := &imageReadbackClient{fakeUploader: &fakeUploader{fakeAPI: base}, pageID: rootID, oldURL: oldURL, newURL: readbackURL, addText: tc.concurrent, stale: tc.stale, concurrentBlock: tc.blockChange}
			a := Action{Kind: "images-update", Key: "file:home.md", Path: "home.md", Entry: entry, File: file}
			err = Apply(context.Background(), cfg, client, &state, Plan{Actions: []Action{a}}, func(string, ...any) {})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) || state.Entries[a.Key].RemoteHash != entry.RemoteHash || state.Entries[a.Key].ImageHashes["image-1"] != "old" {
					t.Fatalf("concurrent edit accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := validateExisting(context.Background(), client, state.Entries[a.Key]); err != nil {
				t.Fatalf("updated image should not cause a conflict: %v", err)
			}
		})
	}
}

func TestImageUpdateRejectsUnmatchedOrAmbiguousReferencesBeforeUpload(t *testing.T) {
	for _, body := range []string{
		"![photo](https://files.example/unrelated.svg)",
		"![one](https://files.example/old.svg)\n![two](https://files.example/old.svg)",
	} {
		t.Run(body, func(t *testing.T) {
			cfg, base, dir := setup(t)
			write(t, filepath.Join(dir, "home.md"), "![photo](photo.svg)\n")
			write(t, filepath.Join(dir, "photo.svg"), "new")
			state, err := loadState(cfg)
			if err != nil {
				t.Fatal(err)
			}
			blockID := "11111111-1111-1111-1111-111111111111"
			base.pages[norm(rootID)].content = body
			base.imageFiles = map[string]imageBlock{norm(blockID): testImageBlock(blockID, "https://files.example/old.svg", "old")}
			entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", RemoteHash: remoteMarkdownHash(body), ImageBlocks: map[string]string{"image-1": blockID}, ImageHashes: map[string]string{"image-1": "old"}}
			state.Entries["file:home.md"], state.Active["home.md"] = entry, true
			client := &fakeUploader{fakeAPI: base}
			a := Action{Kind: "images-update", Key: "file:home.md", Path: "home.md", Entry: entry, File: *state.Documents["home.md"]}
			err = Apply(context.Background(), cfg, client, &state, Plan{Actions: []Action{a}}, func(string, ...any) {})
			if err == nil || client.uploads != 0 || base.blockUpdates != 0 || state.Entries[a.Key].ImageHashes["image-1"] != "old" {
				t.Fatalf("unsafe reference was updated: err=%v uploads=%d updates=%d", err, client.uploads, base.blockUpdates)
			}
		})
	}
}

func TestImageUpdateRejectsUnverifiableProxyBeforeUpload(t *testing.T) {
	for _, ref := range []string{"https://cdn.example/wrong", "https://cdn.example/by-block/11111111-1111-1111-1111-111111111111"} {
		t.Run(ref, func(t *testing.T) {
			cfg, base, dir := setup(t)
			write(t, filepath.Join(dir, "home.md"), "![photo](photo.svg)\n")
			write(t, filepath.Join(dir, "photo.svg"), "new")
			state, err := loadState(cfg)
			if err != nil {
				t.Fatal(err)
			}
			id := "11111111-1111-1111-1111-111111111111"
			base.pages[norm(rootID)].content = "![photo](" + ref + "#notion_record=block." + id + ")"
			base.imageFiles = map[string]imageBlock{norm(id): testImageBlock(id, "https://storage.example/old", "old")}
			entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", RemoteHash: remoteMarkdownHash(base.pages[norm(rootID)].content), ImageBlocks: map[string]string{"image-1": id}, ImageHashes: map[string]string{"image-1": "old"}}
			state.Entries["file:home.md"], state.Active["home.md"] = entry, true
			client := &fakeUploader{fakeAPI: base}
			a := Action{Kind: "images-update", Key: "file:home.md", Path: "home.md", Entry: entry, File: *state.Documents["home.md"]}
			err = Apply(context.Background(), cfg, client, &state, Plan{Actions: []Action{a}}, func(string, ...any) {})
			if err == nil || client.uploads != 0 || base.blockUpdates != 0 || state.Entries[a.Key].ImageHashes["image-1"] != "old" {
				t.Fatalf("unverifiable proxy was updated: %v, uploads=%d", err, client.uploads)
			}
		})
	}
}

func (c *forceReplaceEmbedClient) API(ctx context.Context, method, endpoint string, body any, out any) error {
	if method == "PATCH" && strings.HasSuffix(endpoint, "/children") {
		child := body.(map[string]any)["children"].([]any)[0].(map[string]any)
		c.imageBlocks = append(c.imageBlocks, child)
		b, _ := json.Marshal(map[string]any{"results": []any{map[string]any{"id": "33333333-3333-3333-3333-333333333333", "type": child["type"]}}})
		return json.Unmarshal(b, out)
	}
	return c.fakeUploader.API(ctx, method, endpoint, body, out)
}

func TestPreviewAndApplyLinkDependentEmbedReplacement(t *testing.T) {
	cfg, base, dir := setup(t)
	cfg.Sync.DrawioEmbed, cfg.Sync.ReplaceDrawioEmbeds = true, true
	diagram := `<svg xmlns="http://www.w3.org/2000/svg" content="&lt;mxfile&gt;&lt;diagram&gt;&lt;mxGraphModel&gt;&lt;root/&gt;&lt;/mxGraphModel&gt;&lt;/diagram&gt;&lt;/mxfile&gt;"/>`
	write(t, filepath.Join(dir, "diagram.drawio.svg"), diagram)
	write(t, filepath.Join(dir, "home.md"), "# Home\n\n[New](./new.md)\n\n![diagram](diagram.drawio.svg)\n")
	write(t, filepath.Join(dir, "new.md"), "# New\n")
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	homeID, embedID := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	remote := "# Home\n[New](./new.md)\n<embed src=\"https://example.com/manual\"></embed>"
	base.pages[norm(homeID)] = &fakePage{parent: rootID, parentType: "page_id", content: remote}
	state.Active["home.md"] = true
	state.Entries["file:home.md"] = Entry{PageID: homeID, ParentID: rootID, ParentType: "page_id", Target: "hierarchy:", LocalHash: state.Documents["home.md"].Hash, RemoteHash: remoteMarkdownHash(strings.Replace(remote, "https://example.com/manual", "", 1)), ImageBlocks: map[string]string{"image-1": embedID}, ImageHashes: map[string]string{"image-1": hash("drawio-embed:" + hash(diagram))}}
	if err := saveState(cfg, state); err != nil {
		t.Fatal(err)
	}
	client := &linkPreviewClient{forceReplaceEmbedClient: &forceReplaceEmbedClient{fakeUploader: &fakeUploader{fakeAPI: base}}, embedID: embedID}
	var preview strings.Builder
	if err := Run(context.Background(), cfg, client, false, func(format string, args ...any) { fmt.Fprintf(&preview, format+"\n", args...) }, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.String(), "new page links will change") || !strings.Contains(preview.String(), "embeds recreated empty") {
		t.Fatalf("missing replacement preview: %s", preview.String())
	}
	if base.creates != 0 || base.updates != 0 {
		t.Fatal("dry-run modified Notion")
	}
	output := filepath.Join(cfg.WorkDir, ".notion-sync", "diagram.drawio.svg.txt")
	write(t, output, "user content")
	for _, apply := range []bool{false, true} {
		if err := Run(context.Background(), cfg, client, apply, func(string, ...any) {}, true); err == nil || !strings.Contains(err.Error(), "already exists with different content") {
			t.Fatalf("apply=%v: expected output collision: %v", apply, err)
		}
	}
	if base.creates != 0 || base.updates != 0 {
		t.Fatal("output collision was detected after a Notion write")
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), cfg, client, true, func(string, ...any) {}, true); err != nil {
		t.Fatal(err)
	}
	if base.creates != 1 || !strings.Contains(base.pages[norm(homeID)].content, "<mention-page") {
		t.Fatalf("link not synchronized: creates=%d content=%q", base.creates, base.pages[norm(homeID)].content)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("replacement URL missing: %v", err)
	}
}

func TestReplaceEmbedsDetectsDiagramOnlyChange(t *testing.T) {
	cfg, base, dir := setup(t)
	cfg.Sync.DrawioEmbed = true
	write(t, filepath.Join(dir, "home.md"), "# Home\n\n![diagram](diagram.drawio.svg)\n")
	write(t, filepath.Join(dir, "diagram.drawio.svg"), "new diagram")
	files, err := Scan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	homeID, embedID := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	remote := "# Home\n<embed src=\"https://example.com/manual\"></embed>"
	base.pages[norm(homeID)] = &fakePage{parent: rootID, parentType: "page_id", content: remote}
	state.Entries["file:home.md"] = Entry{PageID: homeID, ParentID: rootID, ParentType: "page_id", Target: "hierarchy:", LocalHash: files[0].Hash, RemoteHash: remoteMarkdownHash(strings.Replace(remote, "https://example.com/manual", "", 1)), ImageBlocks: map[string]string{"image-1": embedID}, ImageHashes: map[string]string{"image-1": hash("drawio-embed:" + hash("old diagram"))}}
	client := &embedAndPagesClient{fakeAPI: base, embedID: embedID}
	plan, err := Build(context.Background(), cfg, client, files, state, true)
	if err != nil || plan.Actions[0].Kind != "unchanged" {
		t.Fatalf("without replacement: %+v %v", plan, err)
	}
	cfg.Sync.ReplaceDrawioEmbeds = true
	plan, err = Build(context.Background(), cfg, client, files, state, true)
	if err != nil || !plan.Actions[0].ForceReplace {
		t.Fatalf("diagram change not detected: %+v %v", plan, err)
	}
}

func TestPushReplacesPageAndRecreatesEmptyDrawioEmbed(t *testing.T) {
	cfg, api, dir := setup(t)
	cfg.Sync.DrawioEmbed = true
	cfg.Sync.ReplaceDrawioEmbeds = true
	diagram := `<svg xmlns="http://www.w3.org/2000/svg" content="&lt;mxfile&gt;&lt;diagram&gt;&lt;mxGraphModel&gt;&lt;root/&gt;&lt;/mxGraphModel&gt;&lt;/diagram&gt;&lt;/mxfile&gt;"/>`
	write(t, filepath.Join(dir, "diagram.drawio.svg"), diagram)
	write(t, filepath.Join(dir, "home.md"), "# New\n\n![diagram](diagram.drawio.svg)\n\nNew body\n")
	files, err := Scan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	file := files[0]
	id := "11111111-1111-1111-1111-111111111111"
	embedID := "22222222-2222-2222-2222-222222222222"
	remote := "# Old\n<embed src=\"https://example.com/manual\"></embed>\nOld body"
	api.pages[norm(id)] = &fakePage{parent: rootID, parentType: "page_id", content: remote}
	entry := Entry{PageID: id, ParentID: rootID, ParentType: "page_id", Target: "hierarchy:", LocalHash: "old-local", RemoteHash: remoteMarkdownHash(remote), ImageBlocks: map[string]string{"image-1": embedID}, ImageHashes: map[string]string{"image-1": hash("drawio-embed:" + file.Images[0].Hash)}}
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state.Entries["file:home.md"] = entry
	state.Active["home.md"] = true
	client := &forceReplaceEmbedClient{fakeUploader: &fakeUploader{fakeAPI: api}}
	plan, err := Build(context.Background(), cfg, client, files, state, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || !plan.Actions[0].ForceReplace {
		t.Fatalf("missing forced replacement: %+v", plan.Actions)
	}
	cfg.Sync.ReplaceDrawioEmbeds = false
	ordinaryPush, err := Build(context.Background(), cfg, client, files, state, true)
	if err != nil || len(ordinaryPush.Actions) != 1 || ordinaryPush.Actions[0].ForceReplace {
		t.Fatalf("ordinary push must preserve embeds: %+v, %v", ordinaryPush, err)
	}
	cfg.Sync.ReplaceDrawioEmbeds = true
	var preview strings.Builder
	printPlan(plan, func(format string, args ...any) { fmt.Fprintf(&preview, format, args...) })
	if !strings.Contains(preview.String(), "embeds recreated empty") {
		t.Fatalf("missing preview: %s", preview.String())
	}
	if err := exportDrawioURL(cfg, plan, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), cfg, client, &state, plan, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if got := api.pages[norm(id)].content; strings.Contains(got, "Old") || strings.Contains(got, "manual") || !strings.Contains(got, "New body") {
		t.Fatalf("page not replaced: %q", got)
	}
	if len(api.imageBlocks) != 1 || api.imageBlocks[0]["type"] != "embed" || api.imageBlocks[0]["embed"].(map[string]any)["url"] != "" {
		t.Fatalf("embed not recreated empty: %+v", api.imageBlocks)
	}
	if state.Entries["file:home.md"].ImageBlocks["image-1"] != "33333333-3333-3333-3333-333333333333" {
		t.Fatal("new block not saved")
	}
}
