package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuyj109/notion-sync/internal/config"
)

const rootID = "01234567-89ab-cdef-0123-456789abcdef"

type fakePage struct {
	parent, content, parentType string
	locked                      bool
	icon                        any
}
type fakeAPI struct {
	pages            map[string]*fakePage
	creates, updates int
	blockUpdates     int
	blockUpdateBody  map[string]any
	failCreate       bool
	failLock         bool
	icons            []any
	failMarkdown     bool
	failUpdate       bool
	onUpdate         func()
	onMarkdownGet    func()
	titles           []string
	imageBlocks      []map[string]any
	imageFiles       map[string]imageBlock
	nextImageURL     string
	renderImages     bool
	events           []string
	pageGets         map[string]int
}

func (f *fakeAPI) API(_ context.Context, method, endpoint string, body any, out any) error {
	copyResult := func(x any) error {
		b, e := json.Marshal(x)
		if e != nil {
			return e
		}
		return json.Unmarshal(b, out)
	}
	if method == "PATCH" && strings.Contains(endpoint, "/children") {
		b := body.(map[string]any)
		f.imageBlocks = append(f.imageBlocks, b["children"].([]any)[0].(map[string]any))
		id := fmt.Sprintf("%032x", 999)
		if f.imageFiles == nil {
			f.imageFiles = map[string]imageBlock{}
		}
		f.imageFiles[norm(id)] = testImageBlock(id, "https://files.example/old.png", "old")
		f.events = append(f.events, "image")
		return copyResult(map[string]any{"results": []any{map[string]any{"id": fmt.Sprintf("%032x", 999), "type": "image"}}})
	}
	if method == "GET" && strings.HasPrefix(endpoint, "v1/blocks/") && strings.Contains(endpoint, "/children") {
		var children []any
		if len(f.imageBlocks) > 0 {
			children = append(children, map[string]any{"id": fmt.Sprintf("%032x", 999), "type": "image"})
		}
		return copyResult(map[string]any{"results": children, "has_more": false})
	}
	if method == "PATCH" && strings.HasPrefix(endpoint, "v1/blocks/") {
		f.blockUpdates++
		f.blockUpdateBody = body.(map[string]any)
		id := strings.TrimPrefix(endpoint, "v1/blocks/")
		newURL := f.nextImageURL
		if newURL == "" {
			newURL = fmt.Sprintf("https://files.example/new-%d.png", f.blockUpdates)
		}
		if f.imageFiles == nil {
			f.imageFiles = map[string]imageBlock{}
		}
		block := testImageBlock(id, newURL, fmt.Sprintf("updated-%d", f.blockUpdates))
		f.imageFiles[norm(id)] = block
		return copyResult(block)
	}
	if method == "GET" && strings.HasPrefix(endpoint, "v1/blocks/") && !strings.Contains(endpoint, "/children") {
		if b, ok := f.imageFiles[norm(strings.TrimPrefix(endpoint, "v1/blocks/"))]; ok {
			return copyResult(b)
		}
		return errors.New("block not found")
	}
	if method == "POST" && endpoint == "v1/pages" {
		f.creates++
		if f.failCreate {
			return errors.New("lost response")
		}
		b := body.(map[string]any)
		f.icons = append(f.icons, b["icon"])
		props := b["properties"].(map[string]any)
		for _, prop := range props {
			parts := prop.(map[string]any)["title"].([]any)
			f.titles = append(f.titles, parts[0].(map[string]any)["text"].(map[string]any)["content"].(string))
		}
		p := b["parent"].(map[string]any)
		parentType := "page_id"
		parent, ok := p[parentType].(string)
		if !ok {
			parentType = "data_source_id"
			parent = p[parentType].(string)
		}
		id := fmt.Sprintf("%032x", f.creates+100)
		content, _ := b["markdown"].(string)
		if content != "" {
			f.events = append(f.events, "text")
		}
		f.pages[norm(id)] = &fakePage{parent: parent, content: content, parentType: parentType, icon: b["icon"]}
		if icon, ok := b["icon"].(map[string]any); ok {
			icon["icon"].(map[string]any)["color"] = "gray"
		}
		return copyResult(map[string]any{"object": "page", "id": id, "parent": map[string]any{"type": parentType, parentType: parent}})
	}
	id := strings.TrimPrefix(endpoint, "v1/pages/")
	isMarkdown := strings.HasSuffix(id, "/markdown")
	id = strings.TrimSuffix(id, "/markdown")
	p, ok := f.pages[norm(id)]
	if !ok {
		return errors.New("not found")
	}
	if method == "GET" && !isMarkdown {
		if f.pageGets != nil {
			f.pageGets[norm(id)]++
		}
		parentType := p.parentType
		if parentType == "" {
			parentType = "page_id"
		}
		return copyResult(map[string]any{"object": "page", "id": id, "is_locked": p.locked, "icon": p.icon, "parent": map[string]any{"type": parentType, parentType: p.parent}})
	}
	if method == "PATCH" && !isMarkdown {
		if f.failLock {
			return errors.New("lock failed")
		}
		b := body.(map[string]any)
		if value, ok := b["is_locked"].(bool); ok {
			p.locked = value
		}
		if icon, ok := b["icon"].(map[string]any); ok {
			icon["icon"].(map[string]any)["color"] = "gray"
			p.icon = icon
		}
		return copyResult(map[string]any{"object": "page", "id": id, "is_locked": p.locked, "icon": p.icon})
	}
	if method == "PATCH" {
		if f.failUpdate {
			return errors.New("update failed")
		}
		f.updates++
		b := body.(map[string]any)
		if b["type"] == "insert_content" {
			content := b["insert_content"].(map[string]any)["content"].(string)
			if p.content != "" {
				p.content += "\n"
			}
			p.content += content
			f.events = append(f.events, "text")
		} else {
			p.content = b["replace_content"].(map[string]any)["new_str"].(string)
			f.imageBlocks = nil
		}
		if f.onUpdate != nil {
			f.onUpdate()
		}
	}
	if isMarkdown && method == "GET" && f.failMarkdown {
		return errors.New("readback failed")
	}
	if isMarkdown && method == "GET" && f.onMarkdownGet != nil {
		f.onMarkdownGet()
	}
	content := p.content
	if isMarkdown && f.renderImages && len(f.imageBlocks) > 0 {
		block := f.imageFiles[norm(fmt.Sprintf("%032x", 999))]
		content += "\n![Architecture](" + block.Image.File.URL + ")"
	}
	return copyResult(map[string]any{"object": "page_markdown", "id": id, "markdown": content, "truncated": false, "unknown_block_ids": []string{}})
}

func testImageBlock(id, rawURL, edited string) imageBlock {
	var b imageBlock
	b.Object, b.ID, b.Type, b.LastEditedTime = "block", id, "image", edited
	b.Image.Type, b.Image.File.URL = "file", rawURL
	return b
}

type fakeUploader struct {
	*fakeAPI
	uploads int
}

