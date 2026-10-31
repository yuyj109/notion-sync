package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuyj109/notion-sync/internal/config"
)

func TestLocalHeadingAnchors(t *testing.T) {
	body := "# 案内\n\n## 設定\n\n## 設定\n\n## A &#x42;\n\n```\n## 偽の見出し\n```\n"
	for _, anchor := range []string{"案内", "設定", "設定-1", "a-b"} {
		if !localHeadingExists(body, anchor) {
			t.Fatalf("missing %s", anchor)
		}
	}
	if localHeadingExists(body, "偽の見出し") {
		t.Fatal("code block parsed as heading")
	}
}

func TestJapaneseHeadingAnchorMatchesNotionFullWidthSpace(t *testing.T) {
	const fragment = "sss-マッピング"
	if !localHeadingExists("## SSS マッピング\n", fragment) {
		t.Fatal("expected local heading to produce the requested anchor")
	}
	block := notionAnchorBlock{
		ID:   "11111111-1111-1111-1111-111111111111",
		Type: "heading_2",
		Data: map[string]json.RawMessage{
			"heading_2": json.RawMessage(`{"rich_text":[{"plain_text":"SSS　マッピング"}]}`),
		},
	}
	headings, err := blockHeadings([]notionAnchorBlock{block})
	if err != nil {
		t.Fatal(err)
	}
	if headings[fragment] != block.ID {
		t.Fatalf("Notion heading produced anchors %v; want %q", headings, fragment)
	}
}

func TestResolveNotionHeadingLinks(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	docs := map[string]*File{"a.md": {PageID: id, SyncBody: "# A\n"}, "b.md": {PageID: id, SyncBody: "# B\n## 設定\n## 設定\n"}}
	entries := map[string]Entry{"file:a.md": {PageID: id}, "file:b.md": {PageID: id}}
	body := "[self](#a) [other](./b.md#設定-1) [missing](./b.md#missing) `[code](./b.md#設定)`"
	got, refs := resolveLinksWithAnchors(body, "a.md", docs, entries)
	if len(refs) != 3 || refs[0].Target != "a.md" || refs[1].Fragment != "設定-1" || refs[2].Reason == "" {
		t.Fatalf("refs: %+v", refs)
	}
	if !strings.Contains(got, anchorPlaceholder(id, refs[0].Token)) || !strings.Contains(got, `\[missing\](./b.md#missing)`) || !strings.Contains(got, "`[code](./b.md#設定)`") {
		t.Fatalf("unexpected output: %q", got)
	}
	_, duplicate := resolveLinksWithAnchors("[one](./b.md#設定) [two](./b.md#設定)", "a.md", docs, entries)
	if len(duplicate) != 2 || duplicate[0].Token == duplicate[1].Token {
		t.Fatal("repeated references must have distinct markers")
	}
}

func TestMissingHeadingProducesWarningAndLiteralText(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	docs := map[string]*File{"b.md": {PageID: id, SyncBody: "# B\n"}}
	entries := map[string]Entry{"file:b.md": {PageID: id}}
	body, refs := resolveLinksWithAnchors("[missing](./b.md#unknown)", "a.md", docs, entries)
	if body != `\[missing\](./b.md#unknown)` || len(refs) != 1 || refs[0].Reason != "heading not found" {
		t.Fatalf("body=%q refs=%+v", body, refs)
	}
	var output strings.Builder
	printPlan(Plan{Actions: []Action{{Kind: "update", Path: "a.md", File: File{Anchors: refs}}}}, func(format string, args ...any) { fmt.Fprintf(&output, format+"\n", args...) })
	if !strings.Contains(output.String(), "warning a.md: anchor b.md#unknown: heading not found") {
		t.Fatalf("missing warning: %s", output.String())
	}
}

