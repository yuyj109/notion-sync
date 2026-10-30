package sync

import (
	"html"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

var inlineMarkdownLink = regexp.MustCompile(`\[[^\]\n]*\]\((<[^>\n]+>|[^\s)]+)(?:\s+"[^"\n]*")?\)`)

// resolveLinks leaves source Markdown untouched. Only links to known pages are rewritten.
func resolveLinks(body, filename string, documents map[string]*File, entries map[string]Entry) string {
	source := []byte(body)
	protected := make([]bool, len(source))
	mark := func(start, end int) {
		for i := start; i < end && i < len(protected); i++ {
			protected[i] = true
		}
	}
	doc := goldmark.DefaultParser().Parse(text.NewReader(source))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindCodeBlock, ast.KindFencedCodeBlock:
			for i := 0; i < n.Lines().Len(); i++ {
				s := n.Lines().At(i)
				mark(s.Start, s.Stop)
			}
		case ast.KindCodeSpan:
			for c := n.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					mark(t.Segment.Start, t.Segment.Stop)
				}
			}
		}
		return ast.WalkContinue, nil
	})
	var out strings.Builder
	last := 0
	for _, m := range inlineMarkdownLink.FindAllStringSubmatchIndex(body, -1) {
		start, end, ds, de := m[0], m[1], m[2], m[3]
		if protected[start] || escapedAt(body, start) || start > 0 && body[start-1] == '!' {
			continue
		}
		dest := strings.Trim(body[ds:de], "<>")
		u, err := url.Parse(dest)
		if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || strings.HasPrefix(u.Path, "/") || !strings.EqualFold(path.Ext(u.Path), ".md") {
			continue
		}
		target := path.Clean(path.Join(path.Dir(filename), u.Path))
		if target == ".." || strings.HasPrefix(target, "../") || documents[target] == nil {
			continue
		}
		id := entries["file:"+target].PageID
		if id == "" {
			id = documents[target].PageID
		}
		if id == "" {
			continue
		}
		labelEnd := strings.Index(body[start:ds], "](")
		if labelEnd < 1 {
			continue
		}
		label := body[start+1 : start+labelEnd]
		out.WriteString(body[last:start])
		out.WriteString(`<mention-page url="` + pageURL(id) + `">` + html.EscapeString(label) + `</mention-page>`)
		last = end
	}
	out.WriteString(body[last:])
	return out.String()
}

func prepareLinks(files []File, s State) {
	for i := range files {
		f := &files[i]
		resolved := resolveLinks(f.SyncBody, f.Path, s.Documents, s.Entries)
		if resolved != f.SyncBody {
			f.Hash = hash(f.SyncBody + "\x00" + resolved)
			f.SyncBody = resolved
		}
	}
}
