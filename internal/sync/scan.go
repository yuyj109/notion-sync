package sync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuyj109/notion-sync/internal/config"
	"gopkg.in/yaml.v3"
)

type File struct {
	Path, Body, Hash, PageID, ParentID, DatabaseID string
	SyncBody                                       string
	Raw                                            []byte
	Images                                         []Image
	Metadata                                       *Metadata
	MetadataFormat                                 string
	RootNeedsFormatting                            bool
}

// Image is a local Markdown image reference that can be uploaded to Notion.
type Image struct {
	Alt, Path, Name, ContentType, Hash, Key string
	SourceLine                              string
	StateHash                               string
	Caption                                 []any
}

var localImageRE = regexp.MustCompile(`^!\[([^\]]*)\]\((<[^>]+>|[^\s)]+)(?:\s+"([^"]*)")?\)$`)
var remoteImageRE = regexp.MustCompile(`!\[([^\]]*)\]\((<[^>]+>|[^\s)]+)(\s+"[^"]*")?\)`)

// withoutHTMLComments removes HTML comments while preserving Markdown code examples.
func withoutHTMLComments(body string) string {
	source := []byte(body)
	protected := make([]bool, len(source))
	protect := func(start, stop int) {
		for i := start; i < stop && i < len(protected); i++ {
			protected[i] = true
		}
	}
	document := goldmark.DefaultParser().Parse(text.NewReader(source))
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.Kind() {
		case ast.KindCodeBlock, ast.KindFencedCodeBlock:
			lines := node.Lines()
			for i := 0; i < lines.Len(); i++ {
				segment := lines.At(i)
				protect(segment.Start, segment.Stop)
			}
		case ast.KindCodeSpan:
			for child := node.FirstChild(); child != nil; child = child.NextSibling() {
				if part, ok := child.(*ast.Text); ok {
					protect(part.Segment.Start, part.Segment.Stop)
				}
			}
		}
		return ast.WalkContinue, nil
	})
	var out strings.Builder
	for i := 0; i < len(body); {
		if !protected[i] && strings.HasPrefix(body[i:], "<!--") && !escapedAt(body, i) {
			end := strings.Index(body[i+4:], "-->")
			if end < 0 {
				end = len(body)
			} else {
				end += i + 7
			}
			for j := i; j < end; j++ {
				if body[j] == '\n' || body[j] == '\r' && j+1 < end && body[j+1] == '\n' {
					out.WriteByte(body[j])
				}
			}
			i = end
			continue
		}
		out.WriteByte(body[i])
		i++
	}
	return out.String()
}

func escapedAt(body string, i int) bool {
	n := 0
	for i > 0 && body[i-1] == '\\' {
		n++
		i--
	}
	return n%2 != 0
}

// remoteMarkdownHash ignores expiring query signatures in Notion-hosted image URLs.
func remoteMarkdownHash(body string) string {
	return hash(canonicalRemoteMarkdown(body))
}

func canonicalRemoteMarkdown(body string) string {
	return normalizeRemoteImages(body, false)
}

// Restrict S3 signature normalization to Notion's own buckets and paths.
func isNotionSignedImageURL(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	canonical := *u
	notionHost := u.Scheme == "https" && u.User == nil && u.Port() == "" && host == "files.notion.so" || canonicalNotionS3URL(&canonical)
	if !notionHost {
		return false
	}
	for key := range u.Query() {
		if strings.EqualFold(key, "signature") || strings.EqualFold(key, "x-amz-signature") {
			return true
		}
	}
	return false
}