func TestAnchorDryRunShowsDeferredResolution(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "# A\n[link](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	var output strings.Builder
	if err := Run(context.Background(), c, api, false, func(format string, args ...any) { fmt.Fprintf(&output, format+"\n", args...) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "check heading links a.md") || strings.Contains(output.String(), "warning a.md:") {
		t.Fatalf("unexpected plan: %s", output.String())
	}
	if api.creates != 0 || api.updates != 0 {
		t.Fatal("dry run wrote to Notion")
	}
}

type testAnchorBlock struct {
	id, kind string
	line     int
}
type testAnchorAPI struct {
	*fakeAPI
	blocks                                       map[string][]testAnchorBlock
	next, patches                                int
	failBeforePatch, failAfterPatch, stripMarker bool
	concurrentEdit                               bool
	failPatchAt                                  int
	normalizeLinks                               bool
}

func (f *testAnchorAPI) rebuild(pageID string) {
	page := f.pages[norm(pageID)]
	var blocks []testAnchorBlock
	for i, line := range strings.Split(page.content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		kind := "paragraph"
		for level := 1; level <= 4; level++ {
			if strings.HasPrefix(line, strings.Repeat("#", level)+" ") {
				kind = fmt.Sprintf("heading_%d", level)
				break
			}
		}
		f.next++
		blocks = append(blocks, testAnchorBlock{id: fmt.Sprintf("%032x", 10000+f.next), kind: kind, line: i})
	}
	f.blocks[norm(pageID)] = blocks
}

func testRichText(line, kind string) []any {
	if strings.HasPrefix(kind, "heading_") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
	}
	var result []any
	add := func(value, link string) {
		if value == "" {
			return
		}
		body := map[string]any{"content": value}
		if link != "" {
			body["link"] = map[string]any{"url": link}
		}
		result = append(result, map[string]any{"type": "text", "plain_text": value, "text": body, "annotations": map[string]any{"bold": false, "italic": false, "strikethrough": false, "underline": false, "code": false, "color": "default"}})
	}
	last := 0
	for _, m := range inlineMarkdownLink.FindAllStringSubmatchIndex(line, -1) {
		start, end, ds, de := m[0], m[1], m[2], m[3]
		labelEnd := strings.Index(line[start:ds], "](")
		if labelEnd < 1 {
			continue
		}
		add(line[last:start], "")
		add(line[start+1:start+labelEnd], strings.Trim(line[ds:de], "<>"))
		last = end
	}
	add(line[last:], "")
	return result
}

