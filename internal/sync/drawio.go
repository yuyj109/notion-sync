package sync

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/yuyj109/notion-sync/internal/config"
)

var embedMarkdown = regexp.MustCompile(`(?s)<embed\b[^>]*>\s*</embed>`)
var embedSrc = regexp.MustCompile(`\bsrc\s*=\s*("[^"]*"|'[^']*')`)

// normalizeManagedEmbeds ignores URLs only for the exact block IDs we created.
// Refuse ambiguous pagination or Markdown layouts instead of masking other edits.
func normalizeManagedEmbeds(ctx context.Context, api Client, entry Entry, body string) (string, bool, error) {
	return transformManagedEmbeds(ctx, api, entry, body, nil)
}

func transformManagedEmbeds(ctx context.Context, api Client, entry Entry, body string, replacements map[string]string) (string, bool, error) {
	var children struct {
		Results []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"results"`
		HasMore bool `json:"has_more"`
	}
	if err := api.API(ctx, "GET", "v1/blocks/"+entry.PageID+"/children?page_size=100", nil, &children); err != nil {
		return "", false, fmt.Errorf("cannot inspect draw.io embed blocks: %w", err)
	}
	if children.HasMore {
		return "", false, fmt.Errorf("cannot safely match draw.io embeds on a paginated page")
	}
	var ids []string
	found := map[string]bool{}
	for _, child := range children.Results {
		id := norm(child.ID)
		if entry.managedEmbeds[id] && child.Type == "embed" {
			found[id] = true
		}
		if child.Type == "embed" {
			ids = append(ids, id)
		}
	}
	if len(found) != len(entry.managedEmbeds) {
		return "", false, fmt.Errorf("cannot safely identify all draw.io blocks on the page")
	}
	matches := embedMarkdown.FindAllStringIndex(body, -1)
	if len(matches) != len(ids) {
		return "", false, fmt.Errorf("cannot safely match draw.io embeds in page Markdown")
	}
	var result strings.Builder
	last := 0
	filled := false
	for i, match := range matches {
		result.WriteString(body[last:match[0]])
		value := body[match[0]:match[1]]
		if entry.managedEmbeds[ids[i]] {
			src := embedSrc.FindStringSubmatchIndex(value)
			if src == nil {
				return "", false, fmt.Errorf("draw.io embed has no recognizable URL")
			}
			if len(value[src[2]:src[3]]) > 2 {
				filled = true
			}
			if replacement, ok := replacements[ids[i]]; ok {
				value = replacement
			} else {
				value = value[:src[2]] + `""` + value[src[3]:]
			}
		}
		result.WriteString(value)
		last = match[1]
	}
	result.WriteString(body[last:])
	return result.String(), filled, nil
}

func drawioDiffMarkdown(ctx context.Context, api Client, entry Entry, f File, remote string) (string, error) {
	replacements := map[string]string{}
	for _, image := range f.Images {
		id := norm(entry.ImageBlocks[imageStateKey(image)])
		if entry.managedEmbeds[id] && image.SourceLine != "" {
			replacements[id] = image.SourceLine
		}
	}
	result, _, err := transformManagedEmbeds(ctx, api, entry, remote, replacements)
	return result, err
}

type drawioTextUpdate struct{ Old, New, Range string }

// drawioTextRange identifies a single region without including an embed in
// either boundary. Complete boundary lines must be unique on the whole page.
func drawioTextRange(remote, old string) (string, bool) {
	lines := strings.Split(old, "\n")
	if len(lines) < 2 {
		return "", false
	}
	findUnique := func(fromStart bool) string {
		for n := 1; n <= len(lines) && n <= 12; n++ {
			var candidate string
			if fromStart {
				candidate = strings.Join(lines[:n], "\n")
			} else {
				candidate = strings.Join(lines[len(lines)-n:], "\n")
			}
			if len(candidate) > 1000 {
				break
			}
			if strings.TrimSpace(candidate) != "" && !strings.Contains(candidate, "...") && strings.Count(remote, candidate) == 1 {
				return candidate
			}
		}
		return ""
	}
	start, end := findUnique(true), findUnique(false)
	if start == "" || end == "" || len(start)+len(end) > len(old) {
		return "", false
	}
	return start + "..." + end, true
}

// drawioMediaParts matches every local image to an existing top-level block and
// its Markdown representation. Images and embeds are protected boundaries.
func drawioMediaParts(ctx context.Context, api Client, a Action, remote string) ([]string, []string, bool, error) {
	if len(a.File.Images) == 0 || len(a.TextParts) != len(a.File.Images)+1 || len(changedUnmanagedImages(a.File.Images, a.Entry)) != 0 {
		return nil, nil, false, nil
	}
	var children struct {
		Results []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"results"`
		HasMore bool `json:"has_more"`
	}
	if err := api.API(ctx, "GET", "v1/blocks/"+a.Entry.PageID+"/children?page_size=100", nil, &children); err != nil {
		return nil, nil, false, fmt.Errorf("cannot inspect draw.io embed blocks: %w", err)
	}
	if children.HasMore {
		return nil, nil, false, nil
	}
	type mediaMatch struct {
		start, end int
		kind       string
	}
	var matches []mediaMatch
	for _, m := range embedMarkdown.FindAllStringIndex(remote, -1) {
		matches = append(matches, mediaMatch{m[0], m[1], "embed"})
	}
	for _, m := range remoteImageRE.FindAllStringIndex(remote, -1) {
		matches = append(matches, mediaMatch{m[0], m[1], "image"})
	}
	for _, m := range remoteVideoRE.FindAllStringIndex(remote, -1) {
		matches = append(matches, mediaMatch{m[0], m[1], "video"})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].start < matches[j].start })
	var blocks []struct {
		ID   string
		Type string
	}
	for _, child := range children.Results {
		if child.Type == "embed" || child.Type == "image" || child.Type == "video" {
			blocks = append(blocks, struct {
				ID   string
				Type string
			}{norm(child.ID), child.Type})
		}
	}
	if len(matches) != len(a.File.Images) || len(blocks) != len(matches) {
		return nil, nil, false, nil
	}
	parts := make([]string, 0, len(matches)+1)
	media := make([]string, 0, len(matches))
	last := 0
	for i, m := range matches {
		image := a.File.Images[i]
		id := norm(a.Entry.ImageBlocks[imageStateKey(image)])
		if m.start < last || id == "" || blocks[i].ID != id || blocks[i].Type != m.kind {
			return nil, nil, false, nil
		}
		value := remote[m.start:m.end]
		switch {
		case isDrawioSVG(image):
			if m.kind != "embed" || !a.Entry.managedEmbeds[id] {
				return nil, nil, false, nil
			}
			media = append(media, value)
		case image.ContentType == "video/mp4":
			if m.kind != "video" {
				return nil, nil, false, nil
			}
			src := videoSrcRE.FindStringSubmatch(value)
			block, err := readImageBlock(ctx, api, id)
			if len(src) < 2 || err != nil || block.Type != "video" || imageMarkdownKey(html.UnescapeString(src[1])) != imageFileKey(mediaBlockURL(block)) {
				return nil, nil, false, nil
			}
			media = append(media, "video:"+id+":"+block.LastEditedTime+":"+imageFileKey(mediaBlockURL(block)))
		default:
			if m.kind != "image" {
				return nil, nil, false, nil
			}
			match := remoteImageRE.FindStringSubmatch(value)
			u, err := url.Parse(strings.Trim(match[2], "<>"))
			if err != nil || !strings.HasPrefix(u.Fragment, "notion_record=block.") {
				return nil, nil, false, nil
			}
			fields := strings.Split(strings.TrimPrefix(u.Fragment, "notion_record=block."), ".")
			if norm(fields[0]) != id {
				return nil, nil, false, nil
			}
			media = append(media, "image:"+id)
		}
		parts = append(parts, remote[last:m.start])
		last = m.end
	}
	parts = append(parts, remote[last:])
	return parts, media, true, nil
}

