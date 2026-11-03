package sync

import (
	"context"
	"fmt"
	"html"
	"maps"
	"net/url"
	"strings"

	"github.com/yuyj109/notion-sync/internal/config"
)

// imageBlock is the API's block representation, not a Markdown URL.
type imageBlock struct {
	Object, ID, Type string
	LastEditedTime   string `json:"last_edited_time"`
	Image            struct {
		Type string `json:"type"`
		File struct {
			URL string `json:"url"`
		} `json:"file"`
	} `json:"image"`
	Video struct {
		Type string `json:"type"`
		File struct {
			URL string `json:"url"`
		} `json:"file"`
	} `json:"video"`
}

func mediaBlockURL(b imageBlock) string {
	if b.Type == "video" && b.Video.Type == "file" {
		return b.Video.File.URL
	}
	if b.Type == "image" && b.Image.Type == "file" {
		return b.Image.File.URL
	}
	return ""
}

// Ignore authentication parameters while retaining resource selectors such as
// S3 versionId, which may identify different bytes at the same path.
func imageFileKey(raw string) string {
	u, err := url.Parse(strings.Trim(raw, "<>"))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ""
	}
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-amz-") || strings.HasPrefix(lower, "x-goog-") || lower == "signature" || lower == "sig" || lower == "expires" || lower == "awsaccesskeyid" || lower == "googleaccessid" || lower == "policy" || lower == "key-pair-id" || lower == "token" {
			query.Del(key)
		}
	}
	u.RawQuery, u.Fragment, u.RawFragment = query.Encode(), "", ""
	u.ForceQuery = false
	canonicalNotionS3URL(u)
	return u.String()
}

// Notion's image proxy embeds the source URL in the path. Do not infer an
// origin for other proxies, including stable URLs that identify only a block.
func imageMarkdownKey(raw string) string {
	u, err := url.Parse(strings.Trim(raw, "<>"))
	if err != nil || u.Scheme != "https" || u.User != nil {
		return ""
	}
	if source, ok, err := notionImageProxySource(u); ok {
		if err != nil {
			return ""
		}
		return imageFileKey(source)
	}
	return imageFileKey(raw)
}

func checkedImageBlock(b imageBlock, id string) error {
	if b.Object != "block" || (b.Type != "image" && b.Type != "video") || !config.ValidID(b.ID) || norm(b.ID) != norm(id) || imageFileKey(mediaBlockURL(b)) == "" || b.LastEditedTime == "" {
		return fmt.Errorf("unexpected image or video block response")
	}
	return nil
}

func readImageBlock(ctx context.Context, api Client, id string) (imageBlock, error) {
	var b imageBlock
	if err := api.API(ctx, "GET", "v1/blocks/"+id, nil, &b); err != nil {
		return b, err
	}
	return b, checkedImageBlock(b, id)
}

func sameImageBlock(a, b imageBlock) bool {
	return norm(a.ID) == norm(b.ID) && a.Type == b.Type && a.LastEditedTime == b.LastEditedTime && imageFileKey(mediaBlockURL(a)) == imageFileKey(mediaBlockURL(b))
}

