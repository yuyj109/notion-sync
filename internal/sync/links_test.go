package sync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalLocalLinksUseCommittedGitHubFiles(t *testing.T) {
	dir := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-q")
	runGit("remote", "add", "origin", "git@github.com:example/project.git")
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "a b.go"), []byte("package a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "guide.md"), []byte("# Guide\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "src/a b.go", "src/guide.md")
	runGit("-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "initial")
	commit := runGit("rev-parse", "HEAD")
	repo := openGitRepository(filepath.Join(dir, "tmp"))
	input := "[source](../src/a%20b.go) [missing](../src/missing.go) `\u005bcode](../src/a%20b.go)` [web](https://example.com)"
	got := resolveLinks(input, "doc.md", nil, nil, repo)
	want := "[source](https://github.com/example/project/blob/" + commit + "/src/a%20b.go) \\[missing\\](../src/missing.go) `[code](../src/a%20b.go)` [web](https://example.com)"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "new.go"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := resolveLinks("[new](../src/new.go)", "doc.md", nil, nil, repo); got != `\[new\](../src/new.go)` {
		t.Fatalf("uncommitted file: %q", got)
	}
	for _, tc := range []struct{ input, suffix string }{
		{"[section](../src/guide.md#introduction)", "#introduction"},
		{"[section](../src/guide.md#%E6%97%A5%E6%9C%AC%E8%AA%9E)", "#%E6%97%A5%E6%9C%AC%E8%AA%9E"},
	} {
		got := resolveLinks(tc.input, "doc.md", nil, nil, repo)
		want := "[section](https://github.com/example/project/blob/" + commit + "/src/guide.md" + tc.suffix + ")"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	if got := resolveLinks("[missing](../src/missing.md#section)", "doc.md", nil, nil, repo); got != `\[missing\](../src/missing.md#section)` {
		t.Fatalf("unresolved anchor: %q", got)
	}
}

func TestHeadingLinksBecomePageMentions(t *testing.T) {
	selfID := "11111111-1111-1111-1111-111111111111"
	otherID := "22222222-2222-2222-2222-222222222222"
	docs := map[string]*File{"a.md": {PageID: selfID}, "b.md": {PageID: otherID}}
	input := "[self](#missing) [other](./b.md#設定) [missing](./missing.md#x) `[code](./b.md#x)` [query](./b.md?q=1#x)"
	want := `self - <mention-page url="` + pageURL(selfID) + `"/> other - <mention-page url="` + pageURL(otherID) + `"/> \[missing\](./missing.md#x) ` + "`[code](./b.md#x)`" + ` [query](./b.md?q=1#x)`
	if got := resolveLinks(input, "a.md", docs, nil); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHeadingLinkKeepsLabelNextToPageMention(t *testing.T) {
	id := "22222222-2222-2222-2222-222222222222"
	docs := map[string]*File{"deployments.md": {PageID: id, SyncBody: "# デプロイ手順\n"}}
	input := "- [ビルド](./deployments.md#ビルド)\n- [リリース](./deployments.md#リリース)\n"
	want := "- ビルド - <mention-page url=\"" + pageURL(id) + "\"/>\n- リリース - <mention-page url=\"" + pageURL(id) + "\"/>\n"
	if got := resolveLinks(input, "home.md", docs, nil); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPageSyncDoesNotValidateLinkedPage(t *testing.T) {
	c, api, dir := setup(t)
	a := filepath.Join(dir, "a.md")
	write(t, a, "# A\n\n[section](./b.md#missing)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	aID, bID := state.Entries["file:a.md"].PageID, state.Entries["file:b.md"].PageID
	api.pages[norm(bID)].content = "# Edited in Notion"
	write(t, a, strings.Replace(string(mustRead(t, a)), "# A\n", "# A changed\n", 1))
	if err := RunFile(context.Background(), c, api, a, true, func(string, ...any) {}, true); err != nil {
		t.Fatal(err)
	}
	if got := api.pages[norm(aID)].content; !strings.Contains(got, `section - <mention-page url="`+pageURL(bID)+`"/>`) {
		t.Fatalf("missing page mention: %s", got)
	}
	if api.pages[norm(bID)].content != "# Edited in Notion" {
		t.Fatal("linked page was modified")
	}
}

func TestSyncMentionsNewPagesWithHeadingLinks(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "# A\n\n[self](#a) [other](./b.md#missing)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n\n[back](./a.md#a)\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	a, b := state.Entries["file:a.md"].PageID, state.Entries["file:b.md"].PageID
	contentA, contentB := api.pages[norm(a)].content, api.pages[norm(b)].content
	if !strings.Contains(contentA, `self - <mention-page url="`+pageURL(a)+`"/>`) || !strings.Contains(contentA, `other - <mention-page url="`+pageURL(b)+`"/>`) || !strings.Contains(contentB, `back - <mention-page url="`+pageURL(a)+`"/>`) {
		t.Fatalf("new pages were not mentioned: a=%q b=%q", contentA, contentB)
	}
}

func TestSyncMigratesExistingHeadingLinks(t *testing.T) {
	c, api, dir := setup(t)
	write(t, filepath.Join(dir, "a.md"), "# A\n\n[section](./b.md#b)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	a, b := state.Entries["file:a.md"], state.Entries["file:b.md"]
	legacy := "# A\n\n[section](" + pageURL(b.PageID) + "?notion-sync-anchor=" + strings.Repeat("a", 64) + "#" + strings.Repeat("b", 32) + ")\n"
	api.pages[norm(a.PageID)].content = legacy
	a.LocalHash = hash("legacy representation")
	a.RemoteHash = remoteMarkdownHash(legacy)
	state.Entries["file:a.md"] = a
	state.Active["a.md"] = true
	if err := saveState(c, state); err != nil {
		t.Fatal(err)
	}
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	if got := api.pages[norm(a.PageID)].content; strings.Contains(got, "notion-sync-anchor") || !strings.Contains(got, `section - <mention-page url="`+pageURL(b.PageID)+`"/>`) {
		t.Fatalf("legacy link was not migrated: %s", got)
	}
}

func TestSyncMentionsNewlyAdoptedPage(t *testing.T) {
	c, api, dir := setup(t)
	bID := "22222222-2222-2222-2222-222222222222"
	api.pages[norm(bID)] = &fakePage{parent: rootID}
	write(t, filepath.Join(dir, "a.md"), "# A\n\n[link](./b.md#heading)\n")
	write(t, filepath.Join(dir, "b.md"), "---\nnotion-page-id: "+bID+"\n---\n# Heading\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	got := api.pages[norm(state.Entries["file:a.md"].PageID)].content
	if !strings.Contains(got, `link - <mention-page url="`+pageURL(bID)+`"/>`) {
		t.Fatalf("adopted page not mentioned: %s", got)
	}
}

func TestPullKeepsLocalHeadingLink(t *testing.T) {
	c, api, dir := setup(t)
	a := filepath.Join(dir, "a.md")
	write(t, a, "# A\n\n[link](./b.md#section)\n")
	write(t, filepath.Join(dir, "b.md"), "# B\n")
	if err := run(c, api, true); err != nil {
		t.Fatal(err)
	}
	state, err := loadState(c)
	if err != nil {
		t.Fatal(err)
	}
	page := api.pages[norm(state.Entries["file:a.md"].PageID)]
	page.content += "\nRemote addition\n"
	if err := Pull(context.Background(), c, api, true, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	got := string(mustRead(t, a))
	if !strings.Contains(got, "[link](./b.md#section)") || !strings.Contains(got, "Remote addition") {
		t.Fatalf("unexpected pulled content: %s", got)
	}
}

func TestUnresolvedLinkKeepsLiteralMarkdown(t *testing.T) {
	got := resolveLinks("[存在しないファイル](../xxxx.x)", "doc.md", nil, nil)
	want := `\[存在しないファイル\](../xxxx.x)`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