// drawioTextUpdates only replaces text between identified existing media blocks.
func drawioTextUpdates(ctx context.Context, api Client, a Action, remote string) ([]drawioTextUpdate, bool, error) {
	parts, _, safe, err := drawioMediaParts(ctx, api, a, remote)
	if err != nil || !safe {
		return nil, false, err
	}
	var updates []drawioTextUpdate
	for i := range a.TextParts {
		old, next := strings.TrimSpace(parts[i]), strings.TrimSpace(a.TextParts[i])
		if old != next {
			if old == "" || strings.Contains(next, "<embed") || remoteImageRE.MatchString(next) || remoteVideoRE.MatchString(next) {
				return nil, false, nil
			}
			update := drawioTextUpdate{Old: old, New: next}
			// Large regions can contain serialized Notion tables and other blocks
			// that do not match as one update_content string. Match only their
			// unique outer boundaries instead.
			if len(old) > 2000 || strings.Contains(old, "<table") {
				var ok bool
				update.Range, ok = drawioTextRange(remote, old)
				if !ok {
					return nil, false, nil
				}
			}
			updates = append(updates, update)
		}
	}
	// The API requires every old string to match exactly once, including after
	// earlier replacements in the same request have been applied.
	current := remote
	for _, update := range updates {
		if strings.Count(current, update.Old) != 1 {
			return nil, false, nil
		}
		current = strings.Replace(current, update.Old, update.New, 1)
	}
	if len(updates) > 100 {
		return nil, false, nil
	}
	return updates, true, nil
}