// Match each expected image to exactly one Markdown reference. A block ID
// fragment is optional when the URL identifies the same Notion-hosted file.
func matchImageMarkdown(body string, blocks map[string]imageBlock) (string, map[string]string, error) {
	protected := protectedMarkdown(body)
	seen := map[string]string{}
	var out strings.Builder
	last := 0
	for _, loc := range remoteImageRE.FindAllStringIndex(body, -1) {
		if protected[loc[0]] {
			continue
		}
		value := body[loc[0]:loc[1]]
		parts := remoteImageRE.FindStringSubmatch(value)
		u, err := url.Parse(strings.Trim(parts[2], "<>"))
		if err != nil {
			continue
		}
		key := imageMarkdownKey(parts[2])
		var matched string
		if fragment, ok := strings.CutPrefix(u.Fragment, "notion_record=block."); ok {
			id := norm(strings.Split(fragment, ".")[0])
			if block, exists := blocks[id]; exists {
				if block.Type != "image" || key == "" || key != imageFileKey(mediaBlockURL(block)) {
					return "", nil, fmt.Errorf("image block ID does not match a verifiable file URL")
				}
				matched = id
			} else {
				// A different block can legitimately share the same file URL.
				continue
			}
		} else {
			for id, block := range blocks {
				if block.Type == "image" && key != "" && key == imageFileKey(mediaBlockURL(block)) {
					if matched != "" {
						return "", nil, fmt.Errorf("image URLs are ambiguous")
					}
					matched = id
				}
			}
		}
		if matched == "" {
			continue
		}
		if _, exists := seen[matched]; exists {
			return "", nil, fmt.Errorf("image reference is ambiguous")
		}
		seen[matched] = key
		out.WriteString(body[last:loc[0]])
		out.WriteString("![")
		out.WriteString(parts[1])
		out.WriteString("](<notion-sync-image:")
		out.WriteString(matched)
		out.WriteString(">)")
		out.WriteString(parts[3])
		last = loc[1]
	}
	out.WriteString(body[last:])
	body = out.String()
	protected = protectedMarkdown(body)
	out.Reset()
	last = 0
	for _, loc := range remoteVideoRE.FindAllStringIndex(body, -1) {
		if protected[loc[0]] {
			continue
		}
		value := body[loc[0]:loc[1]]
		src := videoSrcRE.FindStringSubmatchIndex(value)
		if src == nil {
			continue
		}
		srcURL := html.UnescapeString(value[src[2]:src[3]])
		key := imageMarkdownKey(srcURL)
		var matched string
		u, err := url.Parse(srcURL)
		if err != nil {
			continue
		}
		if fragment, ok := strings.CutPrefix(u.Fragment, "notion_record=block."); ok {
			id := norm(strings.Split(fragment, ".")[0])
			if block, exists := blocks[id]; exists {
				if block.Type != "video" || key == "" || key != imageFileKey(mediaBlockURL(block)) {
					return "", nil, fmt.Errorf("video block ID does not match a verifiable file URL")
				}
				matched = id
			}
		} else {
			for id, block := range blocks {
				if block.Type == "video" && key != "" && key == imageFileKey(mediaBlockURL(block)) {
					if matched != "" {
						return "", nil, fmt.Errorf("video URLs are ambiguous")
					}
					matched = id
				}
			}
		}
		if matched == "" {
			continue
		}
		if _, exists := seen[matched]; exists {
			return "", nil, fmt.Errorf("video reference is ambiguous")
		}
		seen[matched] = key
		out.WriteString(body[last:loc[0]])
		out.WriteString(value[:src[2]])
		out.WriteString("notion-sync-video:")
		out.WriteString(matched)
		out.WriteString(value[src[3]:])
		last = loc[1]
	}
	out.WriteString(body[last:])
	if len(seen) != len(blocks) {
		return "", nil, fmt.Errorf("cannot safely match image blocks to Markdown")
	}
	return canonicalRemoteMarkdown(out.String()), seen, nil
}

func prepareImageUpdate(ctx context.Context, api Client, a Action, body string) (Entry, map[string]imageBlock, string, map[string]string, error) {
	e := a.Entry
	// Do not mutate a state's maps until the entire operation succeeds.
	e.ImageBlocks = maps.Clone(e.ImageBlocks)
	e.ImageHashes = maps.Clone(e.ImageHashes)
	if err := ensureImageBlockIDs(ctx, api, e.PageID, a.Path, a.File.Images, &e); err != nil {
		return e, nil, "", nil, err
	}
	blocks := map[string]imageBlock{}
	for _, image := range changedUnmanagedImages(a.File.Images, e) {
		id := e.ImageBlocks[imageStateKey(image)]
		if !config.ValidID(id) {
			return e, nil, "", nil, fmt.Errorf("%s: missing image block ID", a.Path)
		}
		if _, ok := blocks[norm(id)]; ok {
			return e, nil, "", nil, fmt.Errorf("%s: duplicate image block ID", a.Path)
		}
		b, err := readImageBlock(ctx, api, id)
		if err != nil {
			return e, nil, "", nil, fmt.Errorf("%s: cannot inspect image block before updating: %w", a.Path, err)
		}
		if (image.ContentType == "video/mp4") != (b.Type == "video") {
			return e, nil, "", nil, fmt.Errorf("%s: media block type does not match the local file", a.Path)
		}
		blocks[norm(id)] = b
	}
	comparable, refs, err := matchImageMarkdown(body, blocks)
	if err != nil {
		return e, nil, "", nil, fmt.Errorf("%s: %w; no images were updated", a.Path, err)
	}
	return e, blocks, comparable, refs, nil
}
