package sync

import (
	"net/url"
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

func TestContentDiffRedactsNestedProxySignatures(t *testing.T) {
	proxy := func(secret, version string) string {
		source := "https://s3.us-west-2.amazonaws.com/secure.notion-static.com/a.png?X-Amz-Signature=" + secret + "&versionId=" + version
		return "![x](https://www.notion.so/image/" + url.PathEscape(source) + "?token=outer-secret)"
	}
	d := makeContentDiff("a", proxy("remote-secret", "one"), proxy("local-secret", "two"), 0, 0)
	for _, output := range []string{d.Plain, d.Numbered} {
		if strings.Contains(output, "secret") || !strings.Contains(output, "versionId") || !strings.Contains(output, "one") || !strings.Contains(output, "two") {
			t.Fatalf("unsafe proxy diff: %q", output)
		}
	}
}

func TestContentDiffRedactsNestedProxyWithUserInfoOrRelativeScheme(t *testing.T) {
	source := url.PathEscape("https://example.com/a.png?token=nested-secret&version=1")
	proxyWithUser := (&url.URL{Scheme: "https", Host: "www.notion.so", Path: "/image/", User: url.UserPassword("user", "password")}).String()
	for _, proxy := range []string{proxyWithUser, "//www.notion.so/image/"} {
		got := contentDiff("a", "![x]("+proxy+source+")", "different")
		if strings.Contains(got, "secret") || strings.Contains(got, "password") || !strings.Contains(got, "version") {
			t.Fatalf("unsafe proxy diff: %q", got)
		}
	}
}

func TestContentDiffRedactsMalformedProxySource(t *testing.T) {
	input := "![x](https://www.notion.so/image/" + url.PathEscape("https://files.example/a.png?token=secret&bad=%zz") + ")"
	if got := contentDiff("a", input, "different"); strings.Contains(got, "secret") {
		t.Fatalf("malformed source leaked: %q", got)
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
	got := contentDiff("a", "![x](https://example.com/a.png?signature=secret&version=1)", "different")
	if strings.Contains(got, "secret") || !strings.Contains(got, "https://example.com/a.png?version=1") {
		t.Fatalf("unexpected diff: %q", got)
	}
}

func TestContentDiffRedactsRelativeAndUserInfoCredentials(t *testing.T) {
	imageWithUser := (&url.URL{Scheme: "https", Host: "example.com", Path: "/a.png", RawQuery: "version=1", User: url.UserPassword("user", "remote-secret")}).String()
	for _, image := range []string{
		"![x](//files.notion.so/a.png?signature=remote-secret&version=1)",
		"![x](" + imageWithUser + ")",
		"![x](a.png?token=remote-secret&version=1)",
	} {
		d := makeContentDiff("a", image, "different", 0, 0)
		for _, output := range []string{d.Plain, d.Numbered} {
			if strings.Contains(output, "remote-secret") || !strings.Contains(output, "version=1") {
				t.Fatalf("unsafe diff for %q: %q", image, output)
			}
		}
	}
}

func TestContentDiffRedactsReferenceAndHTMLImageURLs(t *testing.T) {
	for _, image := range []string{
		"![picture][photo]\n\n[photo]: https://example.com/a.png?token=example-secret&version=1\n",
		"![picture][photo]\n\n[photo]: <images/a.png?token=example-secret&version=1>\n",
		"![picture][photo]\n\n> [photo]:\n>  images/a.png?token=example-secret&version=1\n",
		`<img src="https://example.com/a.png?version=1&amp;X-Amz-Credential=example-secret">`,
		`<img src='images/a.png?token=example-secret&amp;version=1'>`,
		`<img srcset="images/a.png?token=example-secret&amp;version=1 1x, //example.com/b?signature=second-secret 2x">`,
		`![x](https://example.com/a.png?version=1&amp;X-Amz-Credential=example-secret)`,
	} {
		for _, d := range []DiffOutput{makeContentDiff("a", image, "different", 0, 0), makeContentDiff("a", "different", image, 0, 0)} {
			for _, output := range []string{d.Plain, d.Numbered} {
				if strings.Contains(output, "secret") || !strings.Contains(output, "version=1") {
					t.Fatalf("unsafe diff for %q: %q", image, output)
				}
			}
		}
	}
}

func TestContentDiffRedactsSigOnBothSides(t *testing.T) {
	d := makeContentDiff("a", "![x](https://example.com/a.png?sig=remote-secret&version=1)", "![x](https://example.com/a.png?SIG=local-secret&version=2)", 0, 0)
	for _, output := range []string{d.Plain, d.Numbered} {
		if strings.Contains(output, "remote-secret") || strings.Contains(output, "local-secret") || !strings.Contains(output, "version=1") || !strings.Contains(output, "version=2") {
			t.Fatalf("unsafe diff: %q", output)
		}
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

func TestConflictDiffMatchesRecordedImageAndListDepth(t *testing.T) {
	id := "40cb955d-f763-816d-a00b-f7bd3db6752d"
	img := Image{Alt: "SVG", SourceLine: "![SVG](images/sample.svg)", Key: "image-1", Hash: "hash"}
	f := File{Images: []Image{img}}
	e := Entry{ImageBlocks: map[string]string{"image-1": id}, ImageHashes: map[string]string{"image-1": "hash"}}
	remote := "# sample\n![SVG](https://files.notion.so/sample.svg?signature=secret#notion_record=block." + id + ".workspace)\n- TEST1\n   - BBBB\n   - CCCC\n- TEST3\n   - DDDD\n"
	local := "# sample\n![SVG](images/sample.svg)\n- TEST1\n  - BBBB\n  - CCCC\n"
	d := makeDisplayDiff("sample.md", remote, local, 0, 0, false, &f, &e)
	for _, output := range []string{d.Plain, d.Numbered} {
		if !strings.Contains(output, "- TEST3") || !strings.Contains(output, "- DDDD") || !strings.Contains(output, "not independently verified") || strings.Contains(output, "secret") {
			t.Fatalf("missing change or image warning: %s", output)
		}
		for _, unwanted := range []string{"-![SVG]", "+![SVG]", "-   - BBBB", "+  - BBBB", "-   - CCCC", "+  - CCCC"} {
			if strings.Contains(output, unwanted) {
				t.Fatalf("unnecessary difference %q: %s", unwanted, output)
			}
		}
	}
	// Changed local image content must not be paired away.
	f.Images[0].Hash = "changed"
	d = makeDisplayDiff("sample.md", remote, local, 0, 0, false, &f, &e)
	if !strings.Contains(d.Plain, "-![SVG]") || !strings.Contains(d.Plain, "+![SVG]") {
		t.Fatalf("changed image was hidden: %s", d.Plain)
	}
	f.Images[0].Hash = "hash"
	imageOnly, _, _ := strings.Cut(remote, "- TEST1")
	localImageOnly, _, _ := strings.Cut(local, "- TEST1")
	d = makeDisplayDiff("sample.md", imageOnly, localImageOnly, 0, 0, false, &f, &e)
	if !strings.Contains(d.Plain, "may have changed") {
		t.Fatalf("missing uncertainty warning when no other differences: %s", d.Plain)
	}
}

func TestListDiffKeepsStructuralAndCodeChanges(t *testing.T) {
	for _, pair := range [][2]string{
		{"- one\n  - two\n", "- one\n- two\n"},
		{"```\n  - two\n```\n", "```\n   - two\n```\n"},
	} {
		if got := contentDiff("a", pair[0], pair[1]); got == "" {
			t.Fatalf("meaningful change hidden: %q / %q", pair[0], pair[1])
		}
	}
}
