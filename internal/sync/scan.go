package sync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	RootNeedsFormatting                            bool
}

// Image is a local Markdown image reference that can be uploaded to Notion.
type Image struct {
	Alt, Path, Name, ContentType, Hash, Key string
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
	return remoteImageRE.ReplaceAllStringFunc(body, func(value string) string {
		parts := remoteImageRE.FindStringSubmatch(value)
		if len(parts) < 3 {
			return value
		}
		urlText := strings.Trim(parts[2], "<>")
		parsed, err := url.Parse(urlText)
		if err != nil || parsed.Scheme == "" {
			return value
		}
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		return "![" + parts[1] + "](<" + parsed.String() + ">)" + parts[3]
	})
}

// localImages resolves standalone Markdown image lines beneath the source root.
// The image URLs are not rewritten here, so hashes continue to represent source Markdown.
func localImages(body, markdownPath, sourceRoot string) ([]Image, error) {
	lines := strings.Split(body, "\n")
	var images []Image
	var fence byte
	var fenceLen int
	for _, line := range lines {
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
		images = append(images, Image{Alt: match[1], Path: filepath.ToSlash(rel), Name: filepath.Base(resolved), ContentType: contentType, Hash: hash(string(data)), Caption: caption, Key: key})
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
	default:
		return ""
	}
}

func bodyWithoutImages(body string, markdownPath string) (string, []int, error) {
	lines := strings.Split(body, "\n")
	var indices []int
	var textLines []string
	var fence byte
	var fenceLen int
	for i, line := range lines {
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
	for _, line := range lines {
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

func hash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

func imageHashMap(images []Image) map[string]string {
	result := make(map[string]string, len(images))
	for _, image := range images {
		result[imageStateKey(image)] = image.Hash
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
		if old != image.Hash {
			changed = append(changed, image)
		}
	}
	return changed
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

func frontmatter(data []byte) (*yaml.Node, string, string, error) {
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
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, "", "", fmt.Errorf("frontmatter must contain one YAML document")
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, "", "", fmt.Errorf("frontmatter must be a mapping")
	}
	return &root, body, nl, nil
}
func parse(data []byte) (File, error) {
	f := File{Raw: append([]byte(nil), data...)}
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
	if _, _, err := bodyWithoutImages(f.SyncBody, f.Path); err != nil {
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
	var b bytes.Buffer
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
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
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
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
func Scan(c config.Config) ([]File, error) {
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
		if e = resolveImages(&f, root); e != nil {
			return fmt.Errorf("%s: %w", rel, e)
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