// appendedDrawioText identifies a safe end-of-page append without rebuilding
// or changing the existing image and embed blocks.
func appendedDrawioText(ctx context.Context, api Client, entry Entry, f File, remote string) (string, error) {
	if len(f.Images) == 0 || !isDrawioSVG(f.Images[len(f.Images)-1]) || len(changedUnmanagedImages(f.Images, entry)) != 0 {
		return "", nil
	}
	remote, err := drawioDiffMarkdown(ctx, api, entry, f, remote)
	if err != nil {
		return "", err
	}
	imageLines := map[string]string{}
	for _, image := range f.Images {
		if !isDrawioSVG(image) && image.SourceLine != "" {
			imageLines[norm(entry.ImageBlocks[imageStateKey(image)])] = image.SourceLine
		}
	}
	remote = remoteImageRE.ReplaceAllStringFunc(remote, func(value string) string {
		parts := remoteImageRE.FindStringSubmatch(value)
		if len(parts) < 3 {
			return value
		}
		u, err := url.Parse(strings.Trim(parts[2], "<>"))
		if err != nil || !strings.HasPrefix(u.Fragment, "notion_record=block.") {
			return value
		}
		fields := strings.Split(strings.TrimPrefix(u.Fragment, "notion_record=block."), ".")
		if len(fields) > 0 {
			if replacement := imageLines[norm(fields[0])]; replacement != "" {
				return replacement
			}
		}
		return value
	})
	oldLines, localLines := diffLines(remote), diffLines(f.SyncBody)
	if len(oldLines) == 0 || len(localLines) <= len(oldLines) || oldLines[len(oldLines)-1].key != f.Images[len(f.Images)-1].SourceLine+"\n" {
		return "", nil
	}
	for i := range oldLines {
		if oldLines[i].key != localLines[i].key {
			return "", nil
		}
	}
	lines := strings.Split(f.SyncBody, "\n")
	return strings.Trim(strings.Join(lines[localLines[len(oldLines)-1].line+1:], "\n"), "\r\n"), nil
}

func isDrawioSVG(image Image) bool {
	return strings.HasSuffix(strings.ToLower(image.Name), ".drawio.svg")
}

func hasDrawioSVG(images []Image) bool {
	return slices.ContainsFunc(images, isDrawioSVG)
}

func trackDrawioEmbeds(entry *Entry, images []Image) {
	entry.managedEmbeds = make(map[string]bool)
	for _, image := range images {
		if isDrawioSVG(image) && config.ValidID(entry.ImageBlocks[imageStateKey(image)]) {
			entry.managedEmbeds[norm(entry.ImageBlocks[imageStateKey(image)])] = true
		}
	}
}

