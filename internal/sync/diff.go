package sync

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuyj109/notion-sync/internal/config"
)

var diffMention = regexp.MustCompile(`<mention-page\s+url="([^"]+)"\s*(?:/>|>[^<]*</mention-page>)`)
var diffPageID = regexp.MustCompile(`(?i)([0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
var diffNotionURL = regexp.MustCompile(`https://(?:www\.|app\.)?notion\.(?:so|com)/[^\s<>)]+`)
var diffEnclosedAnchor = regexp.MustCompile(`\(<(https://notion\.so/[0-9a-f-]+\?notion-sync-anchor=[0-9a-f]{64})>(\s+"[^"]*")?\)`)

// Keep legacy anchor URL normalization for readable diffs during migration.
const anchorQuery = "notion-sync-anchor"

func anchorPlaceholder(pageID, token string) string {
	return pageURL(pageID) + "?" + anchorQuery + "=" + token
}

func normalizeAnchorURLs(body string, permitted map[string]bool) string {
	body = diffNotionURL.ReplaceAllStringFunc(body, func(value string) string {
		u, err := url.Parse(value)
		if err != nil {
			return value
		}
		token := u.Query().Get(anchorQuery)
		pageID := diffPageID.FindString(u.Path)
		if len(token) != 64 || !config.ValidID(pageID) || permitted != nil && !permitted[token] {
			return value
		}
		return anchorPlaceholder(pageID, token)
	})
	return diffEnclosedAnchor.ReplaceAllStringFunc(body, func(value string) string {
		parts := diffEnclosedAnchor.FindStringSubmatch(value)
		u, err := url.Parse(parts[1])
		if err != nil || permitted != nil && !permitted[u.Query().Get(anchorQuery)] {
			return value
		}
		return "(" + parts[1] + parts[2] + ")"
	})
}

type diffLine struct {
	text, key string
	line      int
	code      bool
}

// diffLines normalizes display-only differences. It never changes the sync baseline.
func diffLines(body string) []diffLine {
	return comparableLines(body, false)
}

func comparableLines(body string, includeBlank bool) []diffLine {
	if body == "" {
		return nil
	}
	source := []byte(body)
	protected := make([]bool, len(source))
	mark := func(start, end int) {
		for i := start; i < end && i < len(protected); i++ {
			protected[i] = true
		}
	}
	doc := goldmark.DefaultParser().Parse(text.NewReader(source))
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
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
	var result []diffLine
	offset := 0
	for lineNo, raw := range strings.SplitAfter(body, "\n") {
		if raw == "" {
			continue
		}
		rawLen := len(raw)
		isCode := false
		for i := offset; i < offset+len(raw) && i < len(protected); i++ {
			if protected[i] {
				isCode = true
				break
			}
		}
		trimmed := strings.TrimSpace(raw)
		if !isCode && (trimmed == "<empty-block/>" || !includeBlank && trimmed == "") {
			offset += len(raw)
			continue
		}
		key := raw
		if !isCode {
			key = normalizeAnchorURLs(key, nil)
			key = diffMention.ReplaceAllStringFunc(key, func(mention string) string {
				parts := diffMention.FindStringSubmatch(mention)
				u, err := url.Parse(parts[1])
				if err != nil || u.Scheme != "https" || u.RawQuery != "" || u.Fragment != "" {
					return mention
				}
				switch u.Hostname() {
				case "notion.so", "www.notion.so", "notion.com", "www.notion.com", "app.notion.com":
				default:
					return mention
				}
				id := diffPageID.FindString(u.Path)
				if !config.ValidID(id) {
					return mention
				}
				return "<mention-page:" + config.NormalizeID(id) + ">"
			})
			key = strings.TrimSuffix(strings.TrimSuffix(key, "\n"), "\r") + "\n"
			raw = strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r") + "\n"
		}
		result = append(result, diffLine{text: raw, key: key, line: lineNo, code: isCode})
		offset += rawLen
	}
	return result
}

// contentDiff compares the current Notion Markdown with the prepared local body.
// Notion may normalize Markdown or render images differently from the source.
type DiffOutput struct{ Plain, Numbered string }

func (d DiffOutput) String() string       { return d.Plain }
func (d DiffOutput) NumberedDiff() string { return d.Numbered }

func contentDiff(filename, remote, local string) string {
	return makeContentDiff(filename, remote, local, 0, 0).Plain
}

func sourceBodyOffset(f File) int {
	if !strings.HasSuffix(string(f.Raw), f.Body) {
		return 0
	}
	return strings.Count(string(f.Raw[:len(f.Raw)-len(f.Body)]), "\n")
}

func makeContentDiff(filename, remote, local string, oldOffset, newOffset int) DiffOutput {
	return makeContentDiffWithBlanks(filename, remote, local, oldOffset, newOffset, false)
}

func makeContentDiffWithBlanks(filename, remote, local string, oldOffset, newOffset int, includeBlank bool) DiffOutput {
	return makeDisplayDiff(filename, remote, local, oldOffset, newOffset, includeBlank, nil, nil)
}

// listDepths uses Markdown structure rather than raw indentation, which Notion
// can serialize differently. This is only used for display, never for pull.
func listDepths(body string) map[int]int {
	result := map[int]int{}
	source := []byte(body)
	doc := goldmark.DefaultParser().Parse(text.NewReader(source))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || n.Kind() != ast.KindListItem {
			return ast.WalkContinue, nil
		}
		depth := 0
		for p := n.Parent(); p != nil; p = p.Parent() {
			if p.Kind() == ast.KindListItem {
				depth++
			}
		}
		var first ast.Node
		_ = ast.Walk(n, func(child ast.Node, in bool) (ast.WalkStatus, error) {
			if in && child != n && child.Type() == ast.TypeBlock && child.Lines().Len() > 0 {
				first = child
				return ast.WalkStop, nil
			}
			return ast.WalkContinue, nil
		})
		if first != nil {
			start := first.Lines().At(0).Start
			line := strings.Count(body[:start], "\n")
			result[line] = depth
		}
		return ast.WalkContinue, nil
	})
	return result
}

