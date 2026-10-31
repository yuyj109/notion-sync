package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"github.com/yuyj109/notion-sync/internal/config"
)

const anchorQuery = "notion-sync-anchor"

type AnchorRef struct{ Token, Target, Fragment, Label, Reason string }

func anchorPlaceholder(pageID, token string) string {
	return pageURL(pageID) + "?" + anchorQuery + "=" + token
}

func anchorLabel(label string) string {
	source := []byte("[" + label + "](https://example.invalid)")
	doc := goldmark.DefaultParser().Parse(text.NewReader(source))
	var result strings.Builder
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			switch part := n.(type) {
			case *ast.Text:
				result.Write(util.ResolveNumericReferences(util.ResolveEntityNames(util.UnescapePunctuations(part.Value(source)))))
			case *ast.String:
				result.Write(part.Value)
			}
		}
		return ast.WalkContinue, nil
	})
	return result.String()
}

type headingSlugger struct {
	counts map[string]int
	used   map[string]bool
}

func (s *headingSlugger) next(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r), unicode.IsMark(r), r == '-', r == '_':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('-')
		}
	}
	base := b.String()
	if s.counts == nil {
		s.counts = map[string]int{}
		s.used = map[string]bool{}
	}
	candidate := base
	for s.used[candidate] {
		s.counts[base]++
		candidate = fmt.Sprintf("%s-%d", base, s.counts[base])
	}
	s.used[candidate] = true
	return candidate
}

func localHeadingExists(body, fragment string) bool {
	source := []byte(body)
	doc := goldmark.DefaultParser().Parse(text.NewReader(source))
	var slugger headingSlugger
	found := false
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || n.Kind() != ast.KindHeading {
			return ast.WalkContinue, nil
		}
		var title strings.Builder
		_ = ast.Walk(n, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
			if entering {
				switch part := child.(type) {
				case *ast.Text:
					title.Write(util.ResolveNumericReferences(util.ResolveEntityNames(util.UnescapePunctuations(part.Value(source)))))
				case *ast.String:
					title.Write(part.Value)
				}
			}
			return ast.WalkContinue, nil
		})
		if slugger.next(title.String()) == fragment {
			found = true
		}
		return ast.WalkContinue, nil
	})
	return found
}

type notionAnchorBlock struct {
	Object      string                     `json:"object"`
	ID          string                     `json:"id"`
	Type        string                     `json:"type"`
	HasChildren bool                       `json:"has_children"`
	Data        map[string]json.RawMessage `json:"-"`
}

func (b *notionAnchorBlock) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw["object"], &b.Object); err != nil {
		return err
	}
	if err := json.Unmarshal(raw["id"], &b.ID); err != nil {
		return err
	}
	if err := json.Unmarshal(raw["type"], &b.Type); err != nil {
		return err
	}
	if value, ok := raw["has_children"]; ok {
		if err := json.Unmarshal(value, &b.HasChildren); err != nil {
			return err
		}
	}
	b.Data = raw
	return nil
}

func listAnchorBlocks(ctx context.Context, api Client, pageID string) ([]notionAnchorBlock, error) {
	var blocks []notionAnchorBlock
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visited[norm(id)] {
			return fmt.Errorf("repeated block %s", id)
		}
		visited[norm(id)] = true
		cursor := ""
		cursors := map[string]bool{}
		for {
			endpoint := "v1/blocks/" + id + "/children?page_size=100"
			if cursor != "" {
				endpoint += "&start_cursor=" + url.QueryEscape(cursor)
			}
			var response struct {
				Object     string              `json:"object"`
				Results    []notionAnchorBlock `json:"results"`
				HasMore    bool                `json:"has_more"`
				NextCursor *string             `json:"next_cursor"`
			}
			if err := api.API(ctx, "GET", endpoint, nil, &response); err != nil {
				return err
			}
			if response.Object != "list" {
				return fmt.Errorf("unexpected block list response")
			}
			for _, block := range response.Results {
				if block.Object != "block" || !config.ValidID(block.ID) || block.Type == "" {
					return fmt.Errorf("unexpected block response")
				}
				blocks = append(blocks, block)
				if len(blocks) > 20000 {
					return fmt.Errorf("page has too many blocks to resolve anchors safely")
				}
				if block.HasChildren && block.Type != "child_page" && block.Type != "child_database" {
					if err := visit(block.ID); err != nil {
						return err
					}
				}
			}
			if !response.HasMore {
				return nil
			}
			if response.NextCursor == nil || *response.NextCursor == "" || cursors[*response.NextCursor] {
				return fmt.Errorf("invalid block pagination cursor")
			}
			cursor = *response.NextCursor
			cursors[cursor] = true
		}
	}
	if err := visit(pageID); err != nil {
		return nil, err
	}
	return blocks, nil
}