func (f *testAnchorAPI) API(ctx context.Context, method, endpoint string, body, out any) error {
	copyResult := func(value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	path, _, _ := strings.Cut(endpoint, "?")
	if method == "GET" && strings.HasPrefix(path, "v1/blocks/") && strings.HasSuffix(path, "/children") {
		id := strings.TrimSuffix(strings.TrimPrefix(path, "v1/blocks/"), "/children")
		p, ok := f.pages[norm(id)]
		if !ok {
			return fmt.Errorf("page not found")
		}
		lines := strings.Split(p.content, "\n")
		var results []any
		for _, block := range f.blocks[norm(id)] {
			results = append(results, map[string]any{"object": "block", "id": block.id, "type": block.kind, "has_children": false, block.kind: map[string]any{"rich_text": testRichText(lines[block.line], block.kind)}})
		}
		return copyResult(map[string]any{"object": "list", "results": results, "has_more": false})
	}
	if method == "PATCH" && strings.HasPrefix(path, "v1/blocks/") {
		if f.failPatchAt > 0 && f.patches+1 == f.failPatchAt {
			f.failPatchAt = 0
			return fmt.Errorf("simulated pre-write failure")
		}
		if f.failBeforePatch {
			f.failBeforePatch = false
			return fmt.Errorf("simulated pre-write failure")
		}
		id := strings.TrimPrefix(path, "v1/blocks/")
		for pageID, blocks := range f.blocks {
			for _, block := range blocks {
				if norm(block.id) != norm(id) {
					continue
				}
				p := f.pages[pageID]
				lines := strings.Split(p.content, "\n")
				payload := body.(map[string]any)[block.kind].(map[string]any)["rich_text"].([]any)
				urls := map[string]string{}
				for _, item := range payload {
					entry := item.(map[string]any)["text"].(map[string]any)
					if link, ok := entry["link"].(map[string]any); ok {
						value := link["url"].(string)
						parsed, _ := url.Parse(value)
						if token := parsed.Query().Get(anchorQuery); token != "" {
							urls[token] = value
						}
					}
				}
				line := lines[block.line]
				original := line
				for _, m := range inlineMarkdownLink.FindAllStringSubmatchIndex(original, -1) {
					old := original[m[2]:m[3]]
					parsed, err := url.Parse(old)
					if err != nil {
						continue
					}
					if updated, ok := urls[parsed.Query().Get(anchorQuery)]; ok {
						if f.stripMarker {
							u, _ := url.Parse(updated)
							u.RawQuery = ""
							updated = u.String()
						}
						line = strings.Replace(line, old, updated, 1)
					}
					if parsed.Query().Get(anchorQuery) == "" && f.normalizeLinks {
						labelEnd := strings.Index(original[m[0]:m[2]], "](")
						if labelEnd < 1 {
							continue
						}
						label := original[m[0]+1 : m[0]+labelEnd]
						for _, ref := range payload {
							entry := ref.(map[string]any)["text"].(map[string]any)
							link, ok := entry["link"].(map[string]any)
							if !ok {
								continue
							}
							next := link["url"].(string)
							if sameAnchorMarker(old, next) && entry["content"] == label {
								line = strings.Replace(line, old, next, 1)
								break
							}
						}
					}
				}
				lines[block.line] = line
				p.content = strings.Join(lines, "\n")
				if f.normalizeLinks {
					p.content = normalizeTestNotionLinks(p.content)
				}
				if f.concurrentEdit {
					f.concurrentEdit = false
					p.content += "\nconcurrent edit"
				}
				f.patches++
				if f.failAfterPatch {
					f.failAfterPatch = false
					return fmt.Errorf("simulated lost response")
				}
				return copyResult(map[string]any{"object": "block", "id": block.id, "type": block.kind})
			}
		}
		return fmt.Errorf("block not found")
	}
	err := f.fakeAPI.API(ctx, method, endpoint, body, out)
	if err != nil {
		return err
	}
	if method == "POST" && endpoint == "v1/pages" {
		f.rebuild(fmt.Sprintf("%032x", f.creates+100))
	}
	if method == "PATCH" && strings.HasPrefix(path, "v1/pages/") && strings.HasSuffix(path, "/markdown") {
		id := strings.TrimSuffix(strings.TrimPrefix(path, "v1/pages/"), "/markdown")
		if f.normalizeLinks {
			p := f.pages[norm(id)]
			p.content = normalizeTestNotionLinks(p.content)
			if err := copyResult(map[string]any{"object": "page_markdown", "id": id, "markdown": p.content, "truncated": false, "unknown_block_ids": []string{}}); err != nil {
				return err
			}
		}
		f.rebuild(id)
	}
	return nil
}

func normalizeTestNotionLinks(body string) string {
	return diffNotionURL.ReplaceAllStringFunc(body, func(value string) string {
		u, err := url.Parse(value)
		if err != nil || u.Query().Get(anchorQuery) == "" {
			return value
		}
		id := diffPageID.FindString(u.Path)
		if !config.ValidID(id) {
			return value
		}
		result := "https://app.notion.com/p/" + norm(id)
		if u.Fragment != "" {
			result += "#" + u.Fragment
		}
		return result
	})
}

func TestSyncNotionHeadingAnchorsAndRefresh(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}}
	a, b := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	write(t, a, "# Start\n\n[設定](./b.md#設定)\n\n[second](./b.md#設定-1)\n\n[self](#start)\n")
	write(t, b, "# B\n\n## 設定\n\nfirst\n\n## 設定\n\n[back](./a.md#start)\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	aID, bID := norm(s.Entries["file:a.md"].PageID), norm(s.Entries["file:b.md"].PageID)
	check := func(sourceID, targetID string, heading int) {
		t.Helper()
		content := api.pages[sourceID].content
		block := api.blocks[targetID][heading].id
		if !strings.Contains(content, "#"+norm(block)+")") {
			t.Fatalf("link in %s does not point to %s: %s", sourceID, block, content)
		}
	}
	check(aID, bID, 1)
	check(aID, bID, 3)
	check(aID, aID, 0)
	check(bID, aID, 0)
	patches := api.patches
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if api.patches != patches {
		t.Fatal("unchanged sync patched anchor links")
	}
	api.rebuild(bID) // Notion recreates the same heading text with new block IDs.
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	check(aID, bID, 1)
	check(aID, bID, 3)
	if api.patches == patches {
		t.Fatal("recreated headings did not refresh inbound links")
	}
	write(t, b, strings.Replace(string(mustRead(t, b)), "## 設定", "### Extra\n\n## 設定", 1))
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	check(aID, bID, 2)
	check(aID, bID, 4)
	check(aID, aID, 0)
	check(bID, aID, 0)
	write(t, b, strings.Replace(string(mustRead(t, b)), "### Extra", "### Changed", 1))
	err = RunFile(context.Background(), c, api, b, true, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "use directory sync") {
		t.Fatalf("expected inbound-link protection, got %v", err)
	}
}