// Normalize credentials inside both a Notion proxy path and the outer URL.
// On display, malformed proxy sources are hidden instead of risking a leak.
func normalizeImageURL(u *url.URL, display bool, depth int) bool {
	changed := false
	proxyURL := *u
	if display {
		proxyURL.User = nil
		if proxyURL.Scheme == "" && proxyURL.Host != "" {
			proxyURL.Scheme = "https"
		}
	}
	if source, ok, err := notionImageProxySource(&proxyURL); ok {
		if err != nil || depth >= 3 {
			if display {
				u.Path, u.RawPath = "/image/[redacted]", ""
				changed = true
			}
		} else if nested, parseErr := url.Parse(source); parseErr != nil || nested.Scheme == "" {
			if display {
				u.Path, u.RawPath = "/image/[redacted]", ""
				changed = true
			}
		} else if normalizeImageURL(nested, display, depth+1) {
			u.Path = "/image/" + nested.String()
			u.RawPath = "/image/" + url.PathEscape(nested.String())
			changed = true
		}
	}
	if !display && !isNotionSignedImageURL(u) {
		return changed
	}
	if display && u.User != nil {
		u.User = nil
		changed = true
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		if display {
			u.RawQuery, u.ForceQuery = "", false
			return true
		}
		return changed
	}
	for key := range query {
		lower := strings.ToLower(key)
		if display && (lower == "sig" || strings.Contains(lower, "signature") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.HasPrefix(lower, "x-amz-") || strings.HasPrefix(lower, "x-goog-") || lower == "expires" || lower == "policy" || lower == "key-pair-id" || lower == "awsaccesskeyid" || lower == "googleaccessid") || !display && (lower == "signature" || strings.HasPrefix(lower, "x-amz-") || lower == "expires" || lower == "awsaccesskeyid") {
			query.Del(key)
		}
	}
	u.RawQuery, u.ForceQuery = query.Encode(), false
	if !display {
		canonicalNotionS3URL(u)
	}
	return true
}

func normalizeRemoteImages(body string, display bool) string {
	protected := make([]bool, len(body))
	if !display {
		doc := goldmark.DefaultParser().Parse(text.NewReader([]byte(body)))
		_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			mark := func(start, end int) {
				for i := start; i < end && i < len(protected); i++ {
					protected[i] = true
				}
			}
			switch node.Kind() {
			case ast.KindCodeBlock, ast.KindFencedCodeBlock:
				for i := 0; i < node.Lines().Len(); i++ {
					segment := node.Lines().At(i)
					mark(segment.Start, segment.Stop)
				}
			case ast.KindCodeSpan:
				for child := node.FirstChild(); child != nil; child = child.NextSibling() {
					if part, ok := child.(*ast.Text); ok {
						mark(part.Segment.Start, part.Segment.Stop)
					}
				}
			}
			return ast.WalkContinue, nil
		})
	}
	var out strings.Builder
	last := 0
	for _, loc := range remoteImageRE.FindAllStringIndex(body, -1) {
		out.WriteString(body[last:loc[0]])
		value := body[loc[0]:loc[1]]
		if !display && protected[loc[0]] {
			out.WriteString(value)
			last = loc[1]
			continue
		}
		parts := remoteImageRE.FindStringSubmatch(value)
		urlText := strings.Trim(parts[2], "<>")
		parsed, err := url.Parse(urlText)
		switch {
		case err != nil:
			if display {
				out.WriteString("![" + parts[1] + "](<redacted-invalid-url>)" + parts[3])
			} else {
				out.WriteString(value)
			}
		case parsed.Scheme == "" && (!display || parsed.Host == "" && parsed.RawQuery == ""):
			out.WriteString(value)
		case normalizeImageURL(parsed, display, 0):
			out.WriteString("![" + parts[1] + "](<" + parsed.String() + ">)" + parts[3])
		default:
			out.WriteString(value)
		}
		last = loc[1]
	}
	out.WriteString(body[last:])
	return out.String()
}