func normalizeListDiff(lines []diffLine, body string) {
	depths := listDepths(body)
	for i := range lines {
		if depth, ok := depths[lines[i].line]; ok && !lines[i].code {
			lines[i].key = strings.Repeat("\t", depth) + strings.TrimLeft(lines[i].key, " \t")
		}
	}
}

// matchDiffImages only pairs unambiguous references to a recorded block with
// an unchanged local image. It cannot prove the remote image bytes are unchanged.
func matchDiffImages(a, b []diffLine, f *File, e *Entry) bool {
	if f == nil || e == nil {
		return false
	}
	matched := false
	blockCounts := map[string]int{}
	for _, block := range e.ImageBlocks {
		if config.ValidID(block) {
			blockCounts[norm(block)]++
		}
	}
	for _, img := range f.Images {
		key := imageStateKey(img)
		id := e.ImageBlocks[key]
		localParts := remoteImageRE.FindStringSubmatch(img.SourceLine)
		if !config.ValidID(id) || blockCounts[norm(id)] != 1 || e.ImageHashes[key] != imageSyncHash(img) || len(localParts) == 0 {
			continue
		}
		var remote, local []int
		for i, line := range a {
			if line.code {
				continue
			}
			parts := remoteImageRE.FindStringSubmatch(strings.TrimSpace(line.text))
			if len(parts) == 0 || parts[1] != img.Alt || parts[3] != localParts[3] || strings.TrimSpace(line.text) != parts[0] {
				continue
			}
			u, err := url.Parse(strings.Trim(parts[2], "<>"))
			if err != nil {
				continue
			}
			fragment, ok := strings.CutPrefix(u.Fragment, "notion_record=block.")
			if ok && config.ValidID(strings.Split(fragment, ".")[0]) && norm(strings.Split(fragment, ".")[0]) == norm(id) && imageMarkdownKey(parts[2]) != "" {
				remote = append(remote, i)
			}
		}
		for i, line := range b {
			if !line.code && strings.TrimSpace(line.text) == img.SourceLine {
				local = append(local, i)
			}
		}
		if len(remote) == 1 && len(local) == 1 {
			marker := "notion-sync-display-image:" + norm(id)
			a[remote[0]].key, b[local[0]].key = marker, marker
			matched = true
		}
	}
	return matched
}