func blockHeadings(blocks []notionAnchorBlock) (map[string]string, error) {
	result := map[string]string{}
	var slugger headingSlugger
	for _, b := range blocks {
		if b.Type != "heading_1" && b.Type != "heading_2" && b.Type != "heading_3" && b.Type != "heading_4" {
			continue
		}
		var heading struct {
			RichText []struct {
				PlainText string `json:"plain_text"`
			} `json:"rich_text"`
		}
		if err := json.Unmarshal(b.Data[b.Type], &heading); err != nil {
			return nil, err
		}
		var title strings.Builder
		for _, item := range heading.RichText {
			title.WriteString(item.PlainText)
		}
		result[slugger.next(title.String())] = b.ID
	}
	return result, nil
}

func headingSlugs(headings map[string]string) string {
	slugs := make([]string, 0, len(headings))
	for slug := range headings {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	if len(slugs) == 0 {
		return "none"
	}
	return strings.Join(slugs, ", ")
}

type anchorPatch struct {
	page, path, block, kind string
	richText                []any
	urls                    map[string]string
	refs                    map[string]AnchorRef
	tokens                  []string
	targets                 []string
}

func sameAnchorMarker(actual, desired string) bool {
	a, err := url.Parse(actual)
	if err != nil {
		return false
	}
	b, err := url.Parse(desired)
	if err != nil {
		return false
	}
	switch a.Hostname() {
	case "notion.so", "www.notion.so", "notion.com", "www.notion.com", "app.notion.com":
	default:
		return false
	}
	aPage, bPage := diffPageID.FindString(a.Path), diffPageID.FindString(b.Path)
	marker := a.Query().Get(anchorQuery)
	return a.Scheme == "https" && a.User == nil && config.ValidID(aPage) && config.ValidID(bPage) && norm(aPage) == norm(bPage) && len(a.Query()[anchorQuery]) <= 1 && (marker == "" || marker == b.Query().Get(anchorQuery))
}

func sameAnchorURL(actual, desired string) bool {
	if !sameAnchorMarker(actual, desired) {
		return false
	}
	a, _ := url.Parse(actual)
	b, _ := url.Parse(desired)
	aBlock, bBlock := strings.TrimPrefix(a.Fragment, "block-"), strings.TrimPrefix(b.Fragment, "block-")
	return config.ValidID(aBlock) && config.ValidID(bBlock) && norm(aBlock) == norm(bBlock)
}

func remoteReferencesAnchor(body, pageID string) bool {
	for _, value := range diffNotionURL.FindAllString(body, -1) {
		u, err := url.Parse(value)
		if err != nil {
			continue
		}
		block := strings.TrimPrefix(u.Fragment, "block-")
		if u.Query().Get(anchorQuery) == "" && !config.ValidID(block) {
			continue
		}
		id := diffPageID.FindString(u.Path)
		if config.ValidID(id) && norm(id) == norm(pageID) {
			return true
		}
	}
	return false
}

func normalizeMatchedAnchors(body string, urls map[string]string, refs map[string]AnchorRef, permitted map[string]bool, requireResolved bool) string {
	var out strings.Builder
	last := 0
	protected := protectedMarkdown(body)
	for _, m := range inlineMarkdownLink.FindAllStringSubmatchIndex(body, -1) {
		start, ds, de := m[0], m[2], m[3]
		if protected[start] || escapedAt(body, start) || start > 0 && body[start-1] == '!' {
			continue
		}
		labelEnd := strings.Index(body[start:ds], "](")
		if labelEnd < 1 {
			continue
		}
		label := anchorLabel(body[start+1 : start+labelEnd])
		actual := strings.Trim(body[ds:de], "<>")
		for token, ref := range refs {
			if !permitted[token] || ref.Label != label || !sameAnchorMarker(actual, urls[token]) || requireResolved && !sameAnchorURL(actual, urls[token]) {
				continue
			}
			u, err := url.Parse(urls[token])
			if err != nil {
				continue
			}
			id := diffPageID.FindString(u.Path)
			if !config.ValidID(id) {
				continue
			}
			out.WriteString(body[last:ds])
			out.WriteString(pageURL(id))
			last = de
			break
		}
	}
	out.WriteString(body[last:])
	return out.String()
}

// anchorBlockPatch returns an in-place update only when every rich text item can
// be round-tripped without dropping Notion formatting or unsupported types.
func anchorBlockPatch(block notionAnchorBlock, urls map[string]string, found, used, reserved map[string]bool, references ...map[string]AnchorRef) ([]any, bool, error) {
	data := block.Data[block.Type]
	if len(data) == 0 {
		return nil, false, nil
	}
	var content map[string]json.RawMessage
	if err := json.Unmarshal(data, &content); err != nil {
		return nil, false, err
	}
	var items []struct {
		Type string `json:"type"`
		Text *struct {
			Content string `json:"content"`
			Link    *struct {
				URL string `json:"url"`
			} `json:"link"`
		} `json:"text"`
		Annotations json.RawMessage `json:"annotations"`
	}
	if raw, ok := content["rich_text"]; ok {
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, false, err
		}
	}
	var refs map[string]AnchorRef
	if len(references) > 0 {
		refs = references[0]
	}
	matched := map[int]string{}
	changed := false
	for index := 0; index < len(items); {
		item := items[index]
		if item.Text == nil || item.Text.Link == nil {
			index++
			continue
		}
		end := index + 1
		var label strings.Builder
		label.WriteString(item.Text.Content)
		for end < len(items) && items[end].Text != nil && items[end].Text.Link != nil && items[end].Text.Link.URL == item.Text.Link.URL {
			label.WriteString(items[end].Text.Content)
			end++
		}
		u, err := url.Parse(item.Text.Link.URL)
		if err != nil {
			index = end
			continue
		}
		token := u.Query().Get(anchorQuery)
		var tokens []string
		labelText := label.String()
		matchedLabel := ""
		count := 1
		fragment := ""
		if token != "" {
			if desired, ok := urls[token]; !ok || !sameAnchorMarker(item.Text.Link.URL, desired) {
				index = end
				continue
			}
			if ref, ok := refs[token]; ok {
				if ref.Label == "" || labelText == "" || len(labelText)%len(ref.Label) != 0 || strings.Repeat(ref.Label, len(labelText)/len(ref.Label)) != labelText {
					index = end
					continue
				}
				matchedLabel = ref.Label
				count = len(labelText) / len(ref.Label)
				desired, _ := url.Parse(urls[token])
				fragment = desired.Fragment
			}
			if found[token] || used[token] {
				return nil, false, fmt.Errorf("ambiguous repeated anchor link %q", labelText)
			}
			tokens = append(tokens, token)
		}
		if token == "" || count > 1 {
			var candidates []string
			withoutMarker := *u
			withoutMarker.RawQuery = ""
			for candidate, ref := range refs {
				if ref.Label != "" && labelText != "" && len(labelText)%len(ref.Label) == 0 && strings.Repeat(ref.Label, len(labelText)/len(ref.Label)) == labelText && sameAnchorMarker(withoutMarker.String(), urls[candidate]) {
					desired, err := url.Parse(urls[candidate])
					if err != nil || fragment != "" && fragment != desired.Fragment || matchedLabel != "" && matchedLabel != ref.Label {
						return nil, false, fmt.Errorf("ambiguous anchor link %q", labelText)
					}
					fragment = desired.Fragment
					matchedLabel = ref.Label
					count = len(labelText) / len(ref.Label)
					if candidate != token && !found[candidate] && !used[candidate] && !reserved[candidate] {
						candidates = append(candidates, candidate)
					}
				}
			}
			if matchedLabel != "" {
				sort.Strings(candidates)
				needed := count - len(tokens)
				if len(candidates) < needed {
					return nil, false, fmt.Errorf("ambiguous repeated anchor link %q", labelText)
				}
				tokens = append(tokens, candidates[:needed]...)
			}
		}
		if len(tokens) == 0 {
			index = end
			continue
		}
		for _, matchedToken := range tokens {
			found[matchedToken] = true
		}
		token = tokens[0]
		for i := index; i < end; i++ {
			matched[i] = token
		}
		if !sameAnchorURL(item.Text.Link.URL, urls[token]) {
			changed = true
		}
		index = end
	}
	if !changed {
		// An anchor marker in a non-rich-text block cannot be updated safely.
		if len(items) == 0 && strings.Contains(string(data), anchorQuery) {
			return nil, false, fmt.Errorf("unsupported anchor in %s block", block.Type)
		}
		return nil, false, nil
	}
	result := make([]any, 0, len(items))
	for index, item := range items {
		if item.Type != "text" || item.Text == nil {
			return nil, false, fmt.Errorf("cannot safely update %s block containing non-text rich text", block.Type)
		}
		value := map[string]any{"content": item.Text.Content}
		if item.Text.Link != nil {
			link := item.Text.Link.URL
			if token, ok := matched[index]; ok {
				link = urls[token]
			}
			value["link"] = map[string]any{"url": link}
		}
		entry := map[string]any{"type": "text", "text": value}
		if len(item.Annotations) != 0 && string(item.Annotations) != "null" {
			entry["annotations"] = item.Annotations
		}
		result = append(result, entry)
	}
	return result, true, nil
}