// localImages resolves standalone Markdown image lines beneath the source root.
// The image URLs are not rewritten here, so hashes continue to represent source Markdown.
func localImages(body, markdownPath, sourceRoot string) ([]Image, error) {
	// Inline local images cannot be uploaded in their original position. Reject
	// them before sending Markdown that Notion cannot resolve as a local path.
	protected := protectedMarkdown(body)
	for _, loc := range remoteImageRE.FindAllStringIndex(body, -1) {
		start, end := loc[0], loc[1]
		if protected[start] || escapedAt(body, start) || !isLocalImage(body[start:end]) {
			continue
		}
		lineStart := strings.LastIndex(body[:start], "\n") + 1
		lineEnd := len(body)
		if next := strings.IndexByte(body[end:], '\n'); next >= 0 {
			lineEnd = end + next
		}
		if strings.TrimSpace(body[lineStart:lineEnd]) != body[start:end] {
			return nil, fmt.Errorf("%s: local images must be standalone Markdown lines with a simple relative path", markdownPath)
		}
	}
	lines := strings.Split(body, "\n")
	codeLines := protectedImageLines(body)
	var images []Image
	var fence byte
	var fenceLen int
	for lineNo, line := range lines {
		if codeLines[lineNo] {
			continue
		}
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			n := 0
			for n < len(trimmed) && trimmed[n] == trimmed[0] {
				n++
			}
			if n >= 3 {
				if fence == 0 {
					fence, fenceLen = trimmed[0], n
				} else if trimmed[0] == fence && n >= fenceLen {
					fence = 0
				}
				continue
			}
		}
		if fence != 0 || trimmed == "" {
			continue
		}
		match := localImageRE.FindStringSubmatch(trimmed)
		if match == nil {
			if strings.HasPrefix(trimmed, "![") && strings.Contains(trimmed, "](") {
				candidate := trimmed[strings.Index(trimmed, "](")+2:]
				candidate = strings.TrimSuffix(candidate, ")")
				if !strings.Contains(candidate, "://") && !strings.HasPrefix(candidate, "data:") && !strings.HasPrefix(candidate, "#") {
					return nil, fmt.Errorf("%s: local images must be standalone Markdown lines with a simple relative path", markdownPath)
				}
			}
			continue
		}
		ref := strings.Trim(match[2], "<>")
		if strings.Contains(ref, "://") || strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "//") {
			continue
		}
		parsed, err := url.Parse(ref)
		if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" {
			return nil, fmt.Errorf("%s: image path %q must be a plain relative path", markdownPath, ref)
		}
		if filepath.IsAbs(parsed.Path) || filepath.VolumeName(parsed.Path) != "" {
			return nil, fmt.Errorf("%s: image path %q must stay inside the source directory", markdownPath, ref)
		}
		base := filepath.Dir(filepath.Join(sourceRoot, filepath.FromSlash(markdownPath)))
		resolved := filepath.Clean(filepath.Join(base, filepath.FromSlash(parsed.Path)))
		rel, err := filepath.Rel(sourceRoot, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s: image path %q is outside the source directory", markdownPath, ref)
		}
		canonical, err := filepath.EvalSymlinks(resolved)
		if err != nil {
			return nil, fmt.Errorf("%s: image %q: %w", markdownPath, ref, err)
		}
		if canonical != resolved {
			return nil, fmt.Errorf("%s: image %q uses a symlink path", markdownPath, ref)
		}
		info, err := os.Lstat(resolved)
		if err != nil {
			return nil, fmt.Errorf("%s: image %q: %w", markdownPath, ref, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s: image %q must be a regular file (symlinks are not followed)", markdownPath, ref)
		}
		contentType := imageContentType(filepath.Ext(resolved))
		if !strings.HasPrefix(contentType, "image/") {
			return nil, fmt.Errorf("%s: %q is not a supported image file", markdownPath, ref)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, err
		}
		if !utf8.ValidString(match[1]) {
			return nil, fmt.Errorf("%s: image alt text must be UTF-8", markdownPath)
		}
		caption := []any{}
		if match[1] != "" {
			caption = []any{map[string]any{"type": "text", "text": map[string]any{"content": match[1]}}}
		}
		key := fmt.Sprintf("image-%d", len(images)+1)
		images = append(images, Image{Alt: match[1], Path: filepath.ToSlash(rel), Name: filepath.Base(resolved), ContentType: contentType, Hash: hash(string(data)), Caption: caption, Key: key, SourceLine: trimmed})
	}
	// Goldmark resolves full, collapsed, and shortcut references. Compare its
	// local image count with the standalone forms we can actually upload so a
	// reference-style image is never silently sent with a local destination.
	localCount := 0
	doc := goldmark.DefaultParser().Parse(text.NewReader([]byte(body)))
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if img, ok := node.(*ast.Image); ok {
				ref := string(img.Destination)
				u, err := url.Parse(ref)
				if err == nil && !u.IsAbs() && u.Host == "" && u.Path != "" {
					localCount++
				}
			}
		}
		return ast.WalkContinue, nil
	})
	if localCount != len(images) {
		return nil, fmt.Errorf("%s: local images must be standalone Markdown lines with a simple relative path", markdownPath)
	}
	return images, nil
}

func imageContentType(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	default:
		return ""
	}
}

