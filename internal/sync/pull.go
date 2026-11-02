package sync

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuyj109/notion-sync/internal/config"
)

type pullAction struct {
	file       File
	entry      Entry
	body       string
	remoteHash string
}

var pullAttachment = regexp.MustCompile(`(?i)<(?:image|file|video|audio|pdf)\b`)
var pullReferenceLink = regexp.MustCompile(`\[[^\]\n]+\]\[[^\]\n]*\]|(?i:<a\s)`)
var pullHTML = regexp.MustCompile(`<[^>]*>`)

// With media, only accept a text append when an exact prefix reproduces the
// saved remote baseline. This also verifies the existing image references.
func pullMediaAppend(f File, e Entry, remote string) (string, string, error) {
	if withoutHTMLComments(f.Body) != f.Body {
		return "", "", fmt.Errorf("pull does not yet support files containing HTML comments")
	}
	if len(f.Images) != len(e.ImageHashes) {
		return "", "", fmt.Errorf("local image configuration changed; resolve manually")
	}
	for _, image := range f.Images {
		prior, ok := e.ImageHashes[imageStateKey(image)]
		if !ok {
			prior, ok = e.ImageHashes[image.Path]
		}
		want := image.Hash
		if e.managedEmbeds[norm(e.ImageBlocks[imageStateKey(image)])] {
			want = hash("drawio-embed:" + image.Hash)
		}
		if !ok || prior != want {
			return "", "", fmt.Errorf("local image changed; resolve manually")
		}
	}
	cut := -1
	boundaries := 0
	for i := len(remote); i >= 0 && boundaries < 256; i-- {
		if i != len(remote) && (i == 0 || remote[i-1] != '\n') {
			continue
		}
		boundaries++
		prefix := remote[:i]
		trimmed := strings.TrimSuffix(prefix, "\n")
		if remoteMarkdownHash(prefix) == e.RemoteHash || remoteMarkdownHash(trimmed) == e.RemoteHash || len(e.managedEmbeds) > 0 && remoteMarkdownHash(strings.TrimSuffix(trimmed, "\n<empty-block/>")) == e.RemoteHash {
			if cut != -1 && cut != i {
				return "", "", fmt.Errorf("ambiguous Notion append; resolve manually")
			}
			cut = i
		}
	}
	if cut < 0 {
		return "", "", fmt.Errorf("cannot safely pull changes around images or embeds; only verified text appended to the page is supported")
	}
	addition := strings.TrimLeft(remote[cut:], "\r\n")
	if strings.TrimSpace(addition) == "" {
		return "", "", fmt.Errorf("no supported appended text to pull")
	}
	if strings.Contains(addition, "![") || pullAttachment.MatchString(addition) || pullHTML.MatchString(addition) {
		return "", "", fmt.Errorf("cannot safely pull appended images, embeds, or HTML")
	}
	added, err := pullBody(File{}, addition)
	if err != nil {
		return "", "", err
	}
	if strings.Contains(f.Body, "\r\n") {
		added = strings.ReplaceAll(strings.ReplaceAll(added, "\r\n", "\n"), "\n", "\r\n")
	}
	separator := ""
	if f.Body != "" && !strings.HasSuffix(f.Body, "\n") {
		separator = "\n"
		if strings.Contains(f.Body, "\r\n") {
			separator = "\r\n"
		}
	}
	return f.Body + separator + added, addition, nil
}

