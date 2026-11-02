package sync

import (
	"net/url"
	"strings"
	"testing"
)

func TestMatchImageMarkdownUsesBlockIDsBeforeURLs(t *testing.T) {
	first := "11111111-1111-1111-1111-111111111111"
	second := "22222222-2222-2222-2222-222222222222"
	shared := "https://files.example/shared.png"
	blocks := map[string]imageBlock{norm(first): testImageBlock(first, shared, "old"), norm(second): testImageBlock(second, shared, "old")}
	ref := func(id string) string {
		return "![image](https://www.notion.so/image/" + url.PathEscape(shared) + "#notion_record=block." + id + ".space)"
	}
	comparable, refs, err := matchImageMarkdown(ref(first)+"\n"+ref(second), blocks)
	if err != nil || len(refs) != 2 || !strings.Contains(comparable, norm(first)) || !strings.Contains(comparable, norm(second)) {
		t.Fatalf("block IDs not used: %q %+v %v", comparable, refs, err)
	}
	sharedRefs := "![one](" + shared + "#notion_record=block." + first + ")\n![two](" + shared + "#notion_record=block." + second + ")"
	if _, refs, err := matchImageMarkdown(sharedRefs, blocks); err != nil || len(refs) != 2 {
		t.Fatalf("shared file with block IDs not matched: %+v %v", refs, err)
	}
	if _, _, err := matchImageMarkdown("![image]("+shared+")", blocks); err == nil {
		t.Fatal("shared URL without block IDs must be ambiguous")
	}
	// An unrelated block can use the same file without being selected for update.
	if _, refs, err := matchImageMarkdown(sharedRefs, map[string]imageBlock{norm(first): blocks[norm(first)]}); err != nil || len(refs) != 1 {
		t.Fatalf("unrelated block not ignored: %+v %v", refs, err)
	}
}

func TestMatchImageMarkdownRejectsUnverifiableBlockID(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	blocks := map[string]imageBlock{norm(id): testImageBlock(id, "https://files.example/expected.png", "old")}
	for _, raw := range []string{
		"https://cdn.example/wrong.png",
		"https://cdn.example/by-block/" + id,
		"https://files.example/expected.png?versionId=other",
		"https://evil.example/image/" + url.PathEscape("https://files.example/expected.png"),
		"https://www.notion.so/image/" + url.PathEscape("https://files.example/wrong.png"),
	} {
		body := "![photo](" + raw + "#notion_record=block." + id + ")"
		if _, _, err := matchImageMarkdown(body, blocks); err == nil {
			t.Fatalf("accepted unrelated URL %s", raw)
		}
	}
}

func TestImageFileKeyKeepsResourceSelectors(t *testing.T) {
	a := imageFileKey("https://files.example/file.png?versionId=one&X-Amz-Signature=old")
	b := imageFileKey("https://files.example/file.png?X-Amz-Signature=new&versionId=one")
	c := imageFileKey("https://files.example/file.png?versionId=two&X-Amz-Signature=new")
	if a == "" || a != b || a == c {
		t.Fatalf("incorrect file identity: %q %q %q", a, b, c)
	}
}

func TestImageFileKeyMatchesNotionS3AddressStyles(t *testing.T) {
	path := "https://s3.us-west-2.amazonaws.com/secure.notion-static.com/folder/file.png?versionId=one&X-Amz-Signature=old"
	host := "https://secure.notion-static.com.s3.us-west-2.amazonaws.com/folder/file.png?versionId=one&X-Amz-Signature=new"
	if imageFileKey(path) == "" || imageFileKey(path) != imageFileKey(host) {
		t.Fatal("equivalent Notion S3 addresses did not match")
	}
	for _, other := range []string{
		"https://secure.notion-static.com.s3.us-east-1.amazonaws.com/folder/file.png?versionId=one",
		"https://secure.notion-static.com.s3.us-west-2.amazonaws.com/folder/file.png?versionId=two",
		"https://s3.us-west-2.amazonaws.com/other-bucket/folder/file.png?versionId=one",
	} {
		if imageFileKey(path) == imageFileKey(other) {
			t.Fatalf("different resource matched: %s", other)
		}
	}
	encoded := "https://s3.us-west-2.amazonaws.com/secure.notion-static.com/folder%2Ffile.png"
	plain := "https://secure.notion-static.com.s3.us-west-2.amazonaws.com/folder/file.png"
	if imageFileKey(encoded) == imageFileKey(plain) {
		t.Fatal("different S3 object paths were merged")
	}
	id := "11111111-1111-1111-1111-111111111111"
	block := map[string]imageBlock{norm(id): testImageBlock(id, path, "old")}
	markdown := "![x](https://www.notion.so/image/" + url.PathEscape(host) + "#notion_record=block." + id + ")"
	if _, _, err := matchImageMarkdown(markdown, block); err != nil {
		t.Fatalf("S3 aliases did not match through proxy: %v", err)
	}
}

func TestMatchImageMarkdownIgnoresUnchangedNotionS3Signature(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	oldURL, newURL := "https://files.example/old.png", "https://files.example/new.png"
	other := "https://s3.us-west-2.amazonaws.com/secure.notion-static.com/other.png"
	before := "![selected](" + oldURL + ")\n![other](" + other + "?X-Amz-Signature=old)"
	after := "![selected](" + newURL + ")\n![other](" + other + "?X-Amz-Signature=new)"
	oldText, _, err := matchImageMarkdown(before, map[string]imageBlock{norm(id): testImageBlock(id, oldURL, "old")})
	if err != nil {
		t.Fatal(err)
	}
	newText, _, err := matchImageMarkdown(after, map[string]imageBlock{norm(id): testImageBlock(id, newURL, "new")})
	if err != nil || oldText != newText {
		t.Fatalf("unchanged image caused a conflict: %q != %q: %v", oldText, newText, err)
	}
}