func bodyWithoutImages(body string) (string, []int, error) {
	lines := strings.Split(body, "\n")
	codeLines := protectedImageLines(body)
	var indices []int
	var textLines []string
	var fence byte
	var fenceLen int
	for i, line := range lines {
		if codeLines[i] {
			textLines = append(textLines, line)
			continue
		}
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			n := 0
			for n < len(trimmed) && trimmed[n] == trimmed[0] {
				n++
			}
			if n >= 3 {
				if fence == 0 {
					fence, fenceLen = trimmed[0], n
				} else if trimmed[0] == fence && n >= fenceLen {
					fence = 0
				}
				textLines = append(textLines, line)
				continue
			}
		}
		if fence == 0 && localImageRE.MatchString(trimmed) && isLocalImage(trimmed) {
			indices = append(indices, i)
			continue
		}
		textLines = append(textLines, line)
	}
	if len(indices) == 0 {
		return body, nil, nil
	}
	return strings.Join(textLines, "\n"), indices, nil
}

func isLocalImage(line string) bool {
	match := localImageRE.FindStringSubmatch(strings.TrimSpace(line))
	if match == nil {
		return false
	}
	ref := strings.Trim(match[2], "<>")
	return !strings.Contains(ref, "://") && !strings.HasPrefix(ref, "data:") && !strings.HasPrefix(ref, "#") && !strings.HasPrefix(ref, "//")
}

func imageTextParts(body string) ([]string, error) {
	lines := strings.Split(body, "\n")
	codeLines := protectedImageLines(body)
	var parts []string
	var current []string
	var fence byte
	fenceLen := 0
	flush := func() {
		start, end := 0, len(current)
		for start < end && strings.TrimSpace(current[start]) == "" {
			start++
		}
		for end > start && strings.TrimSpace(current[end-1]) == "" {
			end--
		}
		parts = append(parts, strings.Join(current[start:end], "\n"))
		current = nil
	}
	for lineNo, line := range lines {
		if codeLines[lineNo] {
			current = append(current, line)
			continue
		}
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			n := 0
			for n < len(trimmed) && trimmed[n] == trimmed[0] {
				n++
			}
			if n >= 3 {
				if fence == 0 {
					fence, fenceLen = trimmed[0], n
				} else if trimmed[0] == fence && n >= fenceLen {
					fence = 0
				}
				current = append(current, line)
				continue
			}
		}
		if fence == 0 && isLocalImage(trimmed) {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	return parts, nil
}

func protectedImageLines(body string) map[int]bool {
	protected := make([]bool, len(body))
	doc := goldmark.DefaultParser().Parse(text.NewReader([]byte(body)))
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && (node.Kind() == ast.KindCodeBlock || node.Kind() == ast.KindFencedCodeBlock) {
			for i := 0; i < node.Lines().Len(); i++ {
				segment := node.Lines().At(i)
				for j := segment.Start; j < segment.Stop && j < len(protected); j++ {
					protected[j] = true
				}
			}
		}
		return ast.WalkContinue, nil
	})
	result := map[int]bool{}
	line := 0
	for i := range body {
		if protected[i] {
			result[line] = true
		}
		if body[i] == '\n' {
			line++
		}
	}
	return result
}

func hash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

func imageHashMap(images []Image) map[string]string {
	result := make(map[string]string, len(images))
	for _, image := range images {
		result[imageStateKey(image)] = imageSyncHash(image)
	}
	return result
}

func changedImages(images []Image, prior map[string]string) []Image {
	var changed []Image
	for _, image := range images {
		old, ok := prior[imageStateKey(image)]
		if !ok {
			old = prior[image.Path]
		} // read frontmatter written before image keys were introduced
		if old != imageSyncHash(image) {
			changed = append(changed, image)
		}
	}
	return changed
}

func imageSyncHash(image Image) string {
	if image.StateHash != "" {
		return image.StateHash
	}
	return image.Hash
}

func imageStateKey(image Image) string {
	if image.Key != "" {
		return image.Key
	}
	return image.Path
}