func TestNotionStripsAnchorQueryDuringMarkdownImport(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}, normalizeLinks: true}
	a, b := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	write(t, a, "# A\n\n[案内へ戻る](./b.md#b)\n")
	write(t, b, "# B\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	aID, bID := norm(s.Entries["file:a.md"].PageID), norm(s.Entries["file:b.md"].PageID)
	if !strings.Contains(api.pages[aID].content, "#"+norm(api.blocks[bID][0].id)+")") {
		t.Fatalf("anchor was not resolved: %s", api.pages[aID].content)
	}
	patches := api.patches
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if api.patches != patches {
		t.Fatal("unchanged links were updated")
	}
	write(t, b, strings.Replace(string(mustRead(t, b)), "# B\n", "# B\n\nChanged\n", 1))
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(api.pages[aID].content, "#"+norm(api.blocks[bID][0].id)+")") {
		t.Fatalf("anchor was not refreshed: %s", api.pages[aID].content)
	}
}

func TestRepeatedIdenticalAnchorLinks(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}, normalizeLinks: true}
	a, b := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	write(t, a, "# A\n\n[same](./b.md#b)\n\n[same](./b.md#b)\n")
	write(t, b, "# B\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	aID, bID := norm(s.Entries["file:a.md"].PageID), norm(s.Entries["file:b.md"].PageID)
	anchor := "#" + norm(api.blocks[bID][0].id) + ")"
	if count := strings.Count(api.pages[aID].content, anchor); count != 2 {
		t.Fatalf("expected two resolved links, got %d: %s", count, api.pages[aID].content)
	}
	patches := api.patches
	if err := RunFile(context.Background(), c, api, a, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if api.patches != patches {
		t.Fatal("unchanged links were updated")
	}
	api.rebuild(bID)
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	anchor = "#" + norm(api.blocks[bID][0].id) + ")"
	if count := strings.Count(api.pages[aID].content, anchor); count != 2 {
		t.Fatalf("expected two refreshed links, got %d: %s", count, api.pages[aID].content)
	}
	api.pages[aID].content += "\nremote edit\n"
	if err := Pull(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	local := string(mustRead(t, a))
	if strings.Count(local, "[same](./b.md#b)") != 2 || !strings.Contains(local, "remote edit") {
		t.Fatalf("pull did not preserve links: %s", local)
	}
}

func TestRepeatedIdenticalAnchorLinksInOneBlock(t *testing.T) {
	for _, normalize := range []bool{false, true} {
		for _, separator := range []string{" and ", ""} {
			t.Run(fmt.Sprintf("normalize=%t separator=%q", normalize, separator), func(t *testing.T) {
				c, base, dir := setup(t)
				api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}, normalizeLinks: normalize}
				write(t, filepath.Join(dir, "a.md"), "# A\n\n[same](./b.md#b)"+separator+"[same](./b.md#b)\n")
				write(t, filepath.Join(dir, "b.md"), "# B\n")
				if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
					t.Fatal(err)
				}
				s, err := loadState(c)
				if err != nil {
					t.Fatal(err)
				}
				aID, bID := norm(s.Entries["file:a.md"].PageID), norm(s.Entries["file:b.md"].PageID)
				anchor := "#" + norm(api.blocks[bID][0].id) + ")"
				if count := strings.Count(api.pages[aID].content, anchor); count != 2 {
					t.Fatalf("expected two resolved links, got %d: %s", count, api.pages[aID].content)
				}
			})
		}
	}
}

func TestRepeatedAnchorsWithMixedMarkers(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}}
	name := ""
	for index := 0; index < 100; index++ {
		candidate := fmt.Sprintf("a%d.md", index)
		identity := candidate + "\x00b.md\x00b\x00"
		if hash(identity+"0") > hash(identity+"1") {
			name = candidate
			break
		}
	}
	if name == "" {
		t.Fatal("could not find test token ordering")
	}
	a := filepath.Join(dir, name)
	write(t, a, "# A\n\n[same](./b.md#b)\n\n[same](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	files := []File{*s.Documents[name]}
	prepareLinks(files, s, c.SourcePath)
	page := api.pages[norm(s.Entries["file:"+name].PageID)]
	marker := "?" + anchorQuery + "=" + files[0].Anchors[0].Token
	if !strings.Contains(page.content, marker) {
		t.Fatalf("missing marker: %s", page.content)
	}
	page.content = strings.Replace(page.content, marker, "", 1)
	entry := s.Entries["file:"+name]
	entry.RemoteHash = remoteMarkdownHash(page.content)
	s.Entries["file:"+name] = entry
	s.Active[name] = true
	if err := saveState(c, s); err != nil {
		t.Fatal(err)
	}
	if err := RunFile(context.Background(), c, api, a, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
}

func TestAmbiguousAnchorLabelsStopBeforeBlockUpdates(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}, normalizeLinks: true}
	write(t, filepath.Join(dir, "a.md"), "# A\n\n[same](./b.md#one)\n\n[same](./b.md#two)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n\n## One\n\n## Two\n")
	err := Run(context.Background(), c, api, true, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "ambiguous anchor label") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	if api.patches != 0 {
		t.Fatal("ambiguous links were modified")
	}
}

