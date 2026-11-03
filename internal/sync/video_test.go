package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type videoTestClient struct {
	*fakeAPI
	block     imageBlock
	uploads   int
	child     map[string]any
	videoMIME string
}

func (c *videoTestClient) Upload(_ context.Context, _, contentType, _ string) (string, error) {
	c.uploads++
	c.videoMIME = contentType
	return "22222222-2222-2222-2222-222222222222", nil
}

func (c *videoTestClient) API(ctx context.Context, method, endpoint string, body any, out any) error {
	copyResult := func(value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	if method == "PATCH" && strings.HasSuffix(endpoint, "/children") {
		c.child = body.(map[string]any)["children"].([]any)[0].(map[string]any)
		return copyResult(map[string]any{"results": []any{map[string]any{"id": c.block.ID, "type": "video"}}})
	}
	if strings.HasPrefix(endpoint, "v1/blocks/") && !strings.Contains(endpoint, "/children") {
		if method == "PATCH" {
			request := body.(map[string]any)
			if request["type"] != "video" || request["video"].(map[string]any)["file_upload"] == nil {
				return fmt.Errorf("unexpected video update")
			}
			old := c.block.Video.File.URL
			c.block.Video.File.URL = "https://files.example/new.mp4"
			c.block.LastEditedTime = "new"
			for _, page := range c.pages {
				page.content = strings.ReplaceAll(page.content, old, c.block.Video.File.URL)
			}
		}
		return copyResult(c.block)
	}
	return c.fakeAPI.API(ctx, method, endpoint, body, out)
}

func testVideoBlock(id, fileURL, edited string) imageBlock {
	b := imageBlock{Object: "block", ID: id, Type: "video", LastEditedTime: edited}
	b.Video.Type = "file"
	b.Video.File.URL = fileURL
	return b
}

func TestLocalMP4References(t *testing.T) {
	dir := t.TempDir()
	var err error
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("video data"), 0600); err != nil {
		t.Fatal(err)
	}
	body := "Before\n\n![Video](clip.mp4)\n\n[Demo](clip.mp4)\n\nAfter\n"
	files, err := localImages(body, "page.md", dir)
	if err != nil || len(files) != 2 || files[0].ContentType != "video/mp4" || files[1].Alt != "Demo" {
		t.Fatalf("unexpected video references: %+v, %v", files, err)
	}
	parts, err := imageTextParts(body)
	if err != nil || len(parts) != 3 || parts[0] != "Before" || parts[1] != "" || parts[2] != "After" {
		t.Fatalf("incorrect video placement: %q, %v", parts, err)
	}
	if _, err := localImages("Before [Demo](clip.mp4) after", "page.md", dir); err == nil {
		t.Fatal("inline video must be rejected")
	}
	if _, err := localImages("[Demo][video]\n\n[video]: clip.mp4\n", "page.md", dir); err == nil {
		t.Fatal("reference-style local video must be rejected")
	}
	if got, err := localImages("```\n[Demo](clip.mp4)\n```\n", "page.md", dir); err != nil || len(got) != 0 {
		t.Fatalf("code examples must not upload: %+v, %v", got, err)
	}
}