func (f *fakeUploader) Upload(_ context.Context, _, _, _ string) (string, error) {
	f.uploads++
	return "22222222-2222-2222-2222-222222222222", nil
}
func setup(t *testing.T) (config.Config, *fakeAPI, string) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "docs")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	var c config.Config
	c.WorkDir = dir
	c.SourcePath = source
	c.Notion.ParentPageID = rootID
	c.Sync.PreserveHierarchy = true
	c.Sync.Include = []string{"**/*.md"}
	c.Sync.Exclude = []string{"**/node_modules/**"}
	api := &fakeAPI{pages: map[string]*fakePage{norm(rootID): {parent: rootID}}}
	return c, api, source
}
func write(t *testing.T, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func run(c config.Config, api *fakeAPI, apply bool) error {
	return Run(context.Background(), c, api, apply, func(string, ...any) {})
}

func TestNewPagesUseDocumentIconAndLock(t *testing.T) {
	c, api, dir := setup(t)
	c.Sync.Icon = "document"
	write(t, filepath.Join(dir, "child", "a.md"), "body\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if len(api.icons) != 2 {
		t.Fatalf("expected directory and file pages, got %d", len(api.icons))
	}
	for _, icon := range api.icons {
		data, _ := json.Marshal(icon)
		if string(data) != `{"icon":{"color":"gray","name":"document"},"type":"icon"}` {
			t.Fatalf("unexpected icon: %s", data)
		}
	}
	for id, p := range api.pages {
		if id != norm(rootID) && !p.locked {
			t.Fatalf("page %s not locked", id)
		}
	}
}

func TestOptionalIconForExistingAndNewPages(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "existing.md"), "existing\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if len(api.icons) != 1 || api.icons[0] != nil {
		t.Fatalf("default creation unexpectedly sets icon: %v", api.icons)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	page := api.pages[norm(s.Entries["file:existing.md"].PageID)]
	write(t, filepath.Join(dir, "new.md"), "new\n")
	c.Sync.Icon = "document"
	var lines []string
	print := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	if err := Run(context.Background(), c, api, false, print); err != nil {
		t.Fatal(err)
	}
	if page.icon != nil || !strings.Contains(strings.Join(lines, "\n"), "set icon document on existing.md") || !strings.Contains(strings.Join(lines, "\n"), "set icon document on new page: new.md") {
		t.Fatalf("dry-run did not safely preview icons: %v", lines)
	}
	if err := Run(context.Background(), c, api, true, print); err != nil {
		t.Fatal(err)
	}
	if page.icon == nil || len(api.icons) != 2 || api.icons[1] == nil {
		t.Fatalf("icons were not set: existing=%v creates=%v", page.icon, api.icons)
	}
	lines = nil
	if err := Run(context.Background(), c, api, false, print); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(lines, "\n"), "set icon ") {
		t.Fatalf("already matching icons should not be updated: %v", lines)
	}
	c.Sync.Icon = ""
	if err := run(c, api, true); err != nil || page.icon == nil {
		t.Fatalf("omitting icon changed the existing icon: %v", err)
	}
}

func TestRandomIconIsStableForSyncAndPreview(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "existing.md"), "existing\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "new.md"), "new\n")
	c.Sync.Icon = "random"
	var lines []string
	print := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	if err := Run(context.Background(), c, api, false, print); err != nil {
		t.Fatal(err)
	}
	existingIcon := requestedIcon(c, Action{Key: "file:existing.md"})
	newIcon := requestedIcon(c, Action{Key: "file:new.md"})
	preview := strings.Join(lines, "\n")
	if !strings.Contains(preview, "set icon "+existingIcon+" on existing.md") || !strings.Contains(preview, "set icon "+newIcon+" on new page: new.md") {
		t.Fatalf("unexpected random preview: %s", preview)
	}
	if err := Run(context.Background(), c, api, true, print); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	for filename, want := range map[string]string{"existing.md": existingIcon, "new.md": newIcon} {
		icon := api.pages[norm(s.Entries["file:"+filename].PageID)].icon.(map[string]any)
		if got := icon["icon"].(map[string]any)["name"]; got != want {
			t.Fatalf("%s: icon = %v, want %s", filename, got, want)
		}
	}
	lines = nil
	if err := Run(context.Background(), c, api, false, print); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(lines, "\n"), "set icon ") {
		t.Fatalf("random icons changed on second sync: %v", lines)
	}
}

func TestLockFailureKeepsKnownPendingPage(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "body\n")
	api.failLock = true
	if err := run(c, api, true); err == nil || !strings.Contains(err.Error(), "lock could not be verified") {
		t.Fatalf("expected lock failure, got %v", err)
	}
	s, err := loadStateWithPending(c, true)
	if err != nil || s.Pending == nil || s.Pending.PageID == "" || api.creates != 1 {
		t.Fatalf("pending page not retained: %+v, %v", s.Pending, err)
	}
}

func TestSyncLocksExistingPagesIncludingUnchanged(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "body\n")
	write(t, filepath.Join(dir, "b.md"), "body\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.md", "b.md"} {
		api.pages[norm(s.Entries["file:"+name].PageID)].locked = false
	}
	write(t, filepath.Join(dir, "b.md"), string(mustRead(t, filepath.Join(dir, "b.md")))+"updated\n")
	var lines []string
	print := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	if err := Run(context.Background(), c, api, false, print); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.md", "b.md"} {
		if api.pages[norm(s.Entries["file:"+name].PageID)].locked || !strings.Contains(strings.Join(lines, "\n"), "lock "+name+" ->") {
			t.Fatalf("dry run did not preview lock for %s", name)
		}
	}
	if err := Run(context.Background(), c, api, true, print); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.md", "b.md"} {
		if !api.pages[norm(s.Entries["file:"+name].PageID)].locked {
			t.Fatalf("%s is not locked", name)
		}
	}
}

func TestSyncShowsProgressBeforePlan(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "body\n")
	var lines []string
	if err := Run(context.Background(), c, api, false, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	for _, text := range []string{"scanning source files:", "checking destination in Notion", "checking page 1/1: a.md", "create a.md"} {
		if !strings.Contains(joined, text) {
			t.Fatalf("missing progress %q in %s", text, joined)
		}
	}
	if strings.Index(joined, "checking page 1/1") > strings.Index(joined, "create a.md") {
		t.Fatalf("plan was printed before progress: %s", joined)
	}
}

func TestCreatingPageDoesNotRecheckUnrelatedPages(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "existing.md"), "existing\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	existingID := norm(s.Entries["file:existing.md"].PageID)
	api.pageGets = map[string]int{}
	write(t, filepath.Join(dir, "new.md"), "new\n")
	var lines []string
	if err := Run(context.Background(), c, api, true, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}); err != nil {
		t.Fatal(err)
	}
	if api.pageGets[existingID] != 1 {
		t.Fatalf("existing page checked %d times, want 1", api.pageGets[existingID])
	}
	if strings.Count(strings.Join(lines, "\n"), "checking page 1/2: existing.md") != 1 {
		t.Fatalf("unexpected progress: %v", lines)
	}
}