func TestSingleFileSyncResolvesSelfAnchor(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}}
	filename := filepath.Join(dir, "a.md")
	write(t, filename, "# A\n\n[self](#a)\n")
	if err := RunFile(context.Background(), c, api, filename, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	id := norm(s.Entries["file:a.md"].PageID)
	if !strings.Contains(api.pages[id].content, "#"+norm(api.blocks[id][0].id)+")") {
		t.Fatalf("self link not resolved: %s", api.pages[id].content)
	}
}

func TestMultipleAnchorLinksInOneBlock(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}}
	write(t, filepath.Join(dir, "a.md"), "# A\n\n[first](./b.md#b) and [second](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	aID, bID := norm(s.Entries["file:a.md"].PageID), norm(s.Entries["file:b.md"].PageID)
	fragment := "#" + norm(api.blocks[bID][0].id) + ")"
	if count := strings.Count(api.pages[aID].content, fragment); count != 2 {
		t.Fatalf("expected two resolved links, got %d: %s", count, api.pages[aID].content)
	}
	if api.patches != 1 {
		t.Fatalf("expected one block update, got %d", api.patches)
	}
}

func TestNestedDirectoryHeadingAnchor(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}}
	write(t, filepath.Join(dir, "sample", "index.md"), "# Index\n\n[guide](./child/guide.md#設定)\n")
	write(t, filepath.Join(dir, "sample", "child", "guide.md"), "# Guide\n\n## 設定\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	aID := norm(s.Entries["file:sample/index.md"].PageID)
	bID := norm(s.Entries["file:sample/child/guide.md"].PageID)
	if !strings.Contains(api.pages[aID].content, "#"+norm(api.blocks[bID][1].id)+")") {
		t.Fatalf("nested heading link not resolved: %s", api.pages[aID].content)
	}
}

func TestSingleFileSyncRejectsUnsynchronizedAnchorTarget(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}}
	a, b := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	write(t, a, "# A\n\n[link](./b.md#b)\n")
	write(t, b, "# B\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	write(t, a, strings.Replace(string(mustRead(t, a)), "# A\n", "# A\nExtra\n", 1))
	write(t, b, strings.Replace(string(mustRead(t, b)), "# B\n", "# B\nChanged\n", 1))
	updates := api.updates
	err := RunFile(context.Background(), c, api, a, true, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "unsynchronized local changes") {
		t.Fatalf("expected unsynchronized target error, got %v", err)
	}
	if api.updates != updates {
		t.Fatal("source was written before checking target")
	}
}

func TestSingleFileSyncProtectsPreviouslySyncedLinksRemovedLocally(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}}
	a, b := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	write(t, a, "# A\n\n[link](./b.md#b)\n")
	write(t, b, "# B\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	write(t, a, strings.Replace(string(mustRead(t, a)), "[link](./b.md#b)\n", "", 1))
	write(t, b, strings.Replace(string(mustRead(t, b)), "# B\n", "# B\nChanged\n", 1))
	before := api.updates
	err := RunFile(context.Background(), c, api, b, true, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "still links") {
		t.Fatalf("expected remote inbound-link protection, got %v", err)
	}
	if api.updates != before {
		t.Fatal("target was written despite an existing remote link")
	}
}

func TestSingleFileCreateRejectsInboundHeadingReferences(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}}
	b := filepath.Join(dir, "b.md")
	write(t, b, "# B\n\n[future](./a.md#a)\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(dir, "a.md")
	write(t, a, "# A\n")
	before := api.creates
	err := RunFile(context.Background(), c, api, a, true, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "use directory sync") {
		t.Fatalf("expected inbound-link protection, got %v", err)
	}
	if api.creates != before {
		t.Fatal("page created before checking inbound links")
	}
}

func TestSyncResolvesNewlyAdoptedTarget(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}}
	bID := "22222222-2222-2222-2222-222222222222"
	api.pages[norm(bID)] = &fakePage{parent: rootID}
	write(t, filepath.Join(dir, "a.md"), "# A\n\n[link](./b.md#heading)\n")
	write(t, filepath.Join(dir, "b.md"), "---\nnotion-page-id: "+bID+"\n---\n# Heading\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	aID := norm(s.Entries["file:a.md"].PageID)
	if !strings.Contains(api.pages[aID].content, "#"+norm(api.blocks[norm(bID)][0].id)+")") {
		t.Fatalf("adopted target link not resolved: %s", api.pages[aID].content)
	}
}