// pageTitle uses the first level-one ATX heading outside a fenced code block.
func pageTitle(body, filename string) string {
	var fence byte
	var fenceLen int
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent > 3 {
			continue
		}
		line = line[indent:]
		if len(line) >= 3 && (line[0] == '`' || line[0] == '~') {
			n := 0
			for n < len(line) && line[n] == line[0] {
				n++
			}
			if n >= 3 {
				if fence == 0 && (line[0] != '`' || !strings.Contains(line[n:], "`")) {
					fence, fenceLen = line[0], n
				} else if line[0] == fence && n >= fenceLen && strings.TrimSpace(line[n:]) == "" {
					fence = 0
				}
				continue
			}
		}
		if fence != 0 || len(line) < 2 || line[0] != '#' || (line[1] != ' ' && line[1] != '\t') {
			continue
		}
		title := strings.TrimSpace(line[1:])
		end := strings.TrimRight(title, "#")
		if len(end) < len(title) && len(end) > 0 && (end[len(end)-1] == ' ' || end[len(end)-1] == '\t') {
			title = strings.TrimSpace(end)
		}
		if title != "" {
			return title
		}
	}
	return strings.TrimSuffix(path.Base(filename), path.Ext(filename))
}

func yamlFrontmatter(data []byte) (*yaml.Node, string, string, error) {
	text := string(data)
	nl := "\n"
	if strings.HasPrefix(text, "---\r\n") {
		nl = "\r\n"
	}
	prefix := "---" + nl
	if !strings.HasPrefix(text, prefix) {
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}, text, nl, nil
	}
	rest := text[len(prefix):]
	var meta, body string
	if strings.HasPrefix(rest, "---"+nl) {
		body = rest[len("---"+nl):]
	} else if at := strings.Index(rest, nl+"---"+nl); at >= 0 {
		meta = rest[:at]
		body = rest[at+len(nl+"---"+nl):]
	} else if before, ok := strings.CutSuffix(rest, nl+"---"); ok {
		meta = before
	} else {
		return nil, "", "", fmt.Errorf("unclosed frontmatter")
	}
	if strings.TrimSpace(meta) == "" {
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}, body, nl, nil
	}
	var root yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(meta))
	if err := dec.Decode(&root); err != nil {
		return nil, "", "", fmt.Errorf("invalid YAML: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, "", "", fmt.Errorf("frontmatter must contain one YAML document")
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, "", "", fmt.Errorf("frontmatter must be a mapping")
	}
	return &root, body, nl, nil
}

func isNotionKey(key string) bool {
	return key == "notion-page-id" || key == "notion-parent-page-id" || key == "notion-database-id" || isFlatKey(key)
}

func frontmatter(data []byte) (*yaml.Node, string, string, error) {
	root, body, nl, err := yamlFrontmatter(data)
	if err != nil {
		return nil, "", "", err
	}
	commentNL := "\n"
	if strings.HasPrefix(body, "<!-- notion-sync\r\n") {
		commentNL = "\r\n"
		if !strings.HasPrefix(string(data), "---\n") {
			nl = commentNL
		}
	} else if !strings.HasPrefix(body, "<!-- notion-sync\n") {
		return root, body, nl, nil
	}
	prefix := "<!-- notion-sync" + commentNL
	rest := body[len(prefix):]
	at := strings.Index(rest, commentNL+"-->")
	if at < 0 {
		return nil, "", "", fmt.Errorf("unclosed notion-sync comment")
	}
	meta := rest[:at]
	body = rest[at+len(commentNL+"-->"):]
	if strings.HasPrefix(body, commentNL) {
		body = body[len(commentNL):]
	} else if body != "" {
		return nil, "", "", fmt.Errorf("invalid notion-sync comment closing line")
	}
	comment, _, _, err := yamlFrontmatter([]byte("---" + commentNL + meta + commentNL + "---" + commentNL))
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid notion-sync comment: %w", err)
	}
	for i := 0; i < len(comment.Content[0].Content); i += 2 {
		if !isNotionKey(comment.Content[0].Content[i].Value) {
			return nil, "", "", fmt.Errorf("invalid notion-sync comment key %q", comment.Content[0].Content[i].Value)
		}
	}
	root.Content[0].Content = append(root.Content[0].Content, comment.Content[0].Content...)
	return root, body, nl, nil
}
func parse(data []byte) (File, error) {
	f := File{Raw: append([]byte(nil), data...)}
	_, originalBody, _, err := yamlFrontmatter(data)
	if err != nil {
		return f, err
	}
	f.MetadataFormat = "frontmatter"
	if strings.HasPrefix(originalBody, "<!-- notion-sync\n") || strings.HasPrefix(originalBody, "<!-- notion-sync\r\n") {
		f.MetadataFormat = "comment"
	}
	root, body, _, err := frontmatter(data)
	if err != nil {
		return f, err
	}
	f.Body = body
	f.SyncBody = withoutHTMLComments(body)
	seen := map[string]bool{}
	node := root.Content[0]
	for i := 0; i < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
			return f, fmt.Errorf("frontmatter keys must be strings")
		}
		if seen[k.Value] {
			return f, fmt.Errorf("duplicate frontmatter key %q", k.Value)
		}
		seen[k.Value] = true
		switch k.Value {
		case "notion-page-id", "notion-parent-page-id", "notion-database-id":
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" || !config.ValidID(v.Value) {
				return f, fmt.Errorf("invalid Notion ID for %s", k.Value)
			}
			switch k.Value {
			case "notion-page-id":
				f.PageID = v.Value
			case "notion-parent-page-id":
				f.ParentID = v.Value
			case "notion-database-id":
				f.DatabaseID = v.Value
			}
		}
	}
	if err := readFlat(node, &f); err != nil {
		return f, err
	}
	if f.ParentID != "" && f.DatabaseID != "" {
		return f, fmt.Errorf("notion-parent-page-id and notion-database-id are mutually exclusive")
	}
	f.Hash = hash(f.SyncBody)
	return f, nil
}