func TestSyncResolvesRelativeLinksAfterCreatingPages(t *testing.T) {
	c, api, dir := setup(t)
	a := filepath.Join(dir, "a.md")
	b := filepath.Join(dir, "child", "b.md")
	write(t, a, "[child](./child/b.md)\n`[code](./child/b.md)`\n```\n[code](./child/b.md)\n```\n[missing](./missing.md)\n")
	write(t, b, "[back](../a.md)\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	aID, bID := s.Entries["file:a.md"].PageID, s.Entries["file:child/b.md"].PageID
	content := api.pages[norm(aID)].content
	if !strings.Contains(content, `<mention-page url="`+pageURL(bID)+`">child</mention-page>`) || !strings.Contains(content, "`[code](./child/b.md)`") || !strings.Contains(content, "[missing](./missing.md)") || !strings.Contains(content, "```\n[code](./child/b.md)\n```") {
		t.Fatalf("unexpected content: %q", content)
	}
	if !strings.Contains(api.pages[norm(bID)].content, `<mention-page url="`+pageURL(aID)+`">back</mention-page>`) {
		t.Fatal("back link was not resolved")
	}
	if !strings.Contains(string(mustRead(t, a)), "[child](./child/b.md)") {
		t.Fatal("source link changed")
	}
	updates := api.updates
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.updates != updates {
		t.Fatal("second sync updated unchanged pages")
	}
}

func TestSyncResumesAfterEmptyPageCreation(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "[b](./b.md)\n")
	write(t, filepath.Join(dir, "b.md"), "body\n")
	api.failUpdate = true
	if err := run(c, api, true); err == nil {
		t.Fatal("expected update failure")
	}
	if api.creates != 2 {
		t.Fatalf("created %d pages", api.creates)
	}
	api.failUpdate = false
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.creates != 2 {
		t.Fatal("retry created duplicate pages")
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	a := api.pages[norm(s.Entries["file:a.md"].PageID)]
	if !strings.Contains(a.content, pageURL(s.Entries["file:b.md"].PageID)) {
		t.Fatalf("link not resolved: %q", a.content)
	}
}

func TestSyncUpdatesLinkWhenTargetIsAdded(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "[b](./child/b.md)\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "child", "b.md"), "body\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	a := api.pages[norm(s.Entries["file:a.md"].PageID)]
	if !strings.Contains(a.content, pageURL(s.Entries["file:child/b.md"].PageID)) {
		t.Fatalf("link not updated: %q", a.content)
	}
}

func TestSyncLinkAndImage(t *testing.T) {
	c, base, dir := setup(t)
	api := &fakeUploader{fakeAPI: base}
	write(t, filepath.Join(dir, "a.md"), "[b](./b.md)\n\n![image](./image.png)\n\n[again](./b.md)\n")
	write(t, filepath.Join(dir, "b.md"), "body\n")
	write(t, filepath.Join(dir, "image.png"), "image")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	content := api.pages[norm(s.Entries["file:a.md"].PageID)].content
	if strings.Count(content, pageURL(s.Entries["file:b.md"].PageID)) != 2 || api.uploads != 1 {
		t.Fatalf("content %q, uploads %d", content, api.uploads)
	}
}

func TestResolveLinksEscapesMentionLabel(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	docs := map[string]*File{"b.md": {PageID: id}}
	got := resolveLinks("[A & B](./b.md)", "a.md", docs, nil)
	want := `<mention-page url="` + pageURL(id) + `">A &amp; B</mention-page>`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

type directoryDatabaseAPI struct{ *fakeAPI }

func (f directoryDatabaseAPI) API(ctx context.Context, method, endpoint string, body, out any) error {
	const database = "11111111-1111-1111-1111-111111111111"
	const source = "22222222-2222-2222-2222-222222222222"
	var result any
	switch endpoint {
	case "v1/databases/" + database:
		result = map[string]any{"object": "database", "id": database, "data_sources": []any{map[string]any{"id": source}}}
	case "v1/data_sources/" + source:
		result = map[string]any{"object": "data_source", "id": source, "properties": map[string]any{"Name": map[string]any{"type": "title"}}}
	default:
		return f.fakeAPI.API(ctx, method, endpoint, body, out)
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func TestDirectoryDatabaseSync(t *testing.T) {
	c, base, dir := setup(t)
	c.Notion.ParentPageID = ""
	c.Notion.ParentDatabaseID = "11111111-1111-1111-1111-111111111111"
	api := directoryDatabaseAPI{base}
	for _, name := range []string{"a.md", "nested/b.md"} {
		write(t, filepath.Join(dir, name), "# Example\n")
	}
	for _, apply := range []bool{false, true, true} {
		if err := Run(context.Background(), c, api, apply, func(string, ...any) {}); err != nil {
			t.Fatal(err)
		}
		if !apply && base.creates != 0 {
			t.Fatal("dry run wrote to Notion")
		}
	}
	if base.creates != 2 || base.updates != 2 {
		t.Fatalf("expected two database entries and two content updates, got %d creates and %d updates", base.creates, base.updates)
	}
	for _, page := range base.pages {
		if page.parentType != "" && (page.parentType != "data_source_id" || page.parent != "22222222-2222-2222-2222-222222222222") {
			t.Fatalf("unexpected destination: %+v", page)
		}
	}
	c.Notion.ParentDatabaseID = "33333333-3333-3333-3333-333333333333"
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "does not match configured parent") {
		t.Fatalf("expected changed destination to fail, got %v", err)
	}
	if base.creates != 2 {
		t.Fatal("changed destination caused a write")
	}
}

func TestDatabaseDefaultRespectsFileParent(t *testing.T) {
	c, base, dir := setup(t)
	c.Notion.ParentPageID = ""
	c.Notion.ParentDatabaseID = "11111111-1111-1111-1111-111111111111"
	write(t, filepath.Join(dir, "a.md"), "---\nnotion-parent-page-id: "+rootID+"\n---\n# Example\n")
	if err := Run(context.Background(), c, directoryDatabaseAPI{base}, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if base.creates != 1 {
		t.Fatalf("expected one page, got %d", base.creates)
	}
	for id, page := range base.pages {
		if id != norm(rootID) && (page.parentType != "page_id" || norm(page.parent) != norm(rootID)) {
			t.Fatalf("frontmatter parent not respected: %+v", page)
		}
	}
}
func TestLifecycleAndConflict(t *testing.T) {
	c, api, dir := setup(t)
	file := filepath.Join(dir, "nested", "a.md")
	write(t, file, "# Hello\n")
	if err := run(c, api, false); err != nil {
		t.Fatal(err)
	}
	if api.creates != 0 {
		t.Fatal("dry run wrote to Notion")
	}
	data, _ := os.ReadFile(file)
	if string(data) != "# Hello\n" {
		t.Fatal("dry run changed the file")
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.creates != 2 {
		t.Fatalf("expected directory and page, got %d", api.creates)
	}
	if api.titles[0] != "nested" || api.titles[1] != "Hello" {
		t.Fatalf("unexpected titles: %v", api.titles)
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.creates != 2 || api.updates != 1 {
		t.Fatal("second sync was not idempotent")
	}
	data, _ = os.ReadFile(file)
	write(t, file, strings.Replace(string(data), "# Hello\n", "# Changed\n", 1))
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.updates != 2 {
		t.Fatal("expected update")
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	api.pages[norm(s.Entries["file:nested/a.md"].PageID)].content = "other edit"
	if err := run(c, api, true); err == nil || !strings.Contains(err.Error(), "changed in Notion") {
		t.Fatalf("expected conflict, got %v", err)
	}
	if api.updates != 2 {
		t.Fatal("conflict caused a write")
	}
}

func TestSyncLockedPageStillChecksRemoteChanges(t *testing.T) {
	c, api, dir := setup(t)
	file := filepath.Join(dir, "a.md")
	write(t, file, "original\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	p := api.pages[norm(s.Entries["file:a.md"].PageID)]
	p.locked = true
	write(t, file, strings.Replace(string(mustRead(t, file)), "original\n", "local update\n", 1))
	if err := RunFile(context.Background(), c, api, file, true, func(string, ...any) {}); err != nil {
		t.Fatalf("locked page blocked synchronization: %v", err)
	}
	if p.content != "local update\n" {
		t.Fatalf("locked page was not updated: %q", p.content)
	}
	p.content = "Notion update\n"
	if err := RunFile(context.Background(), c, api, file, true, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "changed in Notion") {
		t.Fatalf("expected conflict after Notion content changed: %v", err)
	}
	if p.content != "Notion update\n" {
		t.Fatal("conflicting Notion content was overwritten")
	}
}

func TestOverwriteRemoteChanges(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "local\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	id := s.Entries["file:a.md"].PageID
	api.pages[norm(id)].content = "edited in Notion"
	if err := run(c, api, true); err == nil || !strings.Contains(err.Error(), "-edited in Notion") || !strings.Contains(err.Error(), "+local") {
		t.Fatalf("expected conflict with diff, got %v", err)
	}
	before := api.updates
	var output strings.Builder
	if err := Run(context.Background(), c, api, false, func(format string, args ...any) { fmt.Fprintf(&output, format+"\n", args...) }, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "-edited in Notion") || !strings.Contains(output.String(), "+local") {
		t.Fatalf("missing preview diff: %s", output.String())
	}
	if api.updates != before {
		t.Fatal("dry run wrote to Notion")
	}
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}, true); err != nil {
		t.Fatal(err)
	}
	if api.pages[norm(id)].content != "local\n" {
		t.Fatal("local content did not replace remote content")
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
}

func TestOverwriteRejectsChangesAfterPlanning(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "local\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	f := *s.Documents["a.md"]
	s.Active[f.Path] = true
	id := s.Entries["file:a.md"].PageID
	api.pages[norm(id)].content = "first edit"
	p, err := Build(context.Background(), c, api, []File{f}, s, true)
	if err != nil {
		t.Fatal(err)
	}
	api.pages[norm(id)].content = "second edit"
	if err := Apply(context.Background(), c, api, &s, p, func(string, ...any) {}); err == nil {
		t.Fatal("expected conflict after planning")
	}
	if api.pages[norm(id)].content != "second edit" {
		t.Fatal("later edit was overwritten")
	}
}

func TestRunFileConflictProvidesNumberedDiff(t *testing.T) {
	c, api, dir := setup(t)
	filename := filepath.Join(dir, "a.md")
	write(t, filename, "# A\n\nlocal text\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	api.pages[norm(s.Entries["file:a.md"].PageID)].content = "# A\nremote text\n"
	noop := func(string, ...any) {}
	err = RunFile(context.Background(), c, api, filename, false, noop)
	var views interface{ DiffViews() (string, string) }
	if !errors.As(err, &views) {
		t.Fatalf("expected conflict with diff views, got %v", err)
	}
	plain, numbered := views.DiffViews()
	if !strings.Contains(plain, "-remote text") || !strings.Contains(plain, "+local text") {
		t.Fatalf("missing diff: %s", plain)
	}
	line := sourceBodyOffset(*s.Documents["a.md"]) + 3
	if !strings.Contains(numbered, fmt.Sprintf("%4d │ local text", line)) || !strings.Contains(numbered, "   2      │ remote text") {
		t.Fatalf("missing original line numbers: %s", numbered)
	}
	var preview DiffOutput
	err = RunFile(context.Background(), c, api, filename, false, func(_ string, args ...any) {
		if len(args) == 1 {
			if d, ok := args[0].(DiffOutput); ok {
				preview = d
			}
		}
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Plain != plain || preview.Numbered != numbered {
		t.Fatal("push preview differs from conflict diff")
	}
}

func TestPageTitle(t *testing.T) {
	tests := []struct{ body, want string }{
		{"## Subheading\n# First\n# Second\n", "First"},
		{"```go\n# Code\n```\n # Heading ###\n", "Heading"},
		{"~~~\n# Code\n~~~\n#\tTitle\r\n", "Title"},
		{"# \n#### Other\n#Valid\n", "file"},
		{"    # Indented code\n", "file"},
	}
	for _, tt := range tests {
		if got := pageTitle(tt.body, "dir/file.md"); got != tt.want {
			t.Errorf("pageTitle(%q) = %q, want %q", tt.body, got, tt.want)
		}
	}
	f, err := parse([]byte("---\ntitle: ignored\n---\n# Body title\n"))
	if err != nil || pageTitle(f.Body, "file.md") != "Body title" {
		t.Fatalf("frontmatter: body=%q, error=%v", f.Body, err)
	}
}

func TestHTMLCommentsExcludedFromSync(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"inline", "before <!-- hidden --> after", "before  after"},
		{"multiline", "one\n<!-- hidden\n![x](missing.png)\n-->\ntwo\n", "one\n\n\n\ntwo\n"},
		{"fenced", "```html\n<!-- example -->\n```\n<!-- hidden -->", "```html\n<!-- example -->\n```\n"},
		{"quoted fence", "> ```html\n> <!-- example -->\n> ```\n> <!-- hidden -->", "> ```html\n> <!-- example -->\n> ```\n> "},
		{"list comment", "- item\n    <!-- hidden -->\n", "- item\n    \n"},
		{"inline code", "`<!-- example -->` <!-- hidden -->", "`<!-- example -->` "},
		{"indented code", "    <!-- example -->\n<!-- hidden -->", "    <!-- example -->\n"},
		{"escaped", `\<!-- example -->`, `\<!-- example -->`},
		{"unclosed", "before <!-- hidden", "before "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := withoutHTMLComments(tc.input); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	c, api, dir := setup(t)
	file := filepath.Join(dir, "guide.md")
	write(t, file, "# Guide\n<!-- cms-doc:manual id=\"notes\" -->\n<!-- ![x](missing.png) -->\nVisible\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.creates != 1 {
		t.Fatalf("created %d pages", api.creates)
	}
	for id, page := range api.pages {
		if id != norm(rootID) && (strings.Contains(page.content, "<!--") || !strings.Contains(page.content, "Visible")) {
			t.Fatalf("synced content = %q", page.content)
		}
	}
	local := string(mustRead(t, file))
	if !strings.Contains(local, "<!-- cms-doc:manual") {
		t.Fatal("local comment was removed")
	}
	write(t, file, strings.Replace(local, `id="notes"`, `id="other"`, 1))
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.updates != 1 {
		t.Fatalf("comment-only change updated Notion %d times", api.updates)
	}
}

func TestLocalImageScanAndCreate(t *testing.T) {
	c, api, dir := setup(t)
	api.renderImages = true
	imgDir := filepath.Join(dir, "images")
	if err := os.Mkdir(imgDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, "diagram.png"), []byte("png-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "guide.md"), "# Guide\nBefore\n\n![Architecture](images/diagram.png)\n\nAfter\n")
	files, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || len(files[0].Images) != 1 || files[0].Images[0].Alt != "Architecture" {
		t.Fatalf("images: %+v", files)
	}
	apiWithUpload := &fakeUploader{fakeAPI: api}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	for i := range files {
		s.Documents[files[i].Path] = &files[i]
		s.Active[files[i].Path] = true
	}
	plan, err := Build(context.Background(), c, api, files, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), c, apiWithUpload, &s, plan, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if apiWithUpload.uploads != 1 || len(api.imageBlocks) != 1 {
		t.Fatalf("uploads=%d blocks=%d", apiWithUpload.uploads, len(api.imageBlocks))
	}
	frontmatter := string(mustRead(t, filepath.Join(dir, "guide.md")))
	if !strings.Contains(frontmatter, "notion-images:\n  image-1:\n    hash:") || !strings.Contains(frontmatter, "    block-id:") || strings.Contains(frontmatter, "notion-image-blocks:") {
		t.Fatalf("image hash and block ID should be paired in notion-images: %s", frontmatter)
	}
	page := api.pages[norm(s.Entries["file:guide.md"].PageID)]
	if page.content != "# Guide\nBefore\nAfter" {
		t.Fatalf("markdown body retains image: %q", page.content)
	}
	if strings.Join(api.events, ",") != "text,image,text" {
		t.Fatalf("content order = %v", api.events)
	}
	// Re-running unchanged content skips image uploads.
	files, err = Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	s, err = loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	for i := range files {
		s.Documents[files[i].Path] = &files[i]
		s.Active[files[i].Path] = true
	}
	plan, err = Build(context.Background(), c, api, files, s)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Actions[0].Kind != "unchanged" {
		t.Fatalf("unchanged image kind=%s", plan.Actions[0].Kind)
	}
	if err := Apply(context.Background(), c, apiWithUpload, &s, plan, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if apiWithUpload.uploads != 1 {
		t.Fatalf("unchanged image uploaded again: %d", apiWithUpload.uploads)
	}
	// A changed local image is replaced in place without an opt-in flag.
	if err := os.WriteFile(filepath.Join(imgDir, "diagram.png"), []byte("new-png-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err = Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	s, err = loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	for i := range files {
		s.Documents[files[i].Path] = &files[i]
		s.Active[files[i].Path] = true
	}
	plan, err = Build(context.Background(), c, api, files, s)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Actions[0].Kind != "images-update" {
		t.Fatalf("default changed image kind=%s", plan.Actions[0].Kind)
	}
	if err := Apply(context.Background(), c, apiWithUpload, &s, plan, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if apiWithUpload.uploads != 2 || api.blockUpdates != 1 || len(api.imageBlocks) != 1 {
		t.Fatalf("changed-image uploads=%d replacements=%d appended blocks=%d", apiWithUpload.uploads, api.blockUpdates, len(api.imageBlocks))
	}
	updatedImage := api.blockUpdateBody["image"].(map[string]any)
	if _, ok := updatedImage["type"]; ok || updatedImage["file_upload"] == nil {
		t.Fatalf("unexpected image update payload: %#v", api.blockUpdateBody)
	}
	// A Markdown text change rebuilds the body and local image blocks in order.
	if err := os.WriteFile(filepath.Join(imgDir, "diagram.png"), []byte("newer-png-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	guide := filepath.Join(dir, "guide.md")
	write(t, guide, strings.Replace(string(mustRead(t, guide)), "Before\n", "Before updated\n", 1))
	files, err = Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	s, err = loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	for i := range files {
		s.Documents[files[i].Path] = &files[i]
		s.Active[files[i].Path] = true
	}
	plan, err = Build(context.Background(), c, api, files, s)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Actions[0].Kind != "update" {
		t.Fatalf("text and image update kind=%s", plan.Actions[0].Kind)
	}
	var plannedCombined []string
	printPlan(plan, func(format string, args ...any) {
		plannedCombined = append(plannedCombined, fmt.Sprintf(format, args...))
	})
	if len(plannedCombined) != 1 || !strings.Contains(plannedCombined[0], "images replaced: 1") {
		t.Fatalf("combined update plan=%v", plannedCombined)
	}
	var completed []string
	if err := Apply(context.Background(), c, apiWithUpload, &s, plan, func(format string, args ...any) { completed = append(completed, fmt.Sprintf(format, args...)) }); err != nil {
		t.Fatal(err)
	}
	if apiWithUpload.uploads != 3 || api.updates != 3 || len(api.imageBlocks) != 1 {
		t.Fatalf("combined update uploads=%d text updates=%d image blocks=%d", apiWithUpload.uploads, api.updates, len(api.imageBlocks))
	}
	if api.pages[norm(s.Entries["file:guide.md"].PageID)].content != "# Guide\nBefore updated\nAfter" {
		t.Fatal("combined update did not preserve text order")
	}
	if !strings.Contains(strings.Join(completed, "\n"), "images replaced: 1") {
		t.Fatalf("combined update output=%v", completed)
	}
	if s.Entries["file:guide.md"].ImageBlocks["image-1"] == "" {
		t.Fatal("replacement image block ID not persisted")
	}
	if s.Entries["file:guide.md"].ImageHashes["image-1"] != hash("newer-png-bytes") {
		t.Fatal("replacement image hash not persisted")
	}
}

func TestCreateMultipleFilesWithMiddleImagePersistsState(t *testing.T) {
	c, api, dir := setup(t)
	if err := os.Mkdir(filepath.Join(dir, "images"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "help.png"), []byte("image-data"), 0600); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "plan-notion-sync.md"), "# Plan\nBefore\n![help](images/help.png)\nAfter\n")
	write(t, filepath.Join(dir, "progress.md"), "# Progress\n")
	files, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	s := State{Version: 1, RootID: rootID, Entries: map[string]Entry{}, Documents: map[string]*File{}, Active: map[string]bool{}}
	for i := range files {
		s.Documents[files[i].Path] = &files[i]
		s.Active[files[i].Path] = true
	}
	plan, err := Build(context.Background(), c, api, files, s)
	if err != nil {
		t.Fatal(err)
	}
	apiWithUpload := &fakeUploader{fakeAPI: api}
	if err := Apply(context.Background(), c, apiWithUpload, &s, plan, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if s.Pending != nil || len(s.Entries) != 2 {
		t.Fatalf("pending=%v entries=%v", s.Pending, s.Entries)
	}
}

func TestLocalImageRejectsOutsideSourceAndCodeFence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "outside.png"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := localImages("![x](../outside.png)", "guide.md", dir); err == nil {
		t.Fatal("expected outside source rejection")
	}
	if images, err := localImages("```md\n![x](missing.png)\n```", "guide.md", dir); err != nil || len(images) != 0 {
		t.Fatalf("code sample should not be resolved: images=%v err=%v", images, err)
	}
}

func TestInlineLocalImageRejected(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{"text ![x](missing.png)", "![x](missing.png) text", "- ![x](missing.png)"} {
		if _, err := localImages(body, "guide.md", dir); err == nil || !strings.Contains(err.Error(), "standalone") {
			t.Errorf("%q: expected standalone error, got %v", body, err)
		}
	}
	for _, body := range []string{"text ![x](https://example.com/a.png)", "`![x](missing.png)`", "\\![x](missing.png)", "```md\ntext ![x](missing.png)\n```"} {
		if _, err := localImages(body, "guide.md", dir); err != nil {
			t.Errorf("%q: unexpected error: %v", body, err)
		}
	}
}

func TestLocalReferenceImagesRejected(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{
		"![diagram][ref]\n\n[ref]: images/a.png\n",
		"![diagram][]\n\n[diagram]: images/a.png\n",
		"![diagram]\n\n[diagram]: images/a.png\n",
		"text ![diagram][ref]\n\n[ref]: images/a.png\n",
	} {
		if _, err := localImages(body, "guide.md", dir); err == nil || !strings.Contains(err.Error(), "standalone") {
			t.Errorf("%q: expected standalone error, got %v", body, err)
		}
	}
	for _, body := range []string{
		"![diagram][ref]\n\n[ref]: https://example.com/a.png\n",
		"`![diagram][ref]`\n\n[ref]: images/a.png\n",
		"```md\n![diagram][ref]\n```\n\n[ref]: images/a.png\n",
	} {
		if _, err := localImages(body, "guide.md", dir); err != nil {
			t.Errorf("%q: unexpected error: %v", body, err)
		}
	}
}

func TestLocalSVGImageUsesSVGContentType(t *testing.T) {
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "diagram.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0600); err != nil {
		t.Fatal(err)
	}
	images, err := localImages("![diagram](diagram.svg)", "guide.md", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].ContentType != "image/svg+xml" {
		t.Fatalf("unexpected SVG image: %+v", images)
	}
}

func TestRemoteImageHashIgnoresExpiringSignature(t *testing.T) {
	a := remoteMarkdownHash("![image](<https://files.notion.so/image.png?signature=one>)")
	b := remoteMarkdownHash("![image](<https://files.notion.so/image.png?signature=two>)")
	if a != b {
		t.Fatal("signed image URL query changed the remote hash")
	}
	if a == remoteMarkdownHash("![image](<https://files.notion.so/other.png?signature=one>)") {
		t.Fatal("different image path did not change hash")
	}
}

func TestRemoteImageHashNormalizesNotionS3Signatures(t *testing.T) {
	for _, base := range []string{
		"https://s3.us-west-2.amazonaws.com/secure.notion-static.com/file.png",
		"https://prod-files-secure.s3.us-west-2.amazonaws.com/file.png",
	} {
		a := "![x](" + base + "?X-Amz-Signature=one&X-Amz-Date=one&version=1)"
		b := "![x](" + base + "?X-Amz-Signature=two&X-Amz-Date=two&version=1)"
		c := "![x](" + base + "?X-Amz-Signature=two&X-Amz-Date=two&version=2)"
		if remoteMarkdownHash(a) != remoteMarkdownHash(b) || remoteMarkdownHash(a) == remoteMarkdownHash(c) {
			t.Fatalf("incorrect signature normalization for %s", base)
		}
	}
	for _, base := range []string{"https://s3.us-west-2.amazonaws.com/other-bucket/file.png", "https://example.com/file.png"} {
		if remoteMarkdownHash("![x]("+base+"?X-Amz-Signature=one)") == remoteMarkdownHash("![x]("+base+"?X-Amz-Signature=two)") {
			t.Fatalf("ignored external signature for %s", base)
		}
	}
}

func TestRemoteImageHashNormalizesNotionS3SignatureV2(t *testing.T) {
	base := "![x](https://s3.us-west-2.amazonaws.com/secure.notion-static.com/file.png?"
	a := base + "Signature=one&Expires=1&AWSAccessKeyId=key1&versionId=1)"
	b := base + "Signature=two&Expires=2&AWSAccessKeyId=key2&versionId=1)"
	c := base + "Signature=two&Expires=2&AWSAccessKeyId=key2&versionId=2)"
	if remoteMarkdownHash(a) != remoteMarkdownHash(b) || remoteMarkdownHash(a) == remoteMarkdownHash(c) {
		t.Fatal("incorrect Signature V2 normalization")
	}
}

func TestRemoteImageHashNormalizesProxySignatures(t *testing.T) {
	proxy := func(base, signature, version string) string {
		source := base + "?X-Amz-Signature=" + signature + "&versionId=" + version
		return "![x](https://www.notion.so/image/" + url.PathEscape(source) + "?table=block)"
	}
	path := "https://s3.us-west-2.amazonaws.com/secure.notion-static.com/folder/file.png"
	host := "https://secure.notion-static.com.s3.us-west-2.amazonaws.com/folder/file.png"
	if remoteMarkdownHash(proxy(path, "old", "one")) != remoteMarkdownHash(proxy(host, "new", "one")) {
		t.Fatal("same Notion image triggered a conflict")
	}
	if remoteMarkdownHash(proxy(path, "old", "one")) == remoteMarkdownHash(proxy(host, "new", "two")) {
		t.Fatal("changed version ignored")
	}
	thirdParty := "https://example.com/file.png"
	if remoteMarkdownHash(proxy(thirdParty, "old", "one")) == remoteMarkdownHash(proxy(thirdParty, "new", "one")) {
		t.Fatal("external signature ignored")
	}
}

func TestRemoteImageHashPreservesMeaningfulQueries(t *testing.T) {
	for _, pair := range [][2]string{
		{"![x](https://example.com/x.png?version=1)", "![x](https://example.com/x.png?version=2)"},
		{"![x](https://files.notion.so/x.png?signature=a&version=1)", "![x](https://files.notion.so/x.png?signature=b&version=2)"},
		{"`![x](https://files.notion.so/x.png?signature=a)`", "`![x](https://files.notion.so/x.png?signature=b)`"},
		{"```\n![x](https://files.notion.so/x.png?signature=a)\n```", "```\n![x](https://files.notion.so/x.png?signature=b)\n```"},
	} {
		if remoteMarkdownHash(pair[0]) == remoteMarkdownHash(pair[1]) {
			t.Fatalf("meaningful change ignored: %q", pair)
		}
	}
}

func TestOversizeImageRejectedBeforePageCreation(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "![image](big.png)\n")
	f, err := os.Create(filepath.Join(dir, "big.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(20*1024*1024 + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, apply := range []bool{false, true} {
		if err := run(c, api, apply); err == nil || !strings.Contains(err.Error(), "20 MB") {
			t.Fatalf("apply=%v: %v", apply, err)
		}
		if err := RunFile(context.Background(), c, api, filepath.Join(dir, "a.md"), apply, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "20 MB") {
			t.Fatalf("single file apply=%v: %v", apply, err)
		}
		if api.creates != 0 {
			t.Fatal("page created before image validation")
		}
	}
}

func TestPreviewRejectsImageRemoval(t *testing.T) {
	action := Action{Kind: "update", Path: "a.md", Entry: Entry{ImageHashes: map[string]string{"image-1": "hash"}}}
	for _, force := range []bool{false, true} {
		action.ForceReplace = force
		if err := previewActions(context.Background(), config.Config{}, nil, Plan{Actions: []Action{action}}, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "removing local images") {
			t.Fatalf("force=%v: expected image removal error: %v", force, err)
		}
	}
}

func TestUncertainCreateStopsRetry(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "Hello")
	api.failCreate = true
	if err := run(c, api, true); err == nil {
		t.Fatal("expected failure")
	}
	api.failCreate = false
	if err := run(c, api, true); err == nil || !strings.Contains(err.Error(), "earlier create") {
		t.Fatalf("expected pending error, got %v", err)
	}
	if api.creates != 1 {
		t.Fatal("repeated uncertain create")
	}
}
func TestFrontmatter(t *testing.T) {
	tests := []struct {
		input string
		bad   bool
	}{{"---\nnotion-page-id: \"0123456789abcdef0123456789abcdef\"\n---\nText", false}, {"---\nnotion-page-id: bad\n---\nText", true}, {"---\nnotion-page-id: \"0123456789abcdef0123456789abcdef\"\nnotion-page-id: \"0123456789abcdef0123456789abcdef\"\n---\n", true}, {"---\nnotion-database-id: \"0123456789abcdef0123456789abcdef\"\nnotion-parent-page-id: \"0123456789abcdef0123456789abcdef\"\n---\n", true}}
	for _, tt := range tests {
		f, err := parse([]byte(tt.input))
		if (err != nil) != tt.bad {
			t.Fatalf("parse(%q): %v", tt.input, err)
		}
		if !tt.bad && f.Body != "Text" {
			t.Fatalf("frontmatter leaked into body: %q", f.Body)
		}
	}
}

func TestSingleFileDestinationPersists(t *testing.T) {
	c, api, dir := setup(t)
	target := filepath.Join(dir, "single.md")
	write(t, target, "---\nnotion-parent-page-id: "+rootID+"\n---\nhello")
	noop := func(string, ...any) {}
	if err := RunFile(context.Background(), c, api, target, true, noop); err != nil {
		t.Fatal(err)
	}
	if err := RunFile(context.Background(), c, api, target, true, noop); err != nil {
		t.Fatal(err)
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.creates != 1 {
		t.Fatalf("created %d pages", api.creates)
	}
}
func TestExistingPageRequiresBaseline(t *testing.T) {
	c, api, dir := setup(t)
	id := "11111111-1111-1111-1111-111111111111"
	api.pages[norm(id)] = &fakePage{parent: rootID, content: "existing remote content"}
	write(t, filepath.Join(dir, "a.md"), "---\nnotion-page-id: \""+id+"\"\n---\nnew content")
	if err := run(c, api, true); err == nil || !strings.Contains(err.Error(), "no synchronization baseline") {
		t.Fatalf("expected safety stop, got %v", err)
	}
	if api.updates != 0 {
		t.Fatal("existing content overwritten")
	}
}

type databaseAPI struct{ count int }

func (d databaseAPI) API(_ context.Context, method, endpoint string, body any, out any) error {
	sources := make([]map[string]any, d.count)
	for i := range sources {
		sources[i] = map[string]any{"id": fmt.Sprintf("%032x", i+1000)}
	}
	var response any
	if strings.HasPrefix(endpoint, "v1/databases/") {
		response = map[string]any{"object": "database", "id": rootID, "data_sources": sources}
	} else {
		response = map[string]any{"object": "data_source", "id": fmt.Sprintf("%032x", 1000), "properties": map[string]any{"Name": map[string]any{"type": "title"}}}
	}
	b, _ := json.Marshal(response)
	return json.Unmarshal(b, out)
}
func TestDatabaseMustHaveSingleSource(t *testing.T) {
	for _, n := range []int{0, 2} {
		if _, _, err := ResolveDataSource(context.Background(), databaseAPI{count: n}, rootID); err == nil {
			t.Fatalf("expected error with %d sources", n)
		}
	}
	id, title, err := ResolveDataSource(context.Background(), databaseAPI{count: 1}, rootID)
	if err != nil || id == "" || title != "Name" {
		t.Fatalf("id=%s title=%s error=%v", id, title, err)
	}
}

func TestFrontmatterPreservesMetadataAndBody(t *testing.T) {
	c, api, dir := setup(t)
	file := filepath.Join(dir, "a.md")
	body := "# Hello\r\nBody\r\n"
	write(t, file, "---\r\ntitle: My title\r\ntags: [one, two]\r\n---\r\n"+body)
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Body != body || parsed.PageID == "" || parsed.Metadata == nil || parsed.Metadata.Entry == nil {
		t.Fatalf("missing metadata or altered body: %#v", parsed)
	}
	if !strings.Contains(string(data), "title: My title") || !strings.Contains(string(data), "tags:") {
		t.Fatalf("original metadata lost: %s", data)
	}
	if _, err = os.Stat(filepath.Join(c.WorkDir, ".notion-sync")); !os.IsNotExist(err) {
		t.Fatal("created legacy state directory")
	}
	if err = run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.creates != 1 || api.updates != 1 {
		t.Fatal("not idempotent")
	}
}

func TestCommentMetadataSync(t *testing.T) {
	c, api, dir := setup(t)
	c.Sync.MetadataFormat = "comment"
	file := filepath.Join(dir, "a.md")
	write(t, file, "---\ntitle: My title\n---\n# Hello\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	data := string(mustRead(t, file))
	if !strings.Contains(data, "title: My title") || !strings.Contains(data, "<!-- notion-sync\n") || !strings.Contains(data, "notion-page-id:") {
		t.Fatalf("missing frontmatter or comment metadata: %s", data)
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.creates != 1 || api.updates != 1 {
		t.Fatalf("sync was not idempotent: creates=%d updates=%d", api.creates, api.updates)
	}
}

func TestMetadataFormatConversion(t *testing.T) {
	c, api, dir := setup(t)
	file := filepath.Join(dir, "a.md")
	write(t, file, "---\r\ntitle: Keep me\r\n---\r\n# Hello\r\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	c.Sync.MetadataFormat = "comment"
	if err := run(c, api, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mustRead(t, file)), "<!-- notion-sync") {
		t.Fatal("dry run changed metadata")
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	data := string(mustRead(t, file))
	if !strings.HasPrefix(data, "---\r\ntitle: Keep me\r\n---\r\n<!-- notion-sync\r\n") || !strings.HasSuffix(data, "-->\r\n# Hello\r\n") {
		t.Fatalf("incorrect comment conversion: %q", data)
	}
	if root, err := FileRootID(file); err != nil || norm(root) != norm(rootID) {
		t.Fatalf("root = %q, err = %v", root, err)
	}
	c.Sync.MetadataFormat = "frontmatter"
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	data = string(mustRead(t, file))
	if strings.Contains(data, "<!-- notion-sync") || !strings.Contains(data, "title: Keep me") || !strings.Contains(data, "notion-page-id:") {
		t.Fatalf("incorrect frontmatter conversion: %q", data)
	}
	if api.creates != 1 || api.updates != 1 {
		t.Fatalf("conversion wrote to Notion: creates=%d updates=%d", api.creates, api.updates)
	}
}

func TestCommentMetadataParsingSafety(t *testing.T) {
	id := "01234567-89ab-cdef-0123-456789abcdef"
	for _, input := range []string{
		"<!-- notion-sync\nnotion-page-id: " + id + "\n",
		"<!-- notion-sync\ntitle: invalid\n-->\nbody\n",
		"---\nnotion-page-id: " + id + "\n---\n<!-- notion-sync\nnotion-page-id: " + id + "\n-->\nbody\n",
	} {
		if _, err := parse([]byte(input)); err == nil {
			t.Fatalf("accepted invalid metadata: %q", input)
		}
	}
	input := "<!-- ordinary comment -->\n# Body\n"
	f, err := parse([]byte(input))
	if err != nil || f.Body != input || f.Metadata != nil {
		t.Fatalf("ordinary comment changed: body=%q metadata=%+v err=%v", f.Body, f.Metadata, err)
	}
	input = "<!-- notion-sync\r\nnotion-page-id: " + id + "\r\n-->\r\n# Body\r\n"
	f, err = parse([]byte(input))
	if err != nil || f.PageID != id || f.Body != "# Body\r\n" {
		t.Fatalf("comment metadata not parsed: %+v, %v", f, err)
	}
}
func TestMissingDirectoryMetadataStops(t *testing.T) {
	c, api, dir := setup(t)
	file := filepath.Join(dir, "nested", "a.md")
	write(t, file, "body")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for line := range strings.SplitSeq(string(mustRead(t, file)), "\n") {
		if !strings.HasPrefix(line, "notion-directory.nested:") {
			lines = append(lines, line)
		}
	}
	write(t, file, strings.Join(lines, "\n"))
	if err := run(c, api, true); err == nil || !strings.Contains(err.Error(), "directory metadata") {
		t.Fatalf("expected safety stop, got %v", err)
	}
	if api.creates != 2 {
		t.Fatal("created duplicate directory")
	}
}
func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestMetadataWriteDoesNotOverwriteConcurrentEdit(t *testing.T) {
	c, _, dir := setup(t)
	name := filepath.Join(dir, "a.md")
	write(t, name, "before\n")
	f, err := parse(mustRead(t, name))
	if err != nil {
		t.Fatal(err)
	}
	f.Path = "a.md"
	write(t, name, "edited concurrently\n")
	m := Metadata{Version: 1, RootID: norm(rootID)}
	if err := writeMetadata(c, &f, &m); err == nil {
		t.Fatal("expected concurrent edit error")
	}
	if string(mustRead(t, name)) != "edited concurrently\n" {
		t.Fatal("concurrent edit was overwritten")
	}
}

func TestPlanPrintsPageURL(t *testing.T) {
	var lines []string
	printPlan(Plan{Actions: []Action{{Kind: "unchanged", Path: "progress.md", Entry: Entry{PageID: "407b955d-f763-8108-80a8-ff85d51b780b"}}}}, func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	if len(lines) != 1 || lines[0] != "unchanged progress.md -> https://notion.so/407b955d-f763-8108-80a8-ff85d51b780b" {
		t.Fatalf("unexpected output: %v", lines)
	}
}

func TestFlatDirectoryMetadataRoundTrip(t *testing.T) {
	c, api, dir := setup(t)
	name := filepath.Join(dir, "nested", "deeper", "a.md")
	write(t, name, "body\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	b := mustRead(t, name)
	if !strings.Contains(string(b), "notion-directory.nested:") || !strings.Contains(string(b), "notion-directory.nested%2Fdeeper:") {
		t.Fatalf("missing flat directories: %s", b)
	}
	if strings.Contains(string(b), "notion-sync:\n") {
		t.Fatal("nested metadata found")
	}
	if strings.Contains(string(b), "notion-parent:") {
		t.Fatal("directory parent duplicated in metadata")
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.creates != 3 {
		t.Fatalf("unexpected create count %d", api.creates)
	}
}
func TestConflictingFrontmatterFormsRejected(t *testing.T) {
	s := "---\nnotion-root-id: 0123456789abcdef0123456789abcdef\nnotion-sync:\n  version: 1\n  root-id: 0123456789abcdef0123456789abcdef\n---\nbody\n"
	f, err := parse([]byte(s))
	if err != nil || f.Metadata == nil || f.Metadata.Entry != nil {
		t.Fatalf("legacy nested metadata should be ignored: metadata=%+v err=%v", f.Metadata, err)
	}
}

func TestFlatDatabaseAvoidsDuplicateTarget(t *testing.T) {
	c, _, dir := setup(t)
	name := filepath.Join(dir, "entry.md")
	db := "11111111-1111-1111-1111-111111111111"
	ds := "22222222-2222-2222-2222-222222222222"
	id := "33333333-3333-3333-3333-333333333333"
	write(t, name, "---\nnotion-database-id: "+db+"\n---\nbody\n")
	f, err := parse(mustRead(t, name))
	if err != nil {
		t.Fatal(err)
	}
	f.Path = "entry.md"
	m := Metadata{Version: 1, RootID: rootID, Entry: &Entry{PageID: id, ParentID: ds, ParentType: "data_source_id", Target: "database:" + norm(db), LocalHash: hash("body\n"), RemoteHash: hash("remote")}}
	if err = writeMetadata(c, &f, &m); err != nil {
		t.Fatal(err)
	}
	data := mustRead(t, name)
	if strings.Contains(string(data), "notion-target:") {
		t.Fatalf("database duplicated: %s", data)
	}
	parsed, err := parse(data)
	if err != nil || parsed.Metadata.Entry.Target != "database:"+norm(db) || parsed.Metadata.Entry.ParentID != ds {
		t.Fatalf("cannot recover database metadata: %#v %v", parsed, err)
	}
}

func TestRecoverPendingPageRequiresVerifiedParent(t *testing.T) {
	c, api, dir := setup(t)
	file := filepath.Join(dir, "recover.md")
	write(t, file, "# local body\n")
	api.failCreate = true
	if err := run(c, api, true); err == nil {
		t.Fatal("expected uncertain creation")
	}
	id := "11111111-1111-1111-1111-111111111111"
	api.pages[norm(id)] = &fakePage{parent: "22222222-2222-2222-2222-222222222222", content: "remote"}
	if _, err := Recover(context.Background(), c, api, file, id, false); err == nil {
		t.Fatal("accepted wrong parent")
	}
	f, err := parse(mustRead(t, file))
	if err != nil || f.Metadata.Pending == nil {
		t.Fatal("pending state was lost")
	}
	api.pages[norm(id)].parent = rootID
	if _, err := Recover(context.Background(), c, api, file, id, false); err != nil {
		t.Fatal(err)
	}
	f, err = parse(mustRead(t, file))
	if err != nil || f.Metadata.Pending != nil || f.PageID != id || f.Metadata.Entry.RemoteHash != hash("remote") {
		t.Fatalf("bad recovery: %#v %v", f, err)
	}
	api.failCreate = false
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.updates != 1 || api.creates != 1 {
		t.Fatal("recovery did not update existing page")
	}
}
func TestRecoverAbsentCreation(t *testing.T) {
	c, api, dir := setup(t)
	file := filepath.Join(dir, "recover.md")
	write(t, file, "body")
	api.failCreate = true
	if err := run(c, api, true); err == nil {
		t.Fatal("expected uncertain creation")
	}
	if _, err := Recover(context.Background(), c, api, file, "", true); err != nil {
		t.Fatal(err)
	}
	api.failCreate = false
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.creates != 2 {
		t.Fatalf("expected explicit retry, got %d attempts", api.creates)
	}
}

func TestRecoverCommentPendingCreation(t *testing.T) {
	c, api, dir := setup(t)
	c.Sync.MetadataFormat = "comment"
	file := filepath.Join(dir, "recover.md")
	write(t, file, "# Recover\n")
	api.failCreate = true
	if err := run(c, api, true); err == nil {
		t.Fatal("expected uncertain creation")
	}
	if err := Reset(file, false, func(string, ...any) {}); err == nil {
		t.Fatal("reset accepted pending creation")
	}
	if _, err := Recover(context.Background(), c, api, file, "", true); err != nil {
		t.Fatal(err)
	}
	api.failCreate = false
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if api.creates != 2 || !strings.HasPrefix(string(mustRead(t, file)), "<!-- notion-sync\n") {
		t.Fatalf("unexpected recovery: creates=%d", api.creates)
	}
}

func TestRecoverIncompleteImagePageRequiresSync(t *testing.T) {
	c, api, dir := setup(t)
	if err := os.Mkdir(filepath.Join(dir, "images"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "a.png"), []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	pageID := "33333333-3333-3333-3333-333333333333"
	file := filepath.Join(dir, "recover.md")
	content := "---\nnotion-root-id: " + rootID + "\nnotion-pending-key: file:recover.md\nnotion-pending-parent-id: " + rootID + "\nnotion-pending-title: Recover\nnotion-pending-page-id: " + pageID + "\n---\n# Recover\n![image](images/a.png)\n"
	write(t, file, content)
	api.pages[norm(pageID)] = &fakePage{parent: rootID, content: "# Recover"}
	if _, err := Recover(context.Background(), c, api, file, pageID, false); err != nil {
		t.Fatal(err)
	}
	f, err := parse(mustRead(t, file))
	if err != nil {
		t.Fatal(err)
	}
	if f.Metadata.Entry == nil || f.Metadata.Entry.LocalHash == f.Hash || len(f.Metadata.Entry.ImageHashes) != 0 {
		t.Fatalf("recovered image baseline=%+v", f.Metadata.Entry)
	}
	if err := Run(context.Background(), c, &fakeUploader{fakeAPI: api}, true, func(string, ...any) {}); err != nil {
		t.Fatalf("sync after recovery: %v", err)
	}
	if api.updates == 0 {
		t.Fatal("incomplete recovered page was not updated")
	}
}

func TestIndentedCodeImageIsNotUploaded(t *testing.T) {
	body := "    ![example](missing.png)\n\n![real](real.png)\n"
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.png"), []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	images, err := localImages(body, "a.md", dir)
	if err != nil || len(images) != 1 || images[0].Path != "real.png" {
		t.Fatalf("images=%v err=%v", images, err)
	}
	parts, err := imageTextParts(body)
	if err != nil || len(parts) != 2 || !strings.Contains(parts[0], "    ![example](missing.png)") {
		t.Fatalf("parts=%q err=%v", parts, err)
	}
	text, _, err := bodyWithoutImages(body)
	if err != nil || !strings.Contains(text, "    ![example](missing.png)") {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestConcurrentEditDoesNotBlockSavingOtherFile(t *testing.T) {
	c, api, dir := setup(t)
	a := filepath.Join(dir, "a.md")
	b := filepath.Join(dir, "b.md")
	write(t, a, "first\n")
	write(t, b, "other\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	write(t, a, strings.Replace(string(mustRead(t, a)), "first\n", "updated\n", 1))
	api.onUpdate = func() { write(t, b, strings.Replace(string(mustRead(t, b)), "other\n", "edited during sync\n", 1)) }
	err := run(c, api, true)
	if err == nil || !strings.Contains(err.Error(), "b.md changed during sync") {
		t.Fatalf("expected concurrent edit error, got %v", err)
	}
	parsed, err := parse(mustRead(t, a))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Metadata.Entry.LocalHash != hash("updated\n") {
		t.Fatal("successful update was not recorded")
	}
	if !strings.Contains(string(mustRead(t, b)), "edited during sync") {
		t.Fatal("concurrent edit was overwritten")
	}
	if api.updates != 3 {
		t.Fatalf("expected one update, got %d", api.updates)
	}
}
func TestCreatedPageIDPersistedBeforeFailedReadback(t *testing.T) {
	c, api, dir := setup(t)
	file := filepath.Join(dir, "a.md")
	write(t, file, "body\n")
	api.failMarkdown = true
	if err := run(c, api, true); err == nil {
		t.Fatal("expected readback failure")
	}
	f, err := parse(mustRead(t, file))
	if err != nil {
		t.Fatal(err)
	}
	if f.Metadata == nil || f.Metadata.Pending == nil || f.Metadata.Pending.PageID == "" {
		t.Fatalf("created page ID was not saved: %#v", f.Metadata)
	}
	if api.creates != 1 {
		t.Fatalf("expected one create, got %d", api.creates)
	}
}

func TestRootIDUsesHyphensAndNormalizesExistingFrontmatter(t *testing.T) {
	c, api, dir := setup(t)
	c.Notion.ParentPageID = strings.ReplaceAll(rootID, "-", "")
	file := filepath.Join(dir, "a.md")
	write(t, file, "body\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	original := string(mustRead(t, file))
	if !strings.Contains(original, "notion-root-id: "+rootID) {
		t.Fatalf("root not formatted: %s", original)
	}
	legacy := strings.Replace(original, rootID, strings.ReplaceAll(strings.ToUpper(rootID), "-", ""), 1)
	write(t, file, legacy)
	if err := run(c, api, false); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, file)) != legacy {
		t.Fatal("dry run changed metadata")
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustRead(t, file)), "notion-root-id: "+rootID) {
		t.Fatal("existing root ID was not formatted")
	}
	if api.creates != 1 || api.updates != 1 {
		t.Fatal("normalization wrote to Notion")
	}
}
func TestPlanFormatsParentURL(t *testing.T) {
	var lines []string
	printPlan(Plan{Actions: []Action{{Kind: "create", Path: "a.md", ParentID: "0123456789ABCDEF0123456789ABCDEF", ParentType: "page_id"}}}, func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	want := "create a.md -> https://notion.so/01234567-89ab-cdef-0123-456789abcdef"
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("got %v, want %s", lines, want)
	}
}