func TestAnchorFailureRecovery(t *testing.T) {
	for _, afterWrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("afterWrite=%t", afterWrite), func(t *testing.T) {
			c, base, dir := setup(t)
			api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}, failBeforePatch: !afterWrite, failAfterPatch: afterWrite}
			write(t, filepath.Join(dir, "a.md"), "# A\n\n[link](./b.md#b)\n")
			write(t, filepath.Join(dir, "b.md"), "# B\n")
			if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err == nil {
				t.Fatal("expected failure")
			}
			err := Run(context.Background(), c, api, true, func(string, ...any) {})
			if afterWrite {
				if err == nil || !strings.Contains(err.Error(), "has changed in Notion") {
					t.Fatalf("uncertain write must stop, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("retry after no-write failure: %v", err)
			}
		})
	}
}

func TestAnchorReadbackAcceptsLostMarker(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}, stripMarker: true}
	write(t, filepath.Join(dir, "a.md"), "# A\n\n[link](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	err := Run(context.Background(), c, api, true, func(string, ...any) {})
	if err != nil {
		t.Fatalf("query stripping should not break links: %v", err)
	}
	patches := api.patches
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if api.patches != patches {
		t.Fatal("stripped marker caused repeat updates")
	}
}

func TestAnchorRetryAfterPartialPageUpdate(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}, failPatchAt: 2}
	write(t, filepath.Join(dir, "a.md"), "# A\n\n[first](./b.md#b)\n\n[second](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err == nil {
		t.Fatal("expected failure after first block")
	}
	if api.patches != 1 {
		t.Fatalf("expected one completed update, got %d", api.patches)
	}
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if api.patches != 2 {
		t.Fatalf("expected one resumed update, got %d total", api.patches)
	}
}

func TestAnchorUpdateRejectsConcurrentTextEdit(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}, concurrentEdit: true}
	write(t, filepath.Join(dir, "a.md"), "# A\n\n[link](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	err := Run(context.Background(), c, api, true, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "other content changed during anchor update") {
		t.Fatalf("expected concurrent edit error, got %v", err)
	}
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "has changed in Notion") {
		t.Fatalf("retry must retain conflict, got %v", err)
	}
}

func TestAnchorUpdateStopsIfHeadingIDsChangeWithoutTextChange(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}}
	write(t, filepath.Join(dir, "a.md"), "# A\n\n[link](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	bID := s.Entries["file:b.md"].PageID
	api.rebuild(bID) // Recreated headings with identical Markdown.
	files, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	for i := range files {
		files[i] = *s.Documents[files[i].Path]
		s.Active[files[i].Path] = true
	}
	prepareLinks(files, s, c.SourcePath)
	reads := 0
	api.onMarkdownGet = func() {
		reads++
		if reads == 3 {
			api.rebuild(bID)
		}
	}
	patches := api.patches
	err = syncAnchors(context.Background(), c, api, &s, files, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "heading blocks changed") {
		t.Fatalf("expected heading ID conflict, got %v", err)
	}
	if api.patches != patches {
		t.Fatal("wrote link using stale heading ID")
	}
}

func TestAnchorDiffIgnoresResolvedBlockID(t *testing.T) {
	page := "11111111-1111-1111-1111-111111111111"
	marker := anchorPlaceholder(page, strings.Repeat("a", 64))
	local := "[link](" + marker + ")\n"
	remote := "[link](" + marker + "#22222222222222222222222222222222)\n"
	if d := contentDiff("a.md", remote, local); d != "" {
		t.Fatalf("resolved anchor should not cause diff: %s", d)
	}
	enclosed := "[link](<" + marker + "#22222222222222222222222222222222>)\n"
	if d := contentDiff("a.md", enclosed, local); d != "" {
		t.Fatalf("equivalent angle-bracket URL should not cause diff: %s", d)
	}
}

func TestPullPreservesHeadingLinks(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}}
	a := filepath.Join(dir, "a.md")
	write(t, a, "# A\n\n[link](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	page := api.pages[norm(s.Entries["file:a.md"].PageID)]
	page.content += "\nremote edit\n"
	if err := Pull(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	local := string(mustRead(t, a))
	if !strings.Contains(local, "[link](./b.md#b)") || !strings.Contains(local, "remote edit") {
		t.Fatalf("local anchor or edit missing: %s", local)
	}
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatalf("sync after pull: %v", err)
	}
}

