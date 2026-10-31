package sync

import (
	"context"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/yuyj109/notion-sync/internal/config"
)

type pullAction struct {
	file         File
	entry        Entry
	body, remote string
}

var pullAttachment = regexp.MustCompile(`(?i)<(?:image|file|video|audio|pdf)\b`)

func pullKeys(lines []diffLine) []string {
	keys := make([]string, len(lines))
	for i := range lines {
		keys[i] = lines[i].key
	}
	return keys
}

// pullBody applies changed normalized lines while keeping unchanged source lines verbatim.
func pullBody(f File, remote string) (string, error) {
	if len(f.Images) != 0 || strings.Contains(remote, "![") || pullAttachment.MatchString(remote) {
		return "", fmt.Errorf("pull does not yet support pages containing images or files")
	}
	if withoutHTMLComments(f.Body) != f.Body {
		return "", fmt.Errorf("pull does not yet support files containing HTML comments")
	}
	localLines, remoteLines := diffLines(f.SyncBody), diffLines(remote)
	raw := strings.SplitAfter(f.Body, "\n")
	prepared := strings.SplitAfter(f.SyncBody, "\n")
	if len(raw) != len(prepared) {
		return "", fmt.Errorf("cannot safely map source lines to Notion content")
	}
	offsets := make([]int, len(raw)+1)
	for i, line := range raw {
		offsets[i+1] = offsets[i] + len(line)
	}
	type edit struct {
		start, end int
		value      string
	}
	var edits []edit
	matcher := difflib.NewMatcherWithJunk(pullKeys(localLines), pullKeys(remoteLines), false, nil)
	seen := map[string]string{}
	for _, line := range localLines {
		source := raw[line.line]
		if prior, ok := seen[line.key]; ok && prior != source {
			return "", fmt.Errorf("cannot safely map repeated equivalent lines with different source text")
		}
		seen[line.key] = source
	}
	for _, op := range matcher.GetOpCodes() {
		if op.Tag == 'e' {
			continue
		}
		start := len(f.Body)
		if op.I1 < len(localLines) {
			start = offsets[localLines[op.I1].line]
		}
		end := start
		if op.I2 > op.I1 {
			end = offsets[localLines[op.I2-1].line+1]
		}
		// A changed link can have a lossy Notion representation. Do not replace
		// it without a stored pre-sync body from which to restore its source form.
		if strings.Contains(f.Body[start:end], "](") || strings.Contains(f.Body[start:end], "<mention-") {
			return "", fmt.Errorf("cannot safely pull a changed line containing a link or mention")
		}
		var text strings.Builder
		for _, line := range remoteLines[op.J1:op.J2] {
			if strings.Contains(line.text, "<mention-") || strings.Contains(line.text, "<unknown") {
				return "", fmt.Errorf("cannot safely pull a new or changed Notion mention")
			}
			text.WriteString(line.text)
		}
		value := text.String()
		newline := "\n"
		if strings.Contains(f.Body, "\r\n") {
			newline = "\r\n"
			value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\n", newline)
		}
		if value != "" && start > 0 && f.Body[start-1] != '\n' {
			value = newline + value
		}
		edits = append(edits, edit{start, end, value})
	}
	body := f.Body
	for _, e := range slices.Backward(edits) {

		body = body[:e.start] + e.value + body[e.end:]
	}
	return body, nil
}

