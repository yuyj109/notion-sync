package sync

import (
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
	runGit("add", "src/a b.go")
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
}

func TestUnresolvedLinkKeepsLiteralMarkdown(t *testing.T) {
	got := resolveLinks("[存在しないファイル](../xxxx.x)", "doc.md", nil, nil)
	want := `\[存在しないファイル\](../xxxx.x)`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