func TestPullPreservesNormalizedHeadingLinks(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}, normalizeLinks: true}
	a := filepath.Join(dir, "a.md")
	write(t, a, "# A\n\n[link](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	page := api.pages[norm(s.Entries["file:a.md"].PageID)]
	page.content += "\nremote edit\n"
	if err := Pull(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	local := string(mustRead(t, a))
	if !strings.Contains(local, "[link](./b.md#b)") || !strings.Contains(local, "remote edit") {
		t.Fatalf("local anchor or edit missing: %s", local)
	}
}

func TestPullDoesNotHideChangedHeadingTarget(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}, normalizeLinks: true}
	a := filepath.Join(dir, "a.md")
	write(t, a, "# A\n\n[link](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n\n## Other\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	aID, bID := norm(s.Entries["file:a.md"].PageID), norm(s.Entries["file:b.md"].PageID)
	page := api.pages[aID]
	page.content = strings.Replace(page.content, "#"+norm(api.blocks[bID][0].id), "#"+norm(api.blocks[bID][1].id), 1)
	page.content += "\nremote edit\n"
	before := string(mustRead(t, a))
	err = Pull(context.Background(), c, api, true, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "changed line containing a link") {
		t.Fatalf("expected changed link conflict, got %v", err)
	}
	if string(mustRead(t, a)) != before {
		t.Fatal("pull overwrote local link")
	}
}

