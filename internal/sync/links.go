package sync

import (
	"html"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
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
	result, _ := resolveLinksWithAnchors(body, filename, documents, entries, repositories...)
	return result
}

func resolveLinksWithAnchors(body, filename string, documents map[string]*File, entries map[string]Entry, repositories ...*gitRepository) (string, []AnchorRef) {
	protected := protectedMarkdown(body)
	var out strings.Builder
	var anchors []AnchorRef
	occurrences := map[string]int{}
	last := 0
	for _, m := range inlineMarkdownLink.FindAllStringSubmatchIndex(body, -1) {
		start, end, ds, de := m[0], m[1], m[2], m[3]
		if protected[start] || escapedAt(body, start) || start > 0 && body[start-1] == '!' {
			continue
		}
		dest := strings.Trim(body[ds:de], "<>")
		u, err := url.Parse(dest)
		if err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(u.Path, "/") || u.Path == "" && u.Fragment == "" {
			continue
		}
		target := filename
		if u.Path != "" {
			target = path.Clean(path.Join(path.Dir(filename), u.Path))
		}
		if target == ".." || strings.HasPrefix(target, "../") {
			labelEnd := strings.Index(body[start:ds], "](")
			if labelEnd < 1 {
				continue
			}
			label := body[start+1 : start+labelEnd]
			out.WriteString(body[last:start])
			var link string
			if len(repositories) > 0 && repositories[0] != nil && u.RawQuery == "" && !u.ForceQuery {
				link = repositories[0].urlFor(target)
				if link != "" && u.Fragment != "" {
					link += "#" + u.EscapedFragment()
				}
			}
			if link == "" {
				out.WriteString(literalMarkdown.Replace(body[start:end]))
			} else {
				out.WriteString("[" + label + "](" + link + ")")
			}
			last = end
			continue
		}
		if u.RawQuery != "" || u.ForceQuery || !strings.EqualFold(path.Ext(target), ".md") {
			continue
		}
		if target == ".." || strings.HasPrefix(target, "../") || documents[target] == nil {
			if u.Fragment != "" {
				anchors = append(anchors, AnchorRef{Target: target, Fragment: u.Fragment, Reason: "target is not included"})
				out.WriteString(body[last:start])
				out.WriteString(literalMarkdown.Replace(body[start:end]))
				last = end
			}
			continue
		}
		id := entries["file:"+target].PageID
		if id == "" {
			id = documents[target].PageID
		}
		if id == "" {
			if u.Fragment != "" {
				anchors = append(anchors, AnchorRef{Target: target, Fragment: u.Fragment, Reason: "target is not synchronized yet"})
				out.WriteString(body[last:start])
				out.WriteString(literalMarkdown.Replace(body[start:end]))
				last = end
			}
			continue
		}
		labelEnd := strings.Index(body[start:ds], "](")
		if labelEnd < 1 {
			continue
		}
		label := body[start+1 : start+labelEnd]
		if u.Fragment != "" {
			reason := ""
			if _, ok := entries["file:"+target]; !ok {
				reason = "target is not synchronized yet"
			} else if !localHeadingExists(documents[target].SyncBody, u.Fragment) {
				reason = "heading not found"
			}
			if reason != "" {
				anchors = append(anchors, AnchorRef{Target: target, Fragment: u.Fragment, Reason: reason})
				out.WriteString(body[last:start])
				out.WriteString(literalMarkdown.Replace(body[start:end]))
				last = end
				continue
			}
			identity := filename + "\x00" + target + "\x00" + u.Fragment
			token := hash(identity + "\x00" + strconv.Itoa(occurrences[identity]))
			occurrences[identity]++
			anchors = append(anchors, AnchorRef{Token: token, Target: target, Fragment: u.Fragment, Label: anchorLabel(label)})
			out.WriteString(body[last:start])
			out.WriteString("[" + label + "](" + anchorPlaceholder(id, token) + ")")
			last = end
			continue
		}
		out.WriteString(body[last:start])
		out.WriteString(`<mention-page url="` + pageURL(id) + `">` + html.EscapeString(label) + `</mention-page>`)
		last = end
	}
	out.WriteString(body[last:])
	return out.String(), anchors
}

func protectedMarkdown(body string) []bool {
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
	return protected
}

func referencesHeading(body, filename, target string) bool {
	return headingReferenceTargets(body, filename)[target]
}

func headingReferenceTargets(body, filename string) map[string]bool {
	protected := protectedMarkdown(body)
	targets := map[string]bool{}
	for _, m := range inlineMarkdownLink.FindAllStringSubmatchIndex(body, -1) {
		start, ds, de := m[0], m[2], m[3]
		if protected[start] || escapedAt(body, start) || start > 0 && body[start-1] == '!' {
			continue
		}
		u, err := url.Parse(strings.Trim(body[ds:de], "<>"))
		if err != nil || u.IsAbs() || u.Host != "" || u.Fragment == "" || strings.HasPrefix(u.Path, "/") {
			continue
		}
		resolved := filename
		if u.Path != "" {
			resolved = path.Clean(path.Join(path.Dir(filename), u.Path))
		}
		targets[resolved] = true
	}
	return targets
}

func prepareLinks(files []File, s State, sourceRoot string) {
	repo := openGitRepository(sourceRoot)
	for i := range files {
		f := &files[i]
		resolved, anchors := resolveLinksWithAnchors(f.SyncBody, f.Path, s.Documents, s.Entries, repo)
		f.Anchors = anchors
		if resolved != f.SyncBody {
			f.Hash = hash(f.SyncBody + "\x00" + resolved)
			f.SyncBody = resolved
		}
	}
}
