package sync

import (
	"html"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

var inlineMarkdownLink = regexp.MustCompile(`\[[^\]\n]*\]\((<[^>\n]+>|[^\s)]+)(?:\s+"[^"\n]*")?\)`)

var literalMarkdown = strings.NewReplacer(
	`\`, `\\`, `*`, `\*`, `_`, `\_`, `~`, `\~`, "`", "\\`", `$`, `\$`,
	`[`, `\[`, `]`, `\]`, `<`, `\<`, `>`, `\>`, `{`, `\{`, `}`, `\}`, `|`, `\|`, `^`, `\^`,
)

type gitRepository struct{ root, source, commit, remote string }

func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func openGitRepository(source string) *gitRepository {
	source, err := filepath.Abs(source)
	if err != nil {
		return nil
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return nil
	}
	root := gitOutput(source, "rev-parse", "--show-toplevel")
	commit := gitOutput(source, "rev-parse", "--verify", "HEAD^{commit}")
	remote := gitOutput(source, "remote", "get-url", "origin")
	if after, ok := strings.CutPrefix(remote, "git@github.com:"); ok {
		remote = "https://github.com/" + after
	}
	u, err := url.Parse(remote)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || root == "" || commit == "" {
		return nil
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil
	}
	return &gitRepository{root: root, source: source, commit: commit, remote: "https://github.com/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])}
}

func (r *gitRepository) urlFor(target string) string {
	full := filepath.Join(r.source, filepath.FromSlash(target))
	rel, err := filepath.Rel(r.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if gitOutput(r.root, "cat-file", "-t", r.commit+":"+rel) != "blob" {
		return ""
	}
	parts := strings.Split(rel, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return r.remote + "/blob/" + r.commit + "/" + strings.Join(parts, "/")
}

// resolveLinks leaves source Markdown untouched. Only links to known pages are rewritten.
func resolveLinks(body, filename string, documents map[string]*File, entries map[string]Entry, repositories ...*gitRepository) string {
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
		if err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(u.Path, "/") || u.Path == "" {
			continue
		}
		target := path.Clean(path.Join(path.Dir(filename), u.Path))
		if target == ".." || strings.HasPrefix(target, "../") {
			labelEnd := strings.Index(body[start:ds], "](")
			if labelEnd < 1 {
				continue
			}
			label := body[start+1 : start+labelEnd]
			out.WriteString(body[last:start])
			var link string
			if len(repositories) > 0 && repositories[0] != nil && u.RawQuery == "" && u.Fragment == "" {
				link = repositories[0].urlFor(target)
			}
			if link == "" {
				out.WriteString(literalMarkdown.Replace(body[start:end]))
			} else {
				out.WriteString("[" + label + "](" + link + ")")
			}
			last = end
			continue
		}
		if u.RawQuery != "" || u.Fragment != "" || !strings.EqualFold(path.Ext(u.Path), ".md") {
			continue
		}
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

func prepareLinks(files []File, s State, sourceRoot string) {
	repo := openGitRepository(sourceRoot)
	for i := range files {
		f := &files[i]
		resolved := resolveLinks(f.SyncBody, f.Path, s.Documents, s.Entries, repo)
		if resolved != f.SyncBody {
			f.Hash = hash(f.SyncBody + "\x00" + resolved)
			f.SyncBody = resolved
		}
	}
}