// Pull imports conflicts only. It never writes to Notion.
func Pull(ctx context.Context, c config.Config, api Client, apply bool, print func(string, ...any)) error {
	if apply {
		unlock, err := lock(c)
		if err != nil {
			return err
		}
		defer unlock()
	}
	s, err := loadState(c)
	if err != nil {
		return err
	}
	seenPages := map[string]string{}
	for key, entry := range s.Entries {
		if !config.ValidID(entry.PageID) || !config.ValidID(entry.ParentID) {
			return fmt.Errorf("invalid ID in state for %s", key)
		}
		id := norm(entry.PageID)
		if prior, ok := seenPages[id]; ok {
			return fmt.Errorf("same Notion page is assigned to %s and %s", prior, key)
		}
		seenPages[id] = key
	}
	files, err := Scan(c)
	if err != nil {
		return err
	}
	for i := range files {
		files[i] = *s.Documents[files[i].Path]
	}
	prepareLinks(files, s, c.SourcePath)
	var actions []pullAction
	for _, f := range files {
		e, ok := s.Entries["file:"+f.Path]
		if !ok {
			continue
		}
		if f.ParentID != "" {
			if e.ParentType != "page_id" || norm(e.ParentID) != norm(f.ParentID) || !strings.HasPrefix(e.Target, "page:") && e.Target != "parent:"+norm(f.ParentID) {
				return fmt.Errorf("%s: destination changed; resolve manually", f.Path)
			}
		} else if f.DatabaseID != "" {
			if !strings.HasPrefix(e.Target, "page:") && e.Target != "database:"+norm(f.DatabaseID) {
				return fmt.Errorf("%s: destination changed; resolve manually", f.Path)
			}
			ds, _, err := ResolveDataSource(ctx, api, f.DatabaseID)
			if err != nil {
				return fmt.Errorf("%s: %w", f.Path, err)
			}
			if e.ParentType != "data_source_id" || norm(e.ParentID) != norm(ds) {
				return fmt.Errorf("%s: destination changed; resolve manually", f.Path)
			}
		} else if strings.HasPrefix(e.Target, "hierarchy:") {
			expected := "hierarchy:"
			if dir := path.Dir(f.Path); c.Sync.PreserveHierarchy && dir != "." {
				expected += dir
			}
			if e.Target != expected {
				return fmt.Errorf("%s: destination changed; resolve manually", f.Path)
			}
		}
		p, err := getPage(ctx, api, e.PageID)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		id, kind := parentOf(p)
		if kind != e.ParentType || norm(id) != norm(e.ParentID) {
			return fmt.Errorf("%s: page was moved; resolve manually", f.Path)
		}
		m, err := getMarkdown(ctx, api, e.PageID)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		if remoteMarkdownHash(m.Markdown) == e.RemoteHash {
			continue
		}
		if e.LocalHash == "" || e.RemoteHash == "" {
			return fmt.Errorf("%s: incomplete synchronization baseline", f.Path)
		}
		comparisonFile, comparisonRemote, err := normalizePullAnchors(ctx, c, api, &s, f, m.Markdown)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		if f.Hash != e.LocalHash && !reflect.DeepEqual(pullKeys(diffLines(comparisonFile.SyncBody)), pullKeys(diffLines(comparisonRemote))) {
			return fmt.Errorf("%s: local and Notion contents both changed; resolve manually", f.Path)
		}
		body, err := pullBody(comparisonFile, comparisonRemote)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		actions = append(actions, pullAction{file: f, entry: e, body: body, remote: m.Markdown})
		print("pull %s <- %s", f.Path, pageURL(e.PageID))
		if d := makeContentDiff(f.Path, comparisonFile.SyncBody, comparisonRemote, sourceBodyOffset(f), 0); d.Plain != "" {
			flip := func(s string) string {
				s = strings.Replace(s, "--- notion/", "--- local/", 1)
				return strings.Replace(s, "+++ local/", "+++ notion/", 1)
			}
			d.Plain, d.Numbered = flip(d.Plain), flip(d.Numbered)
			print("%s", d)
		}
	}
	if len(actions) == 0 {
		print("no Notion changes to pull")
	}
	if !apply {
		return nil
	}
	for _, a := range actions {
		f := s.Documents[a.file.Path]
		if err := verifyFileUnchanged(c, f); err != nil {
			return err
		}
		p, err := getPage(ctx, api, a.entry.PageID)
		if err != nil {
			return err
		}
		parentID, parentType := parentOf(p)
		if parentType != a.entry.ParentType || norm(parentID) != norm(a.entry.ParentID) {
			return fmt.Errorf("%s: page was moved during pull", f.Path)
		}
		m, err := getMarkdown(ctx, api, a.entry.PageID)
		if err != nil {
			return err
		}
		if remoteMarkdownHash(m.Markdown) != remoteMarkdownHash(a.remote) {
			return fmt.Errorf("%s: Notion changed during pull; retry", f.Path)
		}
		updated := *f
		updated.Body = a.body
		updated.SyncBody = withoutHTMLComments(a.body)
		updated.Hash = hash(updated.SyncBody)
		prepared := []File{updated}
		prepareLinks(prepared, s, c.SourcePath)
		e := a.entry
		e.LocalHash = prepared[0].Hash
		e.RemoteHash = remoteMarkdownHash(m.Markdown)
		if f.Metadata == nil {
			return fmt.Errorf("%s: missing synchronization metadata", f.Path)
		}
		metadata := *f.Metadata
		metadata.Entry = &e
		if err := writeMetadataBody(c, f, &metadata, &a.body); err != nil {
			return err
		}
		s.Entries["file:"+f.Path] = e
		print("pulled %s <- %s", f.Path, pageURL(e.PageID))
	}
	return nil
}