func resolveImages(f *File, sourceRoot string) error {
	images, err := localImages(f.SyncBody, f.Path, sourceRoot)
	if err != nil {
		return err
	}
	f.Images = images
	if _, _, err := bodyWithoutImages(f.SyncBody); err != nil {
		return err
	}
	return nil
}

// FileRootID returns the previously configured root from a file's sync metadata.
func FileRootID(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	f, err := parse(data)
	if err != nil {
		return "", err
	}
	if f.Metadata == nil {
		return "", nil
	}
	if !config.ValidID(f.Metadata.RootID) {
		return "", fmt.Errorf("invalid root ID in sync metadata")
	}
	return f.Metadata.RootID, nil
}

func glob(pattern, name string) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	runes := []rune(pattern)
	var b bytes.Buffer
	b.WriteString("^")
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				i++
				if i+1 < len(runes) && runes[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			return false, fmt.Errorf("character classes are not supported in glob %q", pattern)
		default:
			b.WriteString(regexp.QuoteMeta(string(runes[i])))
		}
	}
	b.WriteString("$")
	r, err := regexp.Compile(b.String())
	if err != nil {
		return false, err
	}
	return r.MatchString(name), nil
}
func match(patterns []string, name string) (bool, error) {
	for _, p := range patterns {
		ok, err := glob(p, name)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// excludedTree only prunes patterns that exclude every descendant, not
// patterns such as dir/*.md that can leave nested files included.
func excludedTree(patterns []string, dir string) (bool, error) {
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(pattern)
		if pattern == "**" {
			return true, nil
		}
		if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
			matched, err := glob(prefix, dir)
			if err != nil || matched {
				return matched, err
			}
		}
	}
	return false, nil
}
func Scan(c config.Config) ([]File, error) {
	return scan(c, "")
}

// A single-file operation still reads metadata from other files, but only
// resolves images for the selected file.
func scan(c config.Config, selected string) ([]File, error) {
	root, err := filepath.EvalSymlinks(c.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	stat, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !stat.IsDir() {
		return nil, fmt.Errorf("source must be a directory")
	}
	var files []File
	err = filepath.WalkDir(root, func(full string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if full == root {
			return nil
		}
		rel, e := filepath.Rel(root, full)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			excluded, err := excludedTree(c.Sync.Exclude, rel)
			if err != nil {
				return err
			}
			if excluded {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.ToLower(path.Ext(rel)) != ".md" {
			return nil
		}
		inc, e := match(c.Sync.Include, rel)
		if e != nil {
			return e
		}
		exc, e := match(c.Sync.Exclude, rel)
		if e != nil {
			return e
		}
		if !inc || exc {
			return nil
		}
		data, e := os.ReadFile(full)
		if e != nil {
			return e
		}
		f, e := parse(data)
		if e != nil {
			return fmt.Errorf("%s: %w", rel, e)
		}
		f.Path = rel
		if selected == "" || selected == rel {
			if e = resolveImages(&f, root); e != nil {
				return fmt.Errorf("%s: %w", rel, e)
			}
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}
