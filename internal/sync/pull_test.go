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

func TestPullBodyPreservesCRLF(t *testing.T) {
	f := File{Body: "one\r\n", SyncBody: "one\r\n"}
	got, err := pullBody(f, "one\ntwo\n")
	if err != nil || got != "one\r\ntwo\r\n" {
		t.Fatalf("got %q, err %v", got, err)
	}
}