func containsPullLink(s string) bool {
	if pullReferenceLink.MatchString(s) {
		return true
	}
	doc := goldmark.DefaultParser().Parse(text.NewReader([]byte(s)))
	found := false
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && (node.Kind() == ast.KindLink || node.Kind() == ast.KindAutoLink) {
			found = true
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return found
}

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
	if withoutHTMLComments(f.Body) != f.Body || withoutHTMLComments(remote) != remote {
		return "", fmt.Errorf("pull does not yet support files containing HTML comments")
	}
	localLines, remoteLines := comparableLines(f.SyncBody, true), comparableLines(remote, true)
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
		// Keep source-only whitespace when removing it does not change the
		// rendered Markdown. Notion may omit such formatting on readback.
		if op.Tag == 'd' && strings.TrimSpace(f.Body[start:end]) == "" {
			var before, after bytes.Buffer
			if goldmark.Convert([]byte(f.Body), &before) == nil && goldmark.Convert([]byte(f.Body[:start]+f.Body[end:]), &after) == nil && bytes.Equal(before.Bytes(), after.Bytes()) {
				continue
			}
		}
		// A changed link can have a lossy Notion representation. Do not replace
		// it without a stored pre-sync body from which to restore its source form.
		if strings.Contains(f.Body[start:end], "](") || containsPullLink(f.Body[start:end]) || strings.Contains(f.Body[start:end], "<mention-") {
			return "", fmt.Errorf("cannot safely pull a changed line containing a link or mention")
		}
		var text strings.Builder
		for _, line := range remoteLines[op.J1:op.J2] {
			if strings.Contains(line.text, "](") || containsPullLink(line.text) || strings.Contains(line.text, "<mention-") || strings.Contains(line.text, "<unknown") {
				return "", fmt.Errorf("cannot safely pull a new or changed Notion link or mention")
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
	return pull(ctx, c, api, "", apply, print)
}

// PullFile imports supported changes for only the selected Markdown file.
func PullFile(ctx context.Context, c config.Config, api Client, filename string, apply bool, print func(string, ...any)) error {
	return pull(ctx, c, api, filename, apply, print)
}

func pull(ctx context.Context, c config.Config, api Client, filename string, apply bool, print func(string, ...any)) error {
	if apply {
		unlock, err := lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	selected := ""
	if filename != "" {
		root, err := filepath.EvalSymlinks(c.SourcePath)
		if err != nil {
			return err
		}
		abs, err := filepath.Abs(filename)
		if err != nil {
			return err
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("file must be a regular file, not a symlink")
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("file must be inside the configured source %s", c.SourcePath)
		}
		selected = filepath.ToSlash(rel)
		included, err := match(c.Sync.Include, selected)
		if err != nil {
			return err
		}
		excluded, err := match(c.Sync.Exclude, selected)
		if err != nil {
			return err
		}
		if !included || excluded || strings.ToLower(path.Ext(selected)) != ".md" {
			return fmt.Errorf("file is not included by the configuration")
		}
	}
	s, err := loadStateForFile(c, false, selected)
	if err != nil {
		return err
	}
	if selected != "" && s.Documents[selected] == nil {
		return fmt.Errorf("file is not included by the configuration")
	}
	if apply {
		if err := checkpointSharedMigration(c, &s); err != nil {
			return err
		}
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
	names := make([]string, 0, len(s.Documents))
	for name := range s.Documents {
		if selected == "" || selected == name {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	files := make([]File, 0, len(names))
	for _, name := range names {
		files = append(files, *s.Documents[name])
	}
	prepareLinks(files, s, c.SourcePath)
	var actions []pullAction
	for _, f := range files {
		e, ok := s.Entries["file:"+f.Path]
		if !ok {
			if selected != "" {
				return fmt.Errorf("%s: file has no synchronization baseline", f.Path)
			}
			continue
		}
		switch {
		case f.ParentID != "":
			if e.ParentType != "page_id" || norm(e.ParentID) != norm(f.ParentID) || !strings.HasPrefix(e.Target, "page:") && e.Target != "parent:"+norm(f.ParentID) {
				return fmt.Errorf("%s: destination changed; resolve manually", f.Path)
			}
		case f.DatabaseID != "":
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
		case strings.HasPrefix(e.Target, "hierarchy:"):
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
		remote := m.Markdown
		if selected != "" && len(f.Images) > 0 {
			if err := identifyManagedDrawioEmbeds(ctx, api, &e, f.Images); err != nil {
				return err
			}
			if len(e.managedEmbeds) > 0 {
				remote, _, err = normalizeManagedEmbeds(ctx, api, e, remote)
				if err != nil {
					return fmt.Errorf("%s: %w", f.Path, err)
				}
			}
		}
		if remoteMarkdownHash(remote) == e.RemoteHash || len(e.managedEmbeds) > 0 && remoteMarkdownHash(strings.TrimSuffix(remote, "\n<empty-block/>")) == e.RemoteHash {
			continue
		}
		if e.LocalHash == "" || e.RemoteHash == "" {
			return fmt.Errorf("%s: incomplete synchronization baseline", f.Path)
		}
		comparisonFile, comparisonRemote := f, remote
		if selected != "" && len(f.Images) > 0 {
			if f.Hash != e.LocalHash {
				return fmt.Errorf("%s: local and Notion contents both changed; resolve manually", f.Path)
			}
			body, addition, err := pullMediaAppend(f, e, remote)
			if err != nil {
				return fmt.Errorf("%s: %w", f.Path, err)
			}
			actions = append(actions, pullAction{file: f, entry: e, body: body, remoteHash: remoteMarkdownHash(remote)})
			print("pull %s <- %s", f.Path, pageURL(e.PageID))
			print("append from Notion:\n%s", redactDisplayURLs(addition))
			continue
		}
		if f.Hash != e.LocalHash && !reflect.DeepEqual(pullKeys(comparableLines(comparisonFile.SyncBody, true)), pullKeys(comparableLines(comparisonRemote, true))) {
			return fmt.Errorf("%s: local and Notion contents both changed; resolve manually", f.Path)
		}
		body, err := pullBody(comparisonFile, comparisonRemote)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		actions = append(actions, pullAction{file: f, entry: e, body: body, remoteHash: remoteMarkdownHash(remote)})
		print("pull %s <- %s", f.Path, pageURL(e.PageID))
		if d := makeContentDiffWithBlanks(f.Path, comparisonFile.SyncBody, comparisonRemote, sourceBodyOffset(f), 0, true); d.Plain != "" {
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
		for _, img := range a.file.Images {
			if err := verifyImageUnchanged(filepath.Join(c.SourcePath, filepath.FromSlash(img.Path)), img.Hash); err != nil {
				return fmt.Errorf("%s: %w", f.Path, err)
			}
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
		currentRemote := m.Markdown
		if len(a.entry.managedEmbeds) > 0 {
			currentRemote, _, err = normalizeManagedEmbeds(ctx, api, a.entry, currentRemote)
			if err != nil {
				return err
			}
		}
		if remoteMarkdownHash(currentRemote) != a.remoteHash {
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
		e.RemoteHash = remoteMarkdownHash(currentRemote)
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
