package sync

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPullOnlyChangesConflictingText(t *testing.T) {
	c, api, dir := setup(t)
	a, b := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	write(t, a, "# A\n\n[link](./b.md)\n\nold text\n")
	write(t, b, "# B\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	id := s.Entries["file:a.md"].PageID
	api.pages[norm(id)].content = strings.Replace(api.pages[norm(id)].content, "old text", "new text\nadded", 1)
	before := string(mustRead(t, a))
	other := string(mustRead(t, b))
	if err := Pull(context.Background(), c, api, false, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, a)) != before {
		t.Fatal("dry run changed file")
	}
	if err := Pull(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	got := string(mustRead(t, a))
	if !strings.Contains(got, "[link](./b.md)") || !strings.Contains(got, "new text\nadded\n") || strings.Contains(got, "old text") {
		t.Fatalf("unexpected result: %s", got)
	}
	if string(mustRead(t, b)) != other {
		t.Fatal("unconflicted file changed")
	}
	if err := run(c, api, true); err != nil {
		t.Fatalf("sync after pull: %v", err)
	}
}

func TestPullFileLimitsScope(t *testing.T) {
	c, api, dir := setup(t)
	a, b := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	write(t, a, "a\n")
	write(t, b, "b\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	api.pages[norm(s.Entries["file:a.md"].PageID)].content = "a\nnew\n"
	api.pages[norm(s.Entries["file:b.md"].PageID)].content = "b\nother\n"
	beforeA, beforeB := string(mustRead(t, a)), string(mustRead(t, b))
	if err := PullFile(context.Background(), c, api, a, false, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, a)) != beforeA {
		t.Fatal("preview changed file")
	}
	if err := PullFile(context.Background(), c, api, a, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mustRead(t, a)), "a\nnew\n") || string(mustRead(t, b)) != beforeB {
		t.Fatal("wrong pull scope")
	}
	if api.updates != 2 {
		t.Fatal("pull wrote to Notion")
	}
}

func TestPullMediaAppendRequiresExactRemoteBaseline(t *testing.T) {
	image := Image{Key: "image-1", Path: "a.svg", Hash: "image-hash"}
	diagram := Image{Key: "image-2", Path: "a.drawio.svg", Hash: "diagram-hash"}
	id := "22222222-2222-2222-2222-222222222222"
	f := File{Body: "![a](a.svg)\n![b](a.drawio.svg)\n- TEST2\n", Images: []Image{image, diagram}}
	baseline := "![a](https://files.notion.so/a.svg)\n<embed src=\"\"></embed>\n- TEST2"
	e := Entry{RemoteHash: remoteMarkdownHash(baseline), ImageHashes: map[string]string{"image-1": image.Hash, "image-2": hash("drawio-embed:" + diagram.Hash)}, ImageBlocks: map[string]string{"image-2": id}, managedEmbeds: map[string]bool{norm(id): true}}
	remote := baseline + "\n- TEST3\n   - DDDD\n   - EEEE"
	body, _, err := pullMediaAppend(f, e, remote)
	if err != nil || body != f.Body+"- TEST3\n   - DDDD\n   - EEEE\n" {
		t.Fatalf("unexpected append %q: %v", body, err)
	}
	for _, changed := range []string{strings.Replace(remote, "a.svg", "changed.svg", 1), strings.Replace(remote, "TEST2", "changed", 1), remote + "\n![new](x.png)"} {
		if _, _, err := pullMediaAppend(f, e, changed); err == nil {
			t.Fatalf("unsafe change accepted: %s", changed)
		}
	}
	f.Images[0].Hash = "changed"
	if _, _, err := pullMediaAppend(f, e, remote); err == nil {
		t.Fatal("changed local image accepted")
	}
}

