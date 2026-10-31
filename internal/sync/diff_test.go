package sync

import (
	"regexp"
	"strings"
	"testing"
)

func TestNumberedDiffUsesOriginalLineNumbers(t *testing.T) {
	body := "# title\n\nkeep\nnew\n"
	f := File{Body: body, Raw: []byte("---\ntitle: Sample\n---\n" + body)}
	d := makeContentDiff("a.md", "<empty-block/>\n# title\nkeep\nold\n", body, 0, sourceBodyOffset(f))
	for _, pattern := range []string{`(?m)^\s+2\s+4 │ # title$`, `(?m)^\s+3\s+6 │ keep$`, `(?m)^-\s+4\s+│ old$`, `(?m)^\+\s+7 │ new$`} {
		if !regexp.MustCompile(pattern).MatchString(d.Numbered) {
			t.Fatalf("missing %q in:\n%s", pattern, d.Numbered)
		}
	}
	if d.String() != d.Plain || strings.Contains(d.Plain, "│") {
		t.Fatal("plain diff changed")
	}
}

func TestContentDiff(t *testing.T) {
	got := contentDiff("a.md", "one\nremote\nlast\n", "one\nlocal\nlast\n")
	want := "--- notion/a.md (normalized)\n+++ local/a.md (normalized)\n@@ -1,3 +1,3 @@\n one\n-remote\n+local\n last"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if contentDiff("a", "same\n", "same\n") != "" {
		t.Fatal("identical text has a diff")
	}
	if got := contentDiff("a", "", "new"); !strings.Contains(got, "@@ -0,0 +1,1 @@\n+new") {
		t.Fatalf("unexpected insertion: %q", got)
	}
}

func TestContentDiffRedactsImageSignatures(t *testing.T) {
	got := contentDiff("a", "![x](https://example.com/a.png?signature=secret)", "different")
	if strings.Contains(got, "secret") || !strings.Contains(got, "https://example.com/a.png") {
		t.Fatalf("unexpected diff: %q", got)
	}
}

func TestContentDiffPreservesUnchangedLinesBetweenChanges(t *testing.T) {
	remote := "header\none\ncommon link\nthree\nfooter\n"
	local := "header\n\none\ncommon link\n\nthree\nfooter\n"
	got := contentDiff("a.md", remote, local)
	if got != "" {
		t.Fatalf("formatting-only blank lines should be ignored: %s", got)
	}
}

func TestContentDiffIgnoresNotionFormatting(t *testing.T) {
	remote := "<empty-block/>\n# title\n- <mention-page url=\"https://app.notion.com/p/409b955df763813387b7c84f13242ac3\"/>\n<empty-block/>\naaaa\n<empty-block/>"
	local := "# title\n\n- <mention-page url=\"https://notion.so/409b955d-f763-8133-87b7-c84f13242ac3\">Example</mention-page>\n"
	got := contentDiff("example.md", remote, local)
	if !strings.Contains(got, "\n-aaaa") || strings.Contains(got, "\n-<empty-block/>") || strings.Contains(got, "\n-- <mention-page") || strings.Contains(got, "\n+- <mention-page") {
		t.Fatalf("unexpected differences: %s", got)
	}
}

func TestContentDiffPreservesBlankLinesInCode(t *testing.T) {
	remote := "```\none\n\ntwo\n```\n"
	local := "```\none\ntwo\n```\n"
	got := contentDiff("a.md", remote, local)
	if !strings.Contains(got, "\n-\n") {
		t.Fatalf("code blank line must remain visible: %s", got)
	}
}

func TestContentDiffReportsDifferentMentions(t *testing.T) {
	a := `<mention-page url="https://notion.so/409b955d-f763-8133-87b7-c84f13242ac3">A</mention-page>`
	b := `<mention-page url="https://notion.so/409b955d-f763-8133-87b7-c84f13242ac4">B</mention-page>`
	if got := contentDiff("a", a, b); !strings.Contains(got, "-"+a) || !strings.Contains(got, "+"+b) {
		t.Fatalf("different page IDs must be shown: %s", got)
	}
}

func TestContentDiffSeparatesDistantChanges(t *testing.T) {
	remote := "old1\n" + strings.Repeat("same\n", 12) + "old2\n"
	local := "new1\n" + strings.Repeat("same\n", 12) + "new2\n"
	got := contentDiff("a.md", remote, local)
	if strings.Count(got, "@@ ") != 2 {
		t.Fatalf("expected separate hunks: %s", got)
	}
	if strings.Contains(got, "-same") || strings.Contains(got, "+same") {
		t.Fatalf("common lines marked changed: %s", got)
	}
}