func TestAppendMP4AsVideoBlock(t *testing.T) {
	cfg, base, dir := setup(t)
	var err error
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SourcePath = dir
	filename := filepath.Join(dir, "clip.mp4")
	write(t, filename, "video data")
	body := "# Guide\n\n[Demo](clip.mp4)\n"
	media, err := localImages(body, "page.md", dir)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := imageTextParts(body)
	if err != nil {
		t.Fatal(err)
	}
	client := &videoTestClient{fakeAPI: base, block: testVideoBlock("11111111-1111-1111-1111-111111111111", "https://files.example/old.mp4", "old")}
	blocks, err := appendImages(context.Background(), client, rootID, Action{Path: "page.md", File: File{SyncBody: body, Images: media}, TextParts: parts}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if client.uploads != 1 || client.videoMIME != "video/mp4" || client.child["type"] != "video" || blocks["image-1"] != client.block.ID {
		t.Fatalf("unexpected video upload: %+v %v", client.child, blocks)
	}
}

func TestUpdateMP4VerifiesVideoBlock(t *testing.T) {
	cfg, base, dir := setup(t)
	write(t, filepath.Join(dir, "clip.mp4"), "new video data")
	write(t, filepath.Join(dir, "page.md"), "# Guide\n\n[Demo](clip.mp4)\n")
	state, err := loadState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	file := *state.Documents["page.md"]
	blockID := "11111111-1111-1111-1111-111111111111"
	oldURL := "https://files.example/old.mp4"
	remote := "# Guide\n<video src=\"" + oldURL + "\">Demo</video>"
	base.pages[norm(rootID)].content = remote
	client := &videoTestClient{fakeAPI: base, block: testVideoBlock(blockID, oldURL, "old")}
	entry := Entry{PageID: rootID, ParentID: rootID, ParentType: "page_id", LocalHash: file.Hash, RemoteHash: remoteMarkdownHash(remote), ImageBlocks: map[string]string{"image-1": blockID}, ImageHashes: map[string]string{"image-1": hash("old video data")}}
	state.Active["page.md"] = true
	state.Entries["file:page.md"] = entry
	action := Action{Kind: "images-update", Key: "file:page.md", Path: "page.md", Entry: entry, File: file}
	if err := Apply(context.Background(), cfg, client, &state, Plan{Actions: []Action{action}}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if client.uploads != 1 || client.block.Video.File.URL == oldURL || state.Entries[action.Key].ImageHashes["image-1"] != file.Images[0].Hash {
		t.Fatalf("video update was not recorded: %+v", state.Entries[action.Key])
	}
}

func TestRemoteVideoHashIgnoresNotionSignedURLExpiry(t *testing.T) {
	base := "https://s3.us-west-2.amazonaws.com/secure.notion-static.com/video.mp4?X-Amz-Signature="
	a := `<video src="` + base + `old">Demo</video>`
	b := `<video src="` + base + `new">Demo</video>`
	if remoteMarkdownHash(a) != remoteMarkdownHash(b) {
		t.Fatal("signed video URL expiration caused a conflict")
	}
	if remoteMarkdownHash(a) == remoteMarkdownHash(strings.Replace(a, "video.mp4", "other.mp4", 1)) {
		t.Fatal("different videos must not be treated as the same")
	}
	external := `<video src="https://example.com/video.mp4?X-Amz-Signature=old">Demo</video>`
	if remoteMarkdownHash(external) == remoteMarkdownHash(strings.Replace(external, "old", "new", 1)) {
		t.Fatal("untrusted video URLs must not have query changes ignored")
	}
	diff := makeContentDiff("page.md", a, "# Local\n", 0, 0).Plain
	if strings.Contains(diff, "X-Amz-Signature=old") {
		t.Fatal("video download signature was exposed in a diff")
	}
}

func TestVideoReferenceMatchesBlockAndRejectsAmbiguity(t *testing.T) {
	first := "11111111-1111-1111-1111-111111111111"
	second := "22222222-2222-2222-2222-222222222222"
	fileURL := "https://files.example/shared.mp4"
	blocks := map[string]imageBlock{norm(first): testVideoBlock(first, fileURL, "old"), norm(second): testVideoBlock(second, fileURL, "old")}
	ref := func(id string) string {
		return `<video src="` + fileURL + `#notion_record=block.` + id + `">Demo</video>`
	}
	if _, refs, err := matchImageMarkdown(ref(first)+"\n"+ref(second), blocks); err != nil || len(refs) != 2 {
		t.Fatalf("video block IDs not matched: %+v, %v", refs, err)
	}
	if _, _, err := matchImageMarkdown(`<video src="`+fileURL+`">Demo</video>`, blocks); err == nil {
		t.Fatal("shared video URL without block IDs must be rejected")
	}
}

func TestDrawioModeTreatsChangedVideoAsOrdinaryMedia(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	client := &managedEmbedClient{children: []any{map[string]any{"id": id, "type": "video"}}}
	entry := Entry{PageID: rootID, ImageBlocks: map[string]string{"image-1": id}, ImageHashes: map[string]string{"image-1": "old-hash"}}
	video := Image{Key: "image-1", Name: "clip.mp4", ContentType: "video/mp4", Hash: "new-hash"}
	if err := identifyManagedDrawioEmbeds(context.Background(), client, &entry, []Image{video}); err != nil {
		t.Fatal(err)
	}
	if len(entry.managedEmbeds) != 0 || len(changedUnmanagedImages([]Image{video}, entry)) != 1 {
		t.Fatal("video must remain eligible for update")
	}
}
