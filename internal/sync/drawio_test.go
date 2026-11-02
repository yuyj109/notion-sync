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