// When embed mode is disabled, a stored embed hash differs from the ordinary
// image hash even if the SVG is unchanged. Inspect any ambiguous blocks before
// planning an update, including blocks whose local image was removed.
func rejectDisabledManagedEmbeds(ctx context.Context, api Client, entry Entry, images []Image) error {
	current := map[string]string{}
	for _, image := range images {
		current[imageStateKey(image)] = image.Hash
		current[image.Path] = image.Hash // legacy metadata used paths as keys
	}
	ambiguous := false
	for key, prior := range entry.ImageHashes {
		if current[key] != prior {
			ambiguous = true
			break
		}
	}
	if !ambiguous {
		return nil
	}
	var children struct {
		Results []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"results"`
		HasMore bool `json:"has_more"`
	}
	if err := api.API(ctx, "GET", "v1/blocks/"+entry.PageID+"/children?page_size=100", nil, &children); err != nil {
		return fmt.Errorf("cannot inspect existing media blocks: %w", err)
	}
	if children.HasMore {
		return fmt.Errorf("cannot safely inspect existing media on a paginated page; enable --drawio-embed")
	}
	ids := map[string]bool{}
	for _, id := range entry.ImageBlocks {
		ids[norm(id)] = true
	}
	found := map[string]bool{}
	for _, child := range children.Results {
		id := norm(child.ID)
		if ids[id] {
			found[id] = true
		}
		if child.Type == "embed" && (len(ids) == 0 || ids[id]) {
			return fmt.Errorf("existing managed draw.io embeds require --drawio-embed; enable it to preserve them")
		}
	}
	if len(found) != len(ids) {
		return fmt.Errorf("cannot safely identify existing media blocks; inspect Notion before syncing")
	}
	return nil
}

// Identify previously managed blocks even when their local references have
// been removed, reordered, or replaced by non-draw.io images.
func identifyManagedDrawioEmbeds(ctx context.Context, api Client, entry *Entry, images []Image) error {
	entry.managedEmbeds = map[string]bool{}
	current := map[string]Image{}
	for _, image := range images {
		current[imageStateKey(image)] = image
		current[image.Path] = image // legacy metadata used paths as keys
	}
	unknown := map[string]bool{}
	for key, blockID := range entry.ImageBlocks {
		id := norm(blockID)
		image, present := current[key]
		prior := entry.ImageHashes[key]
		if present && prior == image.Hash {
			continue // unchanged ordinary image
		}
		if present && isDrawioSVG(image) && prior == hash("drawio-embed:"+image.Hash) {
			entry.managedEmbeds[id] = true
			continue
		}
		unknown[id] = true
	}
	if len(unknown) == 0 {
		return nil
	}
	var children struct {
		Results []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"results"`
		HasMore bool `json:"has_more"`
	}
	if err := api.API(ctx, "GET", "v1/blocks/"+entry.PageID+"/children?page_size=100", nil, &children); err != nil {
		return fmt.Errorf("cannot inspect draw.io blocks: %w", err)
	}
	if children.HasMore {
		return fmt.Errorf("cannot safely identify draw.io blocks on a paginated page")
	}
	for _, child := range children.Results {
		id := norm(child.ID)
		if !unknown[id] {
			continue
		}
		switch child.Type {
		case "embed":
			entry.managedEmbeds[id] = true
		case "image", "video":
		default:
			return fmt.Errorf("draw.io block %s has unexpected type %q", child.ID, child.Type)
		}
		delete(unknown, id)
	}
	if len(unknown) > 0 {
		return fmt.Errorf("cannot identify existing draw.io blocks")
	}
	return nil
}

func changedUnmanagedImages(images []Image, entry Entry) []Image {
	var changed []Image
	for _, image := range changedImages(images, entry.ImageHashes) {
		if isDrawioSVG(image) && entry.managedEmbeds[norm(entry.ImageBlocks[imageStateKey(image)])] {
			continue
		}
		changed = append(changed, image)
	}
	return changed
}

func changedManagedDrawio(images []Image, entry Entry) bool {
	for _, image := range changedImages(images, entry.ImageHashes) {
		if isDrawioSVG(image) && entry.managedEmbeds[norm(entry.ImageBlocks[imageStateKey(image)])] {
			return true
		}
	}
	return false
}

func imageHashesPreservingDrawio(images []Image, entry Entry) map[string]string {
	hashes := imageHashMap(images)
	for _, image := range images {
		key := imageStateKey(image)
		if entry.managedEmbeds[norm(entry.ImageBlocks[key])] {
			if prior, ok := entry.ImageHashes[key]; ok {
				hashes[key] = prior
			} else if prior, ok := entry.ImageHashes[image.Path]; ok {
				hashes[key] = prior
			} else {
				delete(hashes, key)
			}
		}
	}
	return hashes
}

type drawioDiagram struct {
	Name    string `xml:"name,attr,omitempty"`
	ID      string `xml:"id,attr,omitempty"`
	Content string `xml:",innerxml"`
}

func encodeDrawioXML(data string) (string, error) {
	var source struct {
		XMLName  xml.Name        `xml:"mxfile"`
		Diagrams []drawioDiagram `xml:"diagram"`
	}
	if err := xml.Unmarshal([]byte(data), &source); err != nil || len(source.Diagrams) == 0 {
		return "", fmt.Errorf("invalid embedded draw.io diagram")
	}
	var result strings.Builder
	result.WriteString("<mxfile>")
	for _, diagram := range source.Diagrams {
		inner := strings.TrimSpace(diagram.Content)
		if strings.HasPrefix(inner, "<") {
			var graph struct{ XMLName xml.Name }
			if err := xml.Unmarshal([]byte(inner), &graph); err != nil || graph.XMLName.Local != "mxGraphModel" {
				return "", fmt.Errorf("unsupported draw.io diagram content")
			}
			encoded := strings.ReplaceAll(url.QueryEscape(inner), "+", "%20")
			var compressed bytes.Buffer
			writer, err := flate.NewWriter(&compressed, flate.BestCompression)
			if err != nil {
				return "", err
			}
			if _, err := writer.Write([]byte(encoded)); err != nil {
				return "", err
			}
			if err := writer.Close(); err != nil {
				return "", err
			}
			inner = base64.StdEncoding.EncodeToString(compressed.Bytes())
		} else if _, err := base64.StdEncoding.DecodeString(inner); err != nil || inner == "" {
			return "", fmt.Errorf("unsupported draw.io compressed diagram")
		}
		result.WriteString("<diagram")
		for _, attr := range []struct{ name, value string }{{"name", diagram.Name}, {"id", diagram.ID}} {
			if attr.value != "" {
				result.WriteByte(' ')
				result.WriteString(attr.name)
				result.WriteString(`="`)
				if err := xml.EscapeText(&result, []byte(attr.value)); err != nil {
					return "", err
				}
				result.WriteByte('"')
			}
		}
		result.WriteByte('>')
		result.WriteString(inner)
		result.WriteString("</diagram>")
	}
	result.WriteString("</mxfile>")
	return result.String(), nil
}