func TestPullFileAppendsTextWithImageAndManagedEmbed(t *testing.T) {
	c, api, dir := setup(t)
	c.Sync.MetadataFormat = "comment"
	c.Sync.DrawioEmbed = true
	file := filepath.Join(dir, "sample.md")
	write(t, file, "# Sample\n\n![image](a.svg)\n\n![diagram](a.drawio.svg)\n\n- TEST2\n")
	write(t, filepath.Join(dir, "a.svg"), "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>")
	write(t, filepath.Join(dir, "a.drawio.svg"), "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>")
	files, err := Scan(c)
	if err != nil {
		t.Fatal(err)
	}
	imageID := "11111111-1111-1111-1111-111111111111"
	embedID := "22222222-2222-2222-2222-222222222222"
	baseline := "# Sample\n![image](https://files.notion.so/a.svg#notion_record=block." + imageID + ".space)\n<embed src=\"\"></embed>\n- TEST2"
	entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", Target: "hierarchy:", LocalHash: files[0].Hash, RemoteHash: remoteMarkdownHash(baseline), ImageHashes: map[string]string{"image-1": files[0].Images[0].Hash, "image-2": hash("drawio-embed:" + files[0].Images[1].Hash)}, ImageBlocks: map[string]string{"image-1": imageID, "image-2": embedID}}
	s := State{RootID: rootID, Entries: map[string]Entry{"file:sample.md": entry}, Documents: map[string]*File{"sample.md": &files[0]}, Active: map[string]bool{"sample.md": true}, sharedMigrated: true}
	if err := saveState(c, s); err != nil {
		t.Fatal(err)
	}
	api.pages[norm(rootID)].content = strings.Replace(baseline, `src=""`, `src="https://viewer.example/diagram"`, 1) + "\n- TEST3\n   - DDDD\n   - EEEE"
	client := &embedAndPagesClient{fakeAPI: api, embedID: embedID}
	before := string(mustRead(t, file))
	if err := PullFile(context.Background(), c, client, file, false, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, file)) != before {
		t.Fatal("preview modified file")
	}
	if err := PullFile(context.Background(), c, client, file, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	got := string(mustRead(t, file))
	if !strings.Contains(got, "![image](a.svg)") || !strings.Contains(got, "![diagram](a.drawio.svg)") || !strings.Contains(got, "- TEST3\n   - DDDD\n   - EEEE") {
		t.Fatalf("unexpected content: %s", got)
	}
	if api.updates != 0 {
		t.Fatal("pull wrote to Notion")
	}
	if err := RunFile(context.Background(), c, client, file, false, func(string, ...any) {}); err != nil {
		t.Fatalf("sync after pull: %v", err)
	}
}

