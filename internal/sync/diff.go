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

type diffLine struct {
	text, key string
	line      int
}

// diffLines normalizes display-only differences. It never changes the sync baseline.
func diffLines(body string) []diffLine {
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
		if !isCode && (trimmed == "" || trimmed == "<empty-block/>") {
			offset += len(raw)
			continue
		}
		key := raw
		if !isCode {
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
		result = append(result, diffLine{text: raw, key: key, line: lineNo})
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
	remote = canonicalRemoteMarkdown(remote)
	a, b := diffLines(remote), diffLines(local)
	keys := func(lines []diffLine) []string {
		result := make([]string, len(lines))
		for i, line := range lines {
			result[i] = line.key
		}
		return result
	}
	matcher := difflib.NewMatcherWithJunk(keys(a), keys(b), false, nil)
	groups := matcher.GetGroupedOpCodes(3)
	if len(groups) == 0 {
		return DiffOutput{}
	}
	var plain, numbered strings.Builder
	header := fmt.Sprintf("--- notion/%s (normalized)\n+++ local/%s (normalized)\n", filename, filename)
	plain.WriteString(header)
	numbered.WriteString(header)
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