func makeDisplayDiff(filename, remote, local string, oldOffset, newOffset int, includeBlank bool, f *File, e *Entry) DiffOutput {
	remote = normalizeRemoteVideos(normalizeRemoteImages(redactDisplayURLs(remote), true), true)
	local = normalizeRemoteVideos(normalizeRemoteImages(redactDisplayURLs(local), true), true)
	a, b := comparableLines(remote, includeBlank), comparableLines(local, includeBlank)
	normalizeListDiff(a, remote)
	normalizeListDiff(b, local)
	pairedImages := matchDiffImages(a, b, f, e)
	keys := func(lines []diffLine) []string {
		result := make([]string, len(lines))
		for i, line := range lines {
			result[i] = line.key
		}
		return result
	}
	matcher := difflib.NewMatcherWithJunk(keys(a), keys(b), false, nil)
	groups := matcher.GetGroupedOpCodes(3)
	const note = "note: matched image block(s) are shown as context; Notion image contents were not independently verified and may have changed\n"
	if len(groups) == 0 {
		if pairedImages {
			message := strings.TrimSuffix(note, "\n")
			return DiffOutput{Plain: message, Numbered: message}
		}
		return DiffOutput{}
	}
	var plain, numbered strings.Builder
	header := fmt.Sprintf("--- notion/%s (normalized)\n+++ local/%s (normalized)\n", filename, filename)
	plain.WriteString(header)
	numbered.WriteString(header)
	if pairedImages {
		plain.WriteString(note)
		numbered.WriteString(note)
	}
	width := 4
	if len(a) > 0 {
		width = max(width, len(strconv.Itoa(a[len(a)-1].line+oldOffset+1)))
	}
	if len(b) > 0 {
		width = max(width, len(strconv.Itoa(b[len(b)-1].line+newOffset+1)))
	}
	write := func(prefix, line string, oldLine, newLine int) {
		plain.WriteString(prefix)
		plain.WriteString(line)
		oldText, newText := "", ""
		if oldLine > 0 {
			oldText = strconv.Itoa(oldLine)
		}
		if newLine > 0 {
			newText = strconv.Itoa(newLine)
		}
		fmt.Fprintf(&numbered, "%s%*s %*s │ %s", prefix, width, oldText, width, newText, line)
		if !strings.HasSuffix(line, "\n") {
			plain.WriteString("\n\\ No newline at end of file\n")
			numbered.WriteString("\n\\ No newline at end of file\n")
		}
	}
	for _, group := range groups {
		first, last := group[0], group[len(group)-1]
		oldStart, newStart := first.I1+1, first.J1+1
		if first.I1 == last.I2 {
			oldStart--
		}
		if first.J1 == last.J2 {
			newStart--
		}
		hunk := fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", oldStart, last.I2-first.I1, newStart, last.J2-first.J1)
		plain.WriteString(hunk)
		numbered.WriteString(hunk)
		for _, op := range group {
			switch op.Tag {
			case 'e':
				for i, line := range a[op.I1:op.I2] {
					write(" ", line.text, line.line+oldOffset+1, b[op.J1+i].line+newOffset+1)
				}
			case 'r', 'd', 'i':
				for _, line := range a[op.I1:op.I2] {
					write("-", line.text, line.line+oldOffset+1, 0)
				}
				for _, line := range b[op.J1:op.J2] {
					write("+", line.text, 0, line.line+newOffset+1)
				}
			}
		}
	}
	return DiffOutput{Plain: strings.TrimSuffix(plain.String(), "\n"), Numbered: strings.TrimSuffix(numbered.String(), "\n")}
}