func anchorBlockMarkers(blocks []notionAnchorBlock) (map[string]bool, error) {
	markers := map[string]bool{}
	for _, block := range blocks {
		data := block.Data[block.Type]
		if len(data) == 0 {
			continue
		}
		var content struct {
			RichText []struct {
				Text *struct {
					Link *struct {
						URL string `json:"url"`
					} `json:"link"`
				} `json:"text"`
			} `json:"rich_text"`
		}
		if err := json.Unmarshal(data, &content); err != nil {
			return nil, err
		}
		for _, item := range content.RichText {
			if item.Text == nil || item.Text.Link == nil {
				continue
			}
			link, err := url.Parse(item.Text.Link.URL)
			if err == nil && link.Query().Get(anchorQuery) != "" {
				markers[link.Query().Get(anchorQuery)] = true
			}
		}
	}
	return markers, nil
}

func syncAnchors(ctx context.Context, c config.Config, api Client, s *State, files []File, print func(string, ...any)) error {
	targets := map[string]bool{}
	for _, f := range files {
		for _, a := range f.Anchors {
			if a.Token != "" {
				targets[a.Target] = true
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}
	sort.Strings(names)
	headings := map[string]map[string]string{}
	for _, name := range names {
		e, err := validateAnchorTarget(ctx, c, api, s, name)
		if err != nil {
			return err
		}
		blocks, err := listAnchorBlocks(ctx, api, e.PageID)
		if err != nil {
			return fmt.Errorf("%s: cannot read heading blocks: %w", name, err)
		}
		headings[name], err = blockHeadings(blocks)
		if err != nil {
			return fmt.Errorf("%s: cannot read headings: %w", name, err)
		}
	}
	var patches []anchorPatch
	for _, f := range files {
		if len(f.Anchors) == 0 {
			continue
		}
		e, ok := s.Entries["file:"+f.Path]
		if !ok {
			return fmt.Errorf("%s: anchor source is not synchronized", f.Path)
		}
		if _, err := validateExisting(ctx, api, e); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		urls := map[string]string{}
		refs := map[string]AnchorRef{}
		labels := map[string]string{}
		tokenTargets := map[string]string{}
		for _, a := range f.Anchors {
			if a.Token == "" {
				continue
			}
			blockID := headings[a.Target][a.Fragment]
			if !config.ValidID(blockID) {
				return fmt.Errorf("%s: Notion heading %q not found in %s (available anchors: %s)", f.Path, a.Fragment, a.Target, headingSlugs(headings[a.Target]))
			}
			target := s.Entries["file:"+a.Target]
			key := norm(target.PageID) + "\x00" + a.Label
			if a.Label == "" || labels[key] != "" && labels[key] != norm(blockID) {
				return fmt.Errorf("%s: ambiguous anchor label %q for %s; use distinct link labels", f.Path, a.Label, a.Target)
			}
			labels[key] = norm(blockID)
			urls[a.Token] = anchorPlaceholder(target.PageID, a.Token) + "#" + norm(blockID)
			refs[a.Token] = a
			tokenTargets[a.Token] = a.Target
		}
		if len(urls) == 0 {
			continue
		}
		blocks, err := listAnchorBlocks(ctx, api, e.PageID)
		if err != nil {
			return fmt.Errorf("%s: cannot read anchor blocks: %w", f.Path, err)
		}
		reserved, err := anchorBlockMarkers(blocks)
		if err != nil {
			return fmt.Errorf("%s: cannot read anchor markers: %w", f.Path, err)
		}
		found := map[string]bool{}
		for _, block := range blocks {
			inBlock := map[string]bool{}
			richText, changed, err := anchorBlockPatch(block, urls, inBlock, found, reserved, refs)
			if err != nil {
				return fmt.Errorf("%s: %w", f.Path, err)
			}
			var tokens []string
			targetSet := map[string]bool{}
			for token := range inBlock {
				if found[token] {
					return fmt.Errorf("%s: ambiguous repeated Notion anchor link %q", f.Path, refs[token].Label)
				}
				found[token] = true
				tokens = append(tokens, token)
				targetSet[tokenTargets[token]] = true
			}
			if changed {
				var patchTargets []string
				for target := range targetSet {
					patchTargets = append(patchTargets, target)
				}
				sort.Strings(patchTargets)
				patches = append(patches, anchorPatch{page: e.PageID, path: f.Path, block: block.ID, kind: block.Type, richText: richText, urls: urls, refs: refs, tokens: tokens, targets: patchTargets})
			}
		}
		for token := range urls {
			if !found[token] {
				ref := refs[token]
				return fmt.Errorf("%s: anchor link %q to %s could not be uniquely matched in Notion; inspect before retry", f.Path, ref.Label, ref.Target)
			}
		}
	}
	verifyHeadings := func(targetNames []string) error {
		for _, name := range targetNames {
			target := s.Entries["file:"+name]
			if _, err := validateExisting(ctx, api, target); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			blocks, err := listAnchorBlocks(ctx, api, target.PageID)
			if err != nil {
				return fmt.Errorf("%s: cannot recheck headings: %w", name, err)
			}
			current, err := blockHeadings(blocks)
			if err != nil || !reflect.DeepEqual(current, headings[name]) {
				return fmt.Errorf("%s: heading blocks changed during anchor update; retry sync", name)
			}
		}
		return nil
	}
	for _, patch := range patches {
		if err := verifyHeadings(patch.targets); err != nil {
			return err
		}
		e := s.Entries["file:"+patch.path]
		before, err := validateExisting(ctx, api, e)
		if err != nil {
			return fmt.Errorf("%s: %w", patch.path, err)
		}
		if err := verifyFileUnchanged(c, s.Documents[patch.path]); err != nil {
			return err
		}
		var updated struct {
			Object string `json:"object"`
			ID     string `json:"id"`
			Type   string `json:"type"`
		}
		body := map[string]any{"type": patch.kind, patch.kind: map[string]any{"rich_text": patch.richText}}
		if err := api.API(ctx, "PATCH", "v1/blocks/"+patch.block, body, &updated); err != nil {
			return fmt.Errorf("%s: anchor update outcome uncertain; inspect Notion before retry: %w", patch.path, err)
		}
		if updated.Object != "block" || norm(updated.ID) != norm(patch.block) || updated.Type != patch.kind {
			return fmt.Errorf("%s: unexpected anchor update response; inspect Notion", patch.path)
		}
		m, err := getMarkdown(ctx, api, patch.page)
		if err != nil {
			return fmt.Errorf("%s: anchor updated but readback failed; inspect Notion: %w", patch.path, err)
		}
		blocks, err := listAnchorBlocks(ctx, api, patch.page)
		if err != nil {
			return fmt.Errorf("%s: anchor updated but block verification failed; inspect Notion: %w", patch.path, err)
		}
		verified := map[string]bool{}
		verifiedURLs := map[string]string{}
		verifiedRefs := map[string]AnchorRef{}
		for _, token := range patch.tokens {
			verifiedURLs[token] = patch.urls[token]
			verifiedRefs[token] = patch.refs[token]
		}
		blockFound := false
		for _, block := range blocks {
			if norm(block.ID) != norm(patch.block) {
				continue
			}
			blockFound = true
			reserved, err := anchorBlockMarkers([]notionAnchorBlock{block})
			if err != nil {
				return fmt.Errorf("%s: anchor readback did not match; inspect Notion", patch.path)
			}
			_, changed, err := anchorBlockPatch(block, verifiedURLs, verified, nil, reserved, verifiedRefs)
			if err != nil || changed {
				return fmt.Errorf("%s: anchor readback did not match; inspect Notion", patch.path)
			}
		}
		if !blockFound {
			return fmt.Errorf("%s: anchor block disappeared; inspect Notion", patch.path)
		}
		for _, token := range patch.tokens {
			if !verified[token] {
				return fmt.Errorf("%s: anchor link could not be verified; inspect Notion", patch.path)
			}
		}
		allowed := map[string]bool{}
		for _, token := range patch.tokens {
			allowed[token] = true
		}
		previous := normalizeMatchedAnchors(canonicalRemoteMarkdown(before.Markdown), patch.urls, patch.refs, allowed, false)
		current := normalizeMatchedAnchors(canonicalRemoteMarkdown(m.Markdown), patch.urls, patch.refs, allowed, false)
		if previous != current {
			return fmt.Errorf("%s: other content changed during anchor update; inspect Notion before retry", patch.path)
		}
		e.RemoteHash = remoteMarkdownHash(m.Markdown)
		s.Entries["file:"+patch.path] = e
		if err := saveState(c, *s); err != nil {
			return err
		}
		print("updated anchor links %s -> %s", patch.path, pageURL(patch.page))
	}
	if len(patches) > 0 {
		if err := verifyHeadings(names); err != nil {
			return err
		}
	}
	return nil
}

func validateAnchorTarget(ctx context.Context, c config.Config, api Client, s *State, name string) (Entry, error) {
	e, ok := s.Entries["file:"+name]
	if !ok {
		return Entry{}, fmt.Errorf("%s: anchor target is not synchronized", name)
	}
	local := s.Documents[name]
	if local == nil {
		return Entry{}, fmt.Errorf("%s: anchor target is not available", name)
	}
	if err := verifyFileUnchanged(c, local); err != nil {
		return Entry{}, err
	}
	prepared := []File{*local}
	prepareLinks(prepared, *s, c.SourcePath)
	if prepared[0].Hash != e.LocalHash {
		return Entry{}, fmt.Errorf("%s: anchor target has unsynchronized local changes", name)
	}
	if _, err := validateExisting(ctx, api, e); err != nil {
		return Entry{}, fmt.Errorf("%s: %w", name, err)
	}
	return e, nil
}

// normalizePullAnchors treats an unchanged resolved heading link as its local
// placeholder, while leaving links to other blocks visible as changes.
func normalizePullAnchors(ctx context.Context, c config.Config, api Client, s *State, f File, remote string) (File, string, error) {
	urls := map[string]string{}
	refs := map[string]AnchorRef{}
	allowed := map[string]bool{}
	labels := map[string]string{}
	cache := map[string]map[string]string{}
	for _, ref := range f.Anchors {
		if ref.Token == "" {
			continue
		}
		e, ok := s.Entries["file:"+ref.Target]
		if !ok {
			return f, remote, fmt.Errorf("%s: anchor target is not synchronized", ref.Target)
		}
		key := norm(e.PageID) + "\x00" + ref.Label
		if _, ok := cache[ref.Target]; !ok {
			if ref.Target != f.Path {
				if _, err := validateAnchorTarget(ctx, c, api, s, ref.Target); err != nil {
					return f, remote, err
				}
			} else if err := verifyFileUnchanged(c, s.Documents[ref.Target]); err != nil {
				return f, remote, err
			}
			blocks, err := listAnchorBlocks(ctx, api, e.PageID)
			if err != nil {
				return f, remote, fmt.Errorf("%s: cannot read heading blocks: %w", ref.Target, err)
			}
			cache[ref.Target], err = blockHeadings(blocks)
			if err != nil {
				return f, remote, fmt.Errorf("%s: cannot read headings: %w", ref.Target, err)
			}
		}
		blockID := cache[ref.Target][ref.Fragment]
		if !config.ValidID(blockID) {
			return f, remote, fmt.Errorf("%s: Notion heading %q not found in %s (available anchors: %s)", f.Path, ref.Fragment, ref.Target, headingSlugs(cache[ref.Target]))
		}
		if ref.Label == "" || labels[key] != "" && labels[key] != norm(blockID) {
			return f, remote, fmt.Errorf("%s: ambiguous anchor label %q for %s", f.Path, ref.Label, ref.Target)
		}
		labels[key] = norm(blockID)
		urls[ref.Token] = anchorPlaceholder(e.PageID, ref.Token) + "#" + norm(blockID)
		refs[ref.Token], allowed[ref.Token] = ref, true
	}
	if len(urls) == 0 {
		return f, remote, nil
	}
	f.SyncBody = normalizeMatchedAnchors(f.SyncBody, urls, refs, allowed, false)
	remote = normalizeMatchedAnchors(remote, urls, refs, allowed, true)
	return f, remote, nil
}

// finishAnchorSync re-evaluates links after newly adopted pages have acquired
// synchronization metadata, then resolves all heading links in place.
func finishAnchorSync(ctx context.Context, c config.Config, api Client, s *State, files []File, print func(string, ...any), overwrite ...bool) error {
	refresh := func() {
		for i := range files {
			files[i] = *s.Documents[files[i].Path]
		}
		prepareLinks(files, *s, c.SourcePath)
	}
	refresh()
	needsUpdate := false
	for _, f := range files {
		if e, ok := s.Entries["file:"+f.Path]; ok && f.Hash != e.LocalHash {
			needsUpdate = true
		}
	}
	if needsUpdate {
		plan, err := Build(ctx, c, api, files, *s, overwrite...)
		if err != nil {
			return err
		}
		if err := Apply(ctx, c, api, s, plan, print); err != nil {
			return err
		}
		refresh()
		for _, f := range files {
			if e, ok := s.Entries["file:"+f.Path]; ok && f.Hash != e.LocalHash {
				return fmt.Errorf("%s: link dependencies changed during sync; retry", f.Path)
			}
		}
	}
	return syncAnchors(ctx, c, api, s, files, print)
}