func TestPullDoesNotHideChangedRepeatedHeadingTarget(t *testing.T) {
	c, base, dir := setup(t)
	api := &testAnchorAPI{fakeAPI: base, blocks: map[string][]testAnchorBlock{}, normalizeLinks: true}
	a := filepath.Join(dir, "a.md")
	write(t, a, "# A\n\n[same](./b.md#b)\n\n[same](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n\n## Other\n")
	if err := Run(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	aID, bID := norm(s.Entries["file:a.md"].PageID), norm(s.Entries["file:b.md"].PageID)
	page := api.pages[aID]
	page.content = strings.Replace(page.content, "#"+norm(api.blocks[bID][0].id), "#"+norm(api.blocks[bID][1].id), 1)
	before := string(mustRead(t, a))
	if err := Pull(context.Background(), c, api, true, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "changed line containing a link") {
		t.Fatalf("expected changed link conflict, got %v", err)
	}
	if string(mustRead(t, a)) != before {
		t.Fatal("pull overwrote local links")
	}
}

func TestAnchorURLMatchesNotionCanonicalForms(t *testing.T) {
	page := "11111111-1111-1111-1111-111111111111"
	block := "22222222222222222222222222222222"
	token := strings.Repeat("a", 64)
	desired := anchorPlaceholder(page, token) + "#" + block
	actual := "https://app.notion.com/p/11111111111111111111111111111111?" + anchorQuery + "=" + token + "#block-" + block
	if !sameAnchorURL(actual, desired) {
		t.Fatal("canonical Notion URL was not recognized")
	}
	if sameAnchorMarker("https://example.com/11111111111111111111111111111111?"+anchorQuery+"="+token, desired) {
		t.Fatal("non-Notion host accepted")
	}
	if sameAnchorURL(anchorPlaceholder(page, token), desired) {
		t.Fatal("unresolved placeholder accepted")
	}
}

func TestAnchorBlockPatchRejectsUnsupportedRichText(t *testing.T) {
	page := "11111111-1111-1111-1111-111111111111"
	token := strings.Repeat("a", 64)
	placeholder := anchorPlaceholder(page, token)
	raw, err := json.Marshal(map[string]any{"rich_text": []any{
		map[string]any{"type": "text", "text": map[string]any{"content": "link", "link": map[string]any{"url": placeholder}}},
		map[string]any{"type": "mention", "mention": map[string]any{"type": "user"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	block := notionAnchorBlock{Type: "paragraph", Data: map[string]json.RawMessage{"paragraph": raw}}
	_, _, err = anchorBlockPatch(block, map[string]string{token: placeholder + "#22222222222222222222222222222222"}, map[string]bool{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "non-text") {
		t.Fatalf("expected safe refusal, got %v", err)
	}
}

func TestAnchorBlockPatchMatchesNotionNormalizedPageURL(t *testing.T) {
	page := "11111111-1111-1111-1111-111111111111"
	token := strings.Repeat("a", 64)
	desired := anchorPlaceholder(page, token) + "#22222222222222222222222222222222"
	raw, err := json.Marshal(map[string]any{"rich_text": []any{
		map[string]any{"type": "text", "plain_text": "サンプルの利用方法です。", "text": map[string]any{"content": "サンプルの利用方法です。", "link": nil}},
		map[string]any{"type": "text", "plain_text": "案内へ戻る", "href": "https://app.notion.com/p/11111111111111111111111111111111", "text": map[string]any{"content": "案内へ戻る", "link": map[string]any{"url": "https://app.notion.com/p/11111111111111111111111111111111"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	block := notionAnchorBlock{Type: "paragraph", Data: map[string]json.RawMessage{"paragraph": raw}}
	found := map[string]bool{}
	items, changed, err := anchorBlockPatch(block, map[string]string{token: desired}, found, nil, nil, map[string]AnchorRef{token: {Token: token, Label: "案内へ戻る"}})
	if err != nil || !changed || !found[token] {
		t.Fatalf("changed=%v found=%v err=%v", changed, found, err)
	}
	link := items[1].(map[string]any)["text"].(map[string]any)["link"].(map[string]any)["url"]
	if link != desired {
		t.Fatalf("unexpected link: %v", link)
	}
}

func TestAnchorBlockPatchPreservesSplitLinkFormatting(t *testing.T) {
	page := "11111111-1111-1111-1111-111111111111"
	token := strings.Repeat("a", 64)
	desired := anchorPlaceholder(page, token) + "#22222222222222222222222222222222"
	actual := "https://app.notion.com/p/11111111111111111111111111111111"
	raw, err := json.Marshal(map[string]any{"rich_text": []any{
		map[string]any{"type": "text", "text": map[string]any{"content": "bold", "link": map[string]any{"url": actual}}, "annotations": map[string]any{"bold": true}},
		map[string]any{"type": "text", "text": map[string]any{"content": " plain", "link": map[string]any{"url": actual}}, "annotations": map[string]any{"bold": false}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	block := notionAnchorBlock{Type: "paragraph", Data: map[string]json.RawMessage{"paragraph": raw}}
	items, changed, err := anchorBlockPatch(block, map[string]string{token: desired}, map[string]bool{}, nil, nil, map[string]AnchorRef{token: {Label: "bold plain"}})
	if err != nil || !changed || len(items) != 2 {
		t.Fatalf("changed=%v items=%v err=%v", changed, items, err)
	}
	for _, item := range items {
		link := item.(map[string]any)["text"].(map[string]any)["link"].(map[string]any)["url"]
		if link != desired {
			t.Fatalf("link not updated: %v", link)
		}
	}
	if string(items[0].(map[string]any)["annotations"].(json.RawMessage)) != `{"bold":true}` {
		t.Fatal("formatting was not preserved")
	}
}

type anchorClientFunc func(context.Context, string, string, any, any) error

func (f anchorClientFunc) API(ctx context.Context, method, endpoint string, body, out any) error {
	return f(ctx, method, endpoint, body, out)
}

func TestListAnchorBlocksPagination(t *testing.T) {
	page := "11111111-1111-1111-1111-111111111111"
	count := 0
	api := anchorClientFunc(func(_ context.Context, method, endpoint string, _ any, out any) error {
		count++
		if method != "GET" {
			return fmt.Errorf("unexpected method")
		}
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return err
		}
		cursor := parsed.Query().Get("start_cursor")
		id := "22222222222222222222222222222222"
		response := map[string]any{"object": "list", "results": []any{map[string]any{"object": "block", "id": id, "type": "paragraph", "has_children": false, "paragraph": map[string]any{"rich_text": []any{}}}}, "has_more": true, "next_cursor": "next cursor/+"}
		if cursor != "" {
			response["has_more"] = false
			response["next_cursor"] = nil
			response["results"] = []any{map[string]any{"object": "block", "id": "33333333333333333333333333333333", "type": "paragraph", "has_children": false, "paragraph": map[string]any{"rich_text": []any{}}}}
		}
		data, err := json.Marshal(response)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	})
	blocks, err := listAnchorBlocks(context.Background(), api, page)
	if err != nil || len(blocks) != 2 || count != 2 {
		t.Fatalf("blocks=%v count=%d err=%v", blocks, count, err)
	}
}

func TestHeadingReferencesIgnoreExamples(t *testing.T) {
	body := "[real](./guide.md#settings)\n`[inline](./guide.md#settings)`\n```\n[example](./guide.md#settings)\n```\n"
	if !referencesHeading(body, "dir/index.md", "dir/guide.md") {
		t.Fatal("expected real reference")
	}
	if referencesHeading(strings.TrimPrefix(body, "[real](./guide.md#settings)\n"), "dir/index.md", "dir/guide.md") {
		t.Fatal("code examples treated as references")
	}
}