func TestPullCommentMetadata(t *testing.T) {
	c, api, dir := setup(t)
	c.Sync.MetadataFormat = "comment"
	file := filepath.Join(dir, "a.md")
	write(t, file, "original\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	api.pages[norm(s.Entries["file:a.md"].PageID)].content = "updated\n"
	if err := Pull(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	data := string(mustRead(t, file))
	if !strings.HasPrefix(data, "<!-- notion-sync\n") || !strings.HasSuffix(data, "-->\nupdated\n") {
		t.Fatalf("pull result: %q", data)
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
}

func TestPullRejectsIndependentLocalChanges(t *testing.T) {
	c, api, dir := setup(t)
	a := filepath.Join(dir, "a.md")
	write(t, a, "original\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	api.pages[norm(s.Entries["file:a.md"].PageID)].content = "remote\n"
	local := strings.Replace(string(mustRead(t, a)), "original\n", "local\n", 1)
	write(t, a, local)
	if err := Pull(context.Background(), c, api, true, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "both changed") {
		t.Fatalf("expected conflict, got %v", err)
	}
	if string(mustRead(t, a)) != local {
		t.Fatal("local file changed")
	}
}

func TestPullRebaselinesAlreadyMatchingContent(t *testing.T) {
	c, api, dir := setup(t)
	a := filepath.Join(dir, "a.md")
	write(t, a, "original\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	api.pages[norm(s.Entries["file:a.md"].PageID)].content = "original\nadded\n"
	write(t, a, strings.Replace(string(mustRead(t, a)), "original\n", "original\nadded\n", 1))
	if err := Pull(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if err := run(c, api, true); err != nil {
		t.Fatalf("sync after pull: %v", err)
	}
}

func TestPullRejectsImagesAndComments(t *testing.T) {
	for _, body := range []string{"text\n<!-- private -->\n", "![x](image.png)\n"} {
		f := File{Body: body, SyncBody: withoutHTMLComments(body)}
		if strings.HasPrefix(body, "![") {
			f.Images = []Image{{Path: "image.png"}}
		}
		if _, err := pullBody(f, "remote\n"); err == nil {
			t.Fatalf("expected unsupported content error for %q", body)
		}
	}
}

func TestPullBodyPreservesFinalNewlineAndLocalLinks(t *testing.T) {
	f := File{Body: "# title\n\n[link](./b.md)\n", SyncBody: "# title\n\n<mention-page url=\"https://notion.so/409b955d-f763-8133-87b7-c84f13242ac3\">link</mention-page>\n"}
	remote := "# title\n<mention-page url=\"https://app.notion.com/p/409b955df763813387b7c84f13242ac3\"/>\nextra\n"
	got, err := pullBody(f, remote)
	if err != nil || got != f.Body+"extra\n" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestPullStopsIfNotionChangesAfterPreview(t *testing.T) {
	c, api, dir := setup(t)
	a := filepath.Join(dir, "a.md")
	write(t, a, "original\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	s, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	p := api.pages[norm(s.Entries["file:a.md"].PageID)]
	p.content = "first edit\n"
	reads := 0
	api.onMarkdownGet = func() {
		reads++
		if reads == 2 {
			p.content = "second edit\n"
		}
	}
	before := string(mustRead(t, a))
	if err := Pull(context.Background(), c, api, true, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "changed during pull") {
		t.Fatalf("expected conflict, got %v", err)
	}
	if string(mustRead(t, a)) != before {
		t.Fatal("local file changed after remote edit")
	}
}

func TestPullRejectsChangedLink(t *testing.T) {
	f := File{Body: "[link](./b.md)\n", SyncBody: "[link](https://example.com/b)\n"}
	if _, err := pullBody(f, "[different](https://example.com/c)\n"); err == nil {
		t.Fatal("changed link must not be overwritten")
	}
}

func TestPullRejectsNewRemoteLinksAndComments(t *testing.T) {
	f := File{Body: "plain\n", SyncBody: "plain\n"}
	for _, remote := range []string{"[link](https://example.com)\n", "<https://example.com>\n", "[link][ref]\n", "plain\n<!-- remote -->\n"} {
		if _, err := pullBody(f, remote); err == nil {
			t.Fatalf("accepted unsafe remote content %q", remote)
		}
	}
}

func TestPullBodyPreservesCRLF(t *testing.T) {
	f := File{Body: "one\r\n", SyncBody: "one\r\n"}
	got, err := pullBody(f, "one\ntwo\n")
	if err != nil || got != "one\r\ntwo\r\n" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestPullPreservesRemoteParagraphBreak(t *testing.T) {
	f := File{Body: "one\n", SyncBody: "one\n"}
	got, err := pullBody(f, "one\n\nnew\n")
	if err != nil || got != "one\n\nnew\n" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestPullRemovesRemoteParagraphBreak(t *testing.T) {
	f := File{Body: "one\n\ntwo\n", SyncBody: "one\n\ntwo\n"}
	got, err := pullBody(f, "one\ntwo\n")
	if err != nil || got != "one\ntwo\n" {
		t.Fatalf("got %q, err %v", got, err)
	}
	if d := makeContentDiffWithBlanks("a.md", f.Body, "one\ntwo\n", 0, 0, true); d.Plain == "" {
		t.Fatal("paragraph break change missing from preview")
	}
}