func drawioViewerURL(data []byte, filename string) ([]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("cannot read draw.io SVG: %w", err)
		}
		root, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if root.Name.Space != "http://www.w3.org/2000/svg" || root.Name.Local != "svg" {
			return nil, fmt.Errorf("draw.io file is not an SVG")
		}
		for _, attr := range root.Attr {
			if attr.Name.Space == "" && attr.Name.Local == "content" {
				encoded, err := encodeDrawioXML(attr.Value)
				if err != nil {
					return nil, err
				}
				viewer := "https://viewer.diagrams.net/?border=0&tags=%7B%7D&lightbox=1&highlight=0000ff&edit=_blank&layers=1&nav=1&title=" + url.QueryEscape(filename) + "&dark=auto#R" + strings.ReplaceAll(url.QueryEscape(encoded), "+", "%20")
				return []byte(viewer + "\n"), nil
			}
		}
		return nil, fmt.Errorf("draw.io SVG has no embedded diagram data")
	}
}

func exportDrawioURL(c config.Config, p Plan, write bool, print func(string, ...any)) error {
	outputs := map[string][]byte{}
	for _, action := range p.Actions {
		for _, image := range action.File.Images {
			if !isDrawioSVG(image) {
				continue
			}
			if !action.ForceReplace && action.Entry.managedEmbeds[norm(action.Entry.ImageBlocks[imageStateKey(image)])] {
				continue
			}
			filename := filepath.Join(c.SourcePath, filepath.FromSlash(image.Path))
			if err := verifyImageUnchanged(filename, image.Hash); err != nil {
				return err
			}
			data, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			if hash(string(data)) != image.Hash {
				return fmt.Errorf("image changed during sync: %s", image.Path)
			}
			content, err := drawioViewerURL(data, image.Name)
			if err != nil {
				return fmt.Errorf("%s: %w", image.Path, err)
			}
			if len(content)-1 > 2000 && print != nil {
				print("warning %s: viewer URL is %d characters; Notion may reject long URLs", image.Path, len(content)-1)
			}
			name := image.Name + ".txt"
			if prior, ok := outputs[name]; ok && !bytes.Equal(prior, content) {
				return fmt.Errorf("draw.io URL output filename collision: %s", name)
			}
			outputs[name] = content
		}
	}
	if len(outputs) == 0 {
		return nil
	}
	dir := filepath.Join(c.WorkDir, ".notion-sync")
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if !write {
			return nil
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		info, err = os.Lstat(dir)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("draw.io output directory must be a real directory: %s", dir)
	}
	// Check all destinations before writing any, including during dry-run.
	for name, content := range outputs {
		dest := filepath.Join(dir, name)
		info, err := os.Lstat(dest)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("draw.io output exists and is not a regular file: %s", dest)
		}
		old, err := os.ReadFile(dest)
		if err != nil || !bytes.Equal(old, content) {
			return fmt.Errorf("draw.io output already exists with different content; move it before syncing: %s", dest)
		}
	}
	if !write {
		return nil
	}
	for name, content := range outputs {
		dest := filepath.Join(dir, name)
		file, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			info, statErr := os.Lstat(dest)
			if statErr != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("draw.io output exists and is not a regular file: %s", dest)
			}
			old, readErr := os.ReadFile(dest)
			if readErr != nil || !bytes.Equal(old, content) {
				return fmt.Errorf("draw.io output already exists with different content; move it before syncing: %s", dest)
			}
			continue
		}
		if err != nil {
			return err
		}
		_, writeErr := file.Write(content)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
