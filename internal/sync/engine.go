package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/yuyj109/notion-sync/internal/config"
)

type Client interface {
	API(context.Context, string, string, any, any) error
}
type MultipartClient interface {
	Upload(context.Context, string, string, string) (string, error)
}
type page struct {
	Object  string `json:"object"`
	ID      string `json:"id"`
	InTrash bool   `json:"in_trash"`
	Parent  struct {
		Type         string `json:"type"`
		PageID       string `json:"page_id"`
		DataSourceID string `json:"data_source_id"`
	} `json:"parent"`
}
type markdown struct {
	Object    string        `json:"object"`
	ID        string        `json:"id"`
	Markdown  string        `json:"markdown"`
	Truncated *bool         `json:"truncated"`
	Unknown   []string      `json:"unknown_block_ids"`
	Warnings  []jsonWarning `json:"warnings"`
}
type jsonWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func markdownUpdateIssue(m markdown, pageID string) string {
	var issues []string
	if m.Object != "page_markdown" || norm(m.ID) != norm(pageID) {
		issues = append(issues, "unexpected response object or page ID")
	}
	if m.Truncated == nil {
		issues = append(issues, "response omitted truncation status")
	} else if *m.Truncated {
		issues = append(issues, "response content was truncated")
	}
	if len(m.Unknown) > 0 {
		issues = append(issues, fmt.Sprintf("response contains %d unknown blocks", len(m.Unknown)))
	} else if strings.Contains(m.Markdown, "<unknown") {
		issues = append(issues, "response contains unknown content")
	}
	for _, warning := range m.Warnings {
		message := []rune(strings.TrimSpace(warning.Message))
		if len(message) > 500 {
			message = append(message[:500], []rune("...")...)
		}
		detail := strings.TrimSpace(warning.Code)
		if len(message) > 0 {
			if detail != "" {
				detail += ": "
			}
			detail += string(message)
		}
		if detail == "" {
			detail = "unspecified warning"
		}
		issues = append(issues, detail)
	}
	return strings.Join(issues, "; ")
}

type Action struct {
	Kind, Key, Path, Title, Target, ParentID, ParentType, DataSourceID, TitleProperty string
	File                                                                              File
	Entry                                                                             Entry
	Body                                                                              string
	TextParts                                                                         []string
	Overwrite                                                                         bool
	ConflictDiff                                                                      string
	ConflictDiffNumbered                                                              string
}
type Plan struct {
	Actions []Action
	Deleted []string
}

func norm(s string) string { return config.NormalizeID(s) }
func getPage(ctx context.Context, api Client, id string) (page, error) {
	var p page
	err := api.API(ctx, "GET", "v1/pages/"+id, nil, &p)
	if err != nil {
		return p, err
	}
	if p.Object != "page" || norm(p.ID) != norm(id) || p.InTrash {
		return p, fmt.Errorf("page %s has an unexpected response or is in trash", id)
	}
	return p, nil
}
func parentOf(p page) (string, string) {
	switch p.Parent.Type {
	case "page_id":
		return p.Parent.PageID, p.Parent.Type
	case "data_source_id":
		return p.Parent.DataSourceID, p.Parent.Type
	}
	return "", p.Parent.Type
}
func getMarkdown(ctx context.Context, api Client, id string) (markdown, error) {
	var m markdown
	err := api.API(ctx, "GET", "v1/pages/"+id+"/markdown", nil, &m)
	if err != nil {
		return m, err
	}
	if m.Object != "page_markdown" || norm(m.ID) != norm(id) || m.Truncated == nil || *m.Truncated || len(m.Unknown) > 0 || strings.Contains(m.Markdown, "<unknown") {
		return m, fmt.Errorf("page %s returned incomplete or unexpected Markdown", id)
	}
	return m, nil
}
func ResolveDataSource(ctx context.Context, api Client, id string) (string, string, error) {
	var db struct {
		Object      string `json:"object"`
		ID          string `json:"id"`
		DataSources []struct {
			ID string `json:"id"`
		} `json:"data_sources"`
	}
	if err := api.API(ctx, "GET", "v1/databases/"+id, nil, &db); err != nil {
		return "", "", err
	}
	if db.Object != "database" || norm(db.ID) != norm(id) || len(db.DataSources) != 1 || !config.ValidID(db.DataSources[0].ID) {
		return "", "", fmt.Errorf("database %s must contain exactly one accessible data source", id)
	}
	ds := db.DataSources[0].ID
	var schema struct {
		Object     string `json:"object"`
		ID         string `json:"id"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := api.API(ctx, "GET", "v1/data_sources/"+ds, nil, &schema); err != nil {
		return "", "", err
	}
	if schema.Object != "data_source" || norm(schema.ID) != norm(ds) {
		return "", "", fmt.Errorf("unexpected data source response")
	}
	title := ""
	for name, p := range schema.Properties {
		if p.Type == "title" {
			if title != "" {
				return "", "", fmt.Errorf("multiple title properties")
			}
			title = name
		}
	}
	if title == "" {
		return "", "", fmt.Errorf("data source has no title property")
	}
	return ds, title, nil
}

type remoteChangedError struct{ pageID, diff, numberedDiff string }

func (e *remoteChangedError) DiffViews() (string, string) { return e.diff, e.numberedDiff }

func (e *remoteChangedError) Error() string {
	msg := fmt.Sprintf("page %s has changed in Notion; review it and use --push to replace it with local content", e.pageID)
	if e.diff != "" {
		msg += "\n" + e.diff
	}
	return msg
}

func validateExisting(ctx context.Context, api Client, e Entry) (markdown, error) {
	p, err := getPage(ctx, api, e.PageID)
	if err != nil {
		return markdown{}, err
	}
	id, kind := parentOf(p)
	if kind != e.ParentType || norm(id) != norm(e.ParentID) {
		return markdown{}, fmt.Errorf("page %s was moved; resolve manually", e.PageID)
	}
	if e.RemoteHash == "" {
		return markdown{}, nil
	}
	m, err := getMarkdown(ctx, api, e.PageID)
	if err != nil {
		return m, err
	}
	if remoteMarkdownHash(m.Markdown) != e.RemoteHash {
		if len(e.managedEmbeds) == 0 {
			return m, &remoteChangedError{pageID: e.PageID}
		}
		normalized, _, err := normalizeManagedEmbeds(ctx, api, e, m.Markdown)
		if err != nil {
			return m, err
		}
		// Notion may append a trailing empty placeholder when an embed is edited.
		// Accept it only if removing it restores the exact stored baseline.
		withoutEmpty := strings.TrimSuffix(normalized, "\n<empty-block/>")
		if remoteMarkdownHash(normalized) != e.RemoteHash && remoteMarkdownHash(withoutEmpty) != e.RemoteHash {
			return m, &remoteChangedError{pageID: e.PageID}
		}
	}
	return m, nil
}
func Build(ctx context.Context, c config.Config, api Client, files []File, s State, overwrite ...bool) (Plan, error) {
	plan := Plan{}
	if c.Notion.ParentDatabaseID != "" {
		if _, _, err := ResolveDataSource(ctx, api, c.Notion.ParentDatabaseID); err != nil {
			return plan, fmt.Errorf("configured database: %w", err)
		}
		for i := range files {
			f := &files[i]
			if f.ParentID == "" && f.DatabaseID == "" && (f.PageID == "" || f.Metadata != nil) {
				f.DatabaseID = c.Notion.ParentDatabaseID
			}
		}
	} else if _, err := getPage(ctx, api, c.Notion.ParentPageID); err != nil {
		return plan, fmt.Errorf("configured parent: %w", err)
	}
	seen := map[string]string{}
	for key, e := range s.Entries {
		if !config.ValidID(e.PageID) || !config.ValidID(e.ParentID) {
			return plan, fmt.Errorf("invalid ID in state for %s", key)
		}
		id := norm(e.PageID)
		if prior, ok := seen[id]; ok {
			return plan, fmt.Errorf("same Notion page is assigned to %s and %s", prior, key)
		}
		seen[id] = key
	}
	needed := map[string]bool{}
	for _, f := range files {
		e, exists := s.Entries["file:"+f.Path]
		useHierarchy := f.ParentID == "" && f.DatabaseID == "" && (f.PageID == "" || exists && strings.HasPrefix(e.Target, "hierarchy:"))
		if useHierarchy && c.Sync.PreserveHierarchy {
			d := path.Dir(f.Path)
			for d != "." && d != "/" {
				needed[d] = true
				d = path.Dir(d)
			}
		}
	}
	dirs := make([]string, 0, len(needed))
	for d := range needed {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		di, dj := strings.Count(dirs[i], "/"), strings.Count(dirs[j], "/")
		if di == dj {
			return dirs[i] < dirs[j]
		}
		return di < dj
	})
	for _, d := range dirs {
		key := "dir:" + d
		parentDir := path.Dir(d)
		parentID := c.Notion.ParentPageID
		if parentDir != "." {
			parentID = s.Entries["dir:"+parentDir].PageID
		}
		target := "dir:" + parentDir
		e, exists := s.Entries[key]
		a := Action{Key: key, Path: d, Title: path.Base(d), Target: target, ParentID: parentID, ParentType: "page_id", Entry: e}
		switch {
		case exists:
			if e.Target != target || parentID == "" || norm(e.ParentID) != norm(parentID) {
				return plan, fmt.Errorf("directory destination changed for %s", d)
			}
			if _, err := validateExisting(ctx, api, e); err != nil {
				return plan, fmt.Errorf("%s: %w", d, err)
			}
			a.Kind = "directory-existing"
		default:
			for _, f := range files {
				if strings.HasPrefix(f.Path, d+"/") {
					if existing, ok := s.Entries["file:"+f.Path]; ok && strings.HasPrefix(existing.Target, "hierarchy:") {
						return plan, fmt.Errorf("directory metadata for %s is missing; restore it before syncing", d)
					}
				}
			}
			a.Kind = "directory-create"
		}
		plan.Actions = append(plan.Actions, a)
	}
	live := map[string]bool{}
	for _, f := range files {
		for i := range f.Images {
			if c.Sync.DrawioEmbed && isDrawioSVG(f.Images[i]) {
				f.Images[i].StateHash = hash("drawio-embed:" + f.Images[i].Hash)
			}
		}
		key := "file:" + f.Path
		live[key] = true
		title := pageTitle(f.SyncBody, f.Path)
		a := Action{Key: key, Path: f.Path, Title: title, File: f}
		var splitErr error
		a.Body, _, splitErr = bodyWithoutImages(f.SyncBody)
		if splitErr != nil {
			return plan, splitErr
		}
		a.TextParts, splitErr = imageTextParts(f.SyncBody)
		if splitErr != nil {
			return plan, splitErr
		}
		if len(f.Images) > 0 {
			if len(a.TextParts) != len(f.Images)+1 {
				return plan, fmt.Errorf("%s: local image segments do not match image references", f.Path)
			}
			a.Body = a.TextParts[0]
		}
		dir := path.Dir(f.Path)
		e, exists := s.Entries[key]
		switch {
		case f.PageID != "" && (!exists || strings.HasPrefix(e.Target, "page:")):
			a.Target = "page:" + norm(f.PageID)
			if f.ParentID != "" {
				a.ParentID, a.ParentType = f.ParentID, "page_id"
			}
			if f.DatabaseID != "" {
				ds, title, err := ResolveDataSource(ctx, api, f.DatabaseID)
				if err != nil {
					return plan, err
				}
				a.ParentID, a.ParentType, a.TitleProperty = ds, "data_source_id", title
			}
		case f.ParentID != "":
			a.Target = "parent:" + norm(f.ParentID)
			a.ParentID = f.ParentID
			a.ParentType = "page_id"
		case f.DatabaseID != "":
			a.Target = "database:" + norm(f.DatabaseID)
			ds, t, err := ResolveDataSource(ctx, api, f.DatabaseID)
			if err != nil {
				return plan, fmt.Errorf("%s: %w", f.Path, err)
			}
			a.ParentID = ds
			a.ParentType = "data_source_id"
			a.TitleProperty = t
		case f.PageID != "" && exists && strings.HasPrefix(e.Target, "parent:"):
			a.Target = e.Target
			a.ParentID = strings.TrimPrefix(e.Target, "parent:")
			a.ParentType = "page_id"
		case f.PageID != "" && exists && strings.HasPrefix(e.Target, "database:"):
			a.Target = e.Target
			ds, title, err := ResolveDataSource(ctx, api, strings.TrimPrefix(e.Target, "database:"))
			if err != nil {
				return plan, fmt.Errorf("%s: %w", f.Path, err)
			}
			a.ParentID, a.ParentType, a.TitleProperty = ds, "data_source_id", title
		default:
			a.Target = "hierarchy:"
			a.ParentID = c.Notion.ParentPageID
			a.ParentType = "page_id"
			if c.Sync.PreserveHierarchy && dir != "." {
				a.Target += dir
				a.ParentID = s.Entries["dir:"+dir].PageID
			}
		}
		if f.PageID != "" {
			id := norm(f.PageID)
			if other, ok := seen[id]; ok && other != key {
				return plan, fmt.Errorf("%s and %s target the same Notion page", other, key)
			}
			seen[id] = key
		}
		switch {
		case exists:
			if c.Sync.DrawioEmbed {
				if err := identifyManagedDrawioEmbeds(ctx, api, &e, f.Images); err != nil {
					return plan, fmt.Errorf("%s: %w", f.Path, err)
				}
				s.Entries[key] = e
			}
			a.Entry = e
			if e.RemoteHash == "" || e.LocalHash == "" {
				return plan, fmt.Errorf("%s: incomplete synchronization baseline", f.Path)
			}
			if e.Target != a.Target || a.ParentID != "" && norm(e.ParentID) != norm(a.ParentID) || f.PageID != "" && norm(e.PageID) != norm(f.PageID) {
				return plan, fmt.Errorf("%s: destination changed; resolve manually", f.Path)
			}
			m, err := validateExisting(ctx, api, e)
			conflict := false
			if err != nil {
				var changed *remoteChangedError
				if !errors.As(err, &changed) {
					return plan, fmt.Errorf("%s: %w", f.Path, err)
				}
				remoteForDiff := m.Markdown
				if len(e.managedEmbeds) > 0 {
					remoteForDiff, err = drawioDiffMarkdown(ctx, api, e, f, m.Markdown)
					if err != nil {
						return plan, fmt.Errorf("%s: %w", f.Path, err)
					}
				}
				diff := makeContentDiff(f.Path, remoteForDiff, f.SyncBody, 0, sourceBodyOffset(f))
				changed.diff, changed.numberedDiff = diff.Plain, diff.Numbered
				if len(overwrite) == 0 || !overwrite[0] {
					return plan, fmt.Errorf("%s: %w", f.Path, changed)
				}
				conflict = true
				a.Overwrite = true
				a.ConflictDiff = changed.diff
				a.ConflictDiffNumbered = changed.numberedDiff
				e.RemoteHash = remoteMarkdownHash(m.Markdown)
				a.Entry = e
			}
			switch {
			case conflict:
				a.Kind = "update"
			case f.Hash == e.LocalHash:
				changed := changedUnmanagedImages(f.Images, e)
				if hasDrawioSVG(changed) {
					a.Kind = "update"
				} else if len(changed) > 0 {
					a.Kind = "images-update"
				} else {
					a.Kind = "unchanged"
				}
				format := c.Sync.MetadataFormat
				if format == "" {
					format = "frontmatter"
				}
				if f.RootNeedsFormatting || f.Metadata != nil && f.MetadataFormat != format {
					if a.Kind == "unchanged" {
						a.Kind = "normalize"
					}
				}
			default:
				a.Kind = "update"
			}
		case f.PageID != "":
			p, err := getPage(ctx, api, f.PageID)
			if err != nil {
				return plan, fmt.Errorf("%s: %w", f.Path, err)
			}
			pid, ptype := parentOf(p)
			if !config.ValidID(pid) {
				return plan, fmt.Errorf("%s: unsupported page parent", f.Path)
			}
			if f.ParentID != "" && (ptype != "page_id" || norm(pid) != norm(f.ParentID)) {
				return plan, fmt.Errorf("%s: page is not under the specified parent", f.Path)
			}
			if f.DatabaseID != "" {
				ds, _, err := ResolveDataSource(ctx, api, f.DatabaseID)
				if err != nil {
					return plan, err
				}
				if ptype != "data_source_id" || norm(pid) != norm(ds) {
					return plan, fmt.Errorf("%s: page is not in the specified database", f.Path)
				}
			}
			m, err := getMarkdown(ctx, api, f.PageID)
			if err != nil {
				return plan, err
			}
			if m.Markdown != "" && m.Markdown != f.SyncBody {
				return plan, fmt.Errorf("%s: existing page has content and no synchronization baseline", f.Path)
			}
			a.Entry = Entry{PageID: f.PageID, ParentID: pid, ParentType: ptype, Target: a.Target, RemoteHash: remoteMarkdownHash(m.Markdown)}
			if m.Markdown == f.SyncBody {
				a.Kind = "adopt"
			} else {
				a.Kind = "update"
			}
		default:
			if a.ParentID != "" && a.ParentType == "page_id" {
				if _, err := getPage(ctx, api, a.ParentID); err != nil {
					return plan, fmt.Errorf("%s: parent: %w", f.Path, err)
				}
			}
			a.Kind = "create"
		}
		plan.Actions = append(plan.Actions, a)
	}
	for key := range s.Entries {
		if strings.HasPrefix(key, "file:") && !live[key] {
			plan.Deleted = append(plan.Deleted, strings.TrimPrefix(key, "file:"))
		}
	}
	sort.Strings(plan.Deleted)
	return plan, nil
}

func appendImages(ctx context.Context, api Client, pageID string, action Action, c config.Config) (map[string]string, error) {
	blockIDs := make(map[string]string, len(action.File.Images))
	if len(action.File.Images) == 0 {
		return blockIDs, nil
	}
	uploader, uploadOK := api.(MultipartClient)
	if !uploadOK {
		return nil, fmt.Errorf("file uploads are not supported by the Notion client")
	}
	if _, _, err := bodyWithoutImages(action.File.SyncBody); err != nil {
		return nil, err
	}
	if len(action.TextParts) > 0 && len(action.TextParts) != len(action.File.Images)+1 {
		return nil, fmt.Errorf("%s: local image segments are inconsistent", action.Path)
	}
	for i, image := range action.File.Images {
		full := filepath.Join(c.SourcePath, filepath.FromSlash(image.Path))
		if err := verifyImageUnchanged(full, image.Hash); err != nil {
			return nil, fmt.Errorf("%s: %w", action.Path, err)
		}
		blockType := "image"
		var child map[string]any
		if c.Sync.DrawioEmbed && isDrawioSVG(image) {
			blockType = "embed"
			child = map[string]any{"object": "block", "type": "embed", "embed": map[string]any{"url": ""}}
		} else {
			uploadID, err := uploadImage(ctx, uploader, image, full)
			if err != nil {
				return nil, fmt.Errorf("%s: image upload outcome uncertain; inspect Notion before retry: %w", action.Path, err)
			}
			child = map[string]any{"object": "block", "type": "image", "image": map[string]any{"type": "file_upload", "file_upload": map[string]any{"id": uploadID}, "caption": image.Caption}}
		}
		var result struct {
			Results []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"results"`
		}
		if err := api.API(ctx, "PATCH", "v1/blocks/"+pageID+"/children", map[string]any{"children": []any{child}}, &result); err != nil {
			return nil, fmt.Errorf("%s: image attachment outcome uncertain; inspect Notion before retry: %w", action.Path, err)
		}
		if len(result.Results) != 1 || result.Results[0].Type != blockType || !config.ValidID(result.Results[0].ID) {
			return nil, fmt.Errorf("%s: unexpected image attachment response", action.Path)
		}
		blockIDs[imageStateKey(image)] = result.Results[0].ID
		if len(action.TextParts) > 0 && strings.TrimSpace(action.TextParts[i+1]) != "" {
			text := action.TextParts[i+1]
			var inserted markdown
			body := map[string]any{"type": "insert_content", "insert_content": map[string]any{"content": text, "position": map[string]any{"type": "end"}}}
			if err := api.API(ctx, "PATCH", "v1/pages/"+pageID+"/markdown", body, &inserted); err != nil {
				return nil, fmt.Errorf("%s: Markdown after image insertion has uncertain outcome; inspect Notion before retry: %w", action.Path, err)
			}
			if issue := markdownUpdateIssue(inserted, pageID); issue != "" {
				return nil, fmt.Errorf("%s: Markdown after image insertion returned warnings or incomplete content: %s; inspect Notion before retrying", action.Path, issue)
			}
		}
	}
	return blockIDs, nil
}

func replaceImages(ctx context.Context, api Client, pageID string, action Action, allImages []Image, sourceRoot string, entry *Entry) error {
	uploader, ok := api.(MultipartClient)
	if !ok {
		return fmt.Errorf("file uploads are not supported by the Notion client")
	}
	if entry.ImageBlocks == nil {
		entry.ImageBlocks = map[string]string{}
	}
	if len(entry.ImageBlocks) < len(allImages) {
		var children struct {
			Results []struct {
				ID    string `json:"id"`
				Type  string `json:"type"`
				Image struct {
					Caption []struct {
						PlainText string `json:"plain_text"`
					} `json:"caption"`
				} `json:"image"`
			} `json:"results"`
			HasMore bool `json:"has_more"`
		}
		if err := api.API(ctx, "GET", "v1/blocks/"+pageID+"/children?page_size=100", nil, &children); err != nil {
			return fmt.Errorf("%s: cannot identify existing image blocks; inspect Notion and sync state: %w", action.Path, err)
		}
		var ids []string
		for _, child := range children.Results {
			if child.Type == "image" {
				ids = append(ids, child.ID)
			}
		}
		if children.HasMore || len(ids) != len(entry.ImageHashes) || len(ids) != len(allImages) {
			return fmt.Errorf("%s: image block IDs are missing and page images cannot be matched safely; reset metadata or inspect Notion", action.Path)
		}
		// Legacy metadata has no block IDs. Match only when current captions still
		// identify the images in their original order.
		for i, image := range allImages {
			if len(children.Results) > 0 {
				var caption strings.Builder
				for _, child := range children.Results {
					if child.ID == ids[i] {
						for _, part := range child.Image.Caption {
							caption.WriteString(part.PlainText)
						}
						break
					}
				}
				if caption.String() != image.Alt {
					return fmt.Errorf("%s: existing image captions do not match local images; cannot safely replace legacy image blocks", action.Path)
				}
			}
			entry.ImageBlocks[imageStateKey(image)] = ids[i]
		}
	}
	for _, image := range action.File.Images {
		key := imageStateKey(image)
		blockID := entry.ImageBlocks[key]
		if !config.ValidID(blockID) {
			return fmt.Errorf("%s: missing image block ID for %s; inspect Notion and reset metadata if needed", action.Path, image.Path)
		}
		full := filepath.Join(sourceRoot, filepath.FromSlash(image.Path))
		if err := verifyImageUnchanged(full, image.Hash); err != nil {
			return fmt.Errorf("%s: %w", action.Path, err)
		}
		uploadID, err := uploadImage(ctx, uploader, image, full)
		if err != nil {
			return fmt.Errorf("%s: image upload outcome uncertain; inspect Notion before retry: %w", action.Path, err)
		}
		// Update-block requests differ from append-children requests: the nested
		// image object accepts file_upload directly and rejects a nested `type`.
		body := map[string]any{"type": "image", "image": map[string]any{"file_upload": map[string]any{"id": uploadID}, "caption": image.Caption}}
		var result struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Object string `json:"object"`
		}
		if err := api.API(ctx, "PATCH", "v1/blocks/"+blockID, body, &result); err != nil {
			return fmt.Errorf("%s: image replacement outcome uncertain; inspect Notion before retry: %w", action.Path, err)
		}
		if result.Object != "block" || result.Type != "image" || norm(result.ID) != norm(blockID) {
			return fmt.Errorf("%s: unexpected image replacement response", action.Path)
		}
		if entry.ImageHashes == nil {
			entry.ImageHashes = map[string]string{}
		}
		entry.ImageHashes[key] = image.Hash
	}
	return nil
}

func verifyImageUnchanged(filename, expected string) error {
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("image is no longer a regular file")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	if hash(string(data)) != expected {
		return fmt.Errorf("image changed during sync; run diff again before retrying")
	}
	return nil
}
func printPlan(p Plan, print func(string, ...any)) {
	for _, a := range p.Actions {
		if a.Kind != "directory-existing" {
			destination := a.Target
			if a.Entry.PageID != "" {
				destination = pageURL(a.Entry.PageID)
			} else if a.ParentID != "" {
				if a.ParentType == "page_id" {
					destination = pageURL(a.ParentID)
				} else {
					destination = a.ParentType + ":" + a.ParentID
				}
			}
			switch {
			case a.Overwrite:
				print("overwrite %s -> %s (Notion content changed)", a.Path, destination)
				if a.ConflictDiff != "" {
					print("%s", DiffOutput{Plain: a.ConflictDiff, Numbered: a.ConflictDiffNumbered})
				}
			case a.Kind == "images-update":
				images := changedUnmanagedImages(a.File.Images, a.Entry)
				print("update images (%d) %s -> %s", len(images), a.Path, destination)
			case len(a.File.Images) > 0 && (a.Kind == "unchanged" || a.Kind == "normalize"):
				changed := changedUnmanagedImages(a.File.Images, a.Entry)
				if len(changed) > 0 {
					print("%s %s -> %s (images changed: %d)", a.Kind, a.Path, destination, len(changed))
				} else if len(a.Entry.managedEmbeds) > 0 {
					print("%s %s -> %s (draw.io embeds preserved)", a.Kind, a.Path, destination)
				} else {
					print("%s %s -> %s (images unchanged: %d; not uploaded)", a.Kind, a.Path, destination, len(a.File.Images))
				}
			case a.Kind == "update" && len(a.Entry.managedEmbeds) > 0:
				print("update %s -> %s (draw.io embeds checked before updating)", a.Path, destination)
			case a.Kind == "update" && len(a.File.Images) > 0:
				print("%s %s -> %s (images replaced: %d)", a.Kind, a.Path, destination, len(a.File.Images))
			default:
				print("%s %s -> %s", a.Kind, a.Path, destination)
			}
		}
	}
	for _, d := range p.Deleted {
		print("local file missing (no deletion): %s", d)
	}
}
func pageURL(id string) string { return "https://notion.so/" + config.FormatID(id) }
func makeTitle(name string) map[string]any {
	return map[string]any{"title": []any{map[string]any{"text": map[string]any{"content": name}}}}
}
func Apply(ctx context.Context, c config.Config, api Client, s *State, p Plan, print func(string, ...any)) error {
	skipped := map[string]bool{}
	appendOnly := map[string]string{}
	preservedDrawio := map[string][]drawioTextUpdate{}
	for _, action := range p.Actions {
		if c.Sync.DrawioEmbed && action.Kind == "update" && len(action.Entry.managedEmbeds) > 0 {
			m, err := getMarkdown(ctx, api, action.Entry.PageID)
			if err != nil {
				return err
			}
			_, _, err = normalizeManagedEmbeds(ctx, api, action.Entry, m.Markdown)
			if err != nil {
				return err
			}
			updates, safe, err := drawioTextUpdates(ctx, api, action, m.Markdown)
			if err != nil {
				return fmt.Errorf("%s: %w", action.Path, err)
			}
			if safe {
				preservedDrawio[action.Key] = updates
				continue
			}
			addition, err := appendedDrawioText(ctx, api, action.Entry, action.File, m.Markdown)
			if err != nil {
				return err
			}
			if addition != "" {
				appendOnly[action.Key] = addition
				continue
			}
			skipped[action.Key] = true
			continue
		}
		if action.Kind == "update" && len(action.Entry.ImageHashes) > len(action.File.Images) {
			return fmt.Errorf("%s: removing local images from an existing page is not supported", action.Path)
		}
	}
	for _, action := range p.Actions {
		if skipped[action.Key] {
			continue
		}
		if _, ok := preservedDrawio[action.Key]; ok {
			continue
		}
		if len(action.File.Images) > 0 && (action.Kind == "create" || action.Kind == "update" || action.Kind == "images-update") {
			if _, ok := api.(MultipartClient); !ok {
				return fmt.Errorf("%s: Notion client does not support file uploads", action.Path)
			}
			if f := s.Documents[action.Path]; f != nil {
				if err := verifyFileUnchanged(c, f); err != nil {
					return err
				}
			}
			for _, img := range action.File.Images {
				if err := verifyImageUnchanged(filepath.Join(c.SourcePath, filepath.FromSlash(img.Path)), img.Hash); err != nil {
					return fmt.Errorf("%s: %w", action.Path, err)
				}
				info, err := os.Stat(filepath.Join(c.SourcePath, filepath.FromSlash(img.Path)))
				if err != nil {
					return err
				}
				if info.Size() > 20*1024*1024 {
					return fmt.Errorf("%s: image %s exceeds the 20 MB single-part upload limit", action.Path, img.Path)
				}
			}
		}
	}
	for _, a := range p.Actions {
		if skipped[a.Key] {
			if s.Skipped == nil {
				s.Skipped = map[string]bool{}
			}
			s.Skipped[a.Path] = true
			print("warning %s: page not synchronized because existing draw.io embeds cannot be preserved safely; update the page manually", a.Path)
			continue
		}
		if strings.HasPrefix(a.Key, "file:") {
			f := s.Documents[a.Path]
			if f == nil {
				return fmt.Errorf("missing source file %s", a.Path)
			}
			if err := verifyFileUnchanged(c, f); err != nil {
				return err
			}
		}
		if planned, ok := preservedDrawio[a.Key]; ok {
			e := a.Entry
			before, err := validateExisting(ctx, api, e)
			if err != nil {
				return fmt.Errorf("%s: %w", a.Path, err)
			}
			current, safe, err := drawioTextUpdates(ctx, api, a, before.Markdown)
			if err != nil || !safe || !reflect.DeepEqual(current, planned) {
				return fmt.Errorf("%s: page changed while preparing the draw.io-preserving update; inspect Notion before retrying", a.Path)
			}
			for _, update := range current {
				if err := verifyFileUnchanged(c, s.Documents[a.Path]); err != nil {
					return err
				}
				if strings.Count(before.Markdown, update.Old) != 1 {
					return fmt.Errorf("%s: text changed during the draw.io-preserving update; inspect Notion before retrying", a.Path)
				}
				if update.Range != "" {
					selection, ok := drawioTextRange(before.Markdown, update.Old)
					if !ok || selection != update.Range {
						return fmt.Errorf("%s: text boundaries changed during the draw.io-preserving update; inspect Notion before retrying", a.Path)
					}
				}
				segment := len(embedMarkdown.FindAllStringIndex(before.Markdown[:strings.Index(before.Markdown, update.Old)], -1))
				previousSegments := embedMarkdown.Split(before.Markdown, -1)
				body := map[string]any{"type": "update_content", "update_content": map[string]any{"content_updates": []any{map[string]any{"old_str": update.Old, "new_str": update.New}}}}
				if update.Range != "" {
					body = map[string]any{"type": "replace_content_range", "replace_content_range": map[string]any{"content_range": update.Range, "content": update.New}}
				}
				var updated markdown
				if err := api.API(ctx, "PATCH", "v1/pages/"+e.PageID+"/markdown", body, &updated); err != nil {
					return fmt.Errorf("%s: text update outcome uncertain; inspect Notion before retry: %w", a.Path, err)
				}
				if issue := markdownUpdateIssue(updated, e.PageID); issue != "" {
					return fmt.Errorf("%s: text update returned warnings or incomplete content: %s; the update may be partially applied, inspect Notion before retrying", a.Path, issue)
				}
				after, err := getMarkdown(ctx, api, e.PageID)
				if err != nil {
					return fmt.Errorf("%s: text updated but readback failed; inspect Notion: %w", a.Path, err)
				}
				if _, _, err := normalizeManagedEmbeds(ctx, api, e, after.Markdown); err != nil {
					return fmt.Errorf("%s: text updated but embed readback failed; inspect Notion: %w", a.Path, err)
				}
				afterSegments := embedMarkdown.Split(after.Markdown, -1)
				if len(afterSegments) != len(previousSegments) {
					return fmt.Errorf("%s: embed layout changed during text update; inspect Notion", a.Path)
				}
				for i := range afterSegments {
					if i != segment && strings.TrimSpace(afterSegments[i]) != strings.TrimSpace(previousSegments[i]) {
						return fmt.Errorf("%s: other content changed during text update; inspect Notion", a.Path)
					}
				}
				before = after
			}
			normalized, _, err := normalizeManagedEmbeds(ctx, api, e, before.Markdown)
			if err != nil {
				return fmt.Errorf("%s: text updated but embed readback failed; inspect Notion: %w", a.Path, err)
			}
			e.LocalHash = a.File.Hash
			e.RemoteHash = remoteMarkdownHash(normalized)
			e.ImageHashes = imageHashesPreservingDrawio(a.File.Images, e)
			s.Entries[a.Key] = e
			if err := saveState(c, *s); err != nil {
				return err
			}
			print("updated %s -> %s (draw.io embeds preserved)", a.Path, pageURL(e.PageID))
			continue
		}
		if addition := appendOnly[a.Key]; addition != "" {
			e := a.Entry
			before, err := validateExisting(ctx, api, e)
			if err != nil {
				return fmt.Errorf("%s: %w", a.Path, err)
			}
			current, err := appendedDrawioText(ctx, api, e, a.File, before.Markdown)
			if err != nil || current != addition {
				return fmt.Errorf("%s: page changed while preparing the text append; inspect Notion before retrying", a.Path)
			}
			var inserted markdown
			body := map[string]any{"type": "insert_content", "insert_content": map[string]any{"content": addition, "position": map[string]any{"type": "end"}}}
			if err := api.API(ctx, "PATCH", "v1/pages/"+e.PageID+"/markdown", body, &inserted); err != nil {
				return fmt.Errorf("%s: text append outcome uncertain; inspect Notion before retry: %w", a.Path, err)
			}
			if issue := markdownUpdateIssue(inserted, e.PageID); issue != "" {
				return fmt.Errorf("%s: text append returned warnings or incomplete content: %s; inspect Notion before retrying", a.Path, issue)
			}
			after, err := getMarkdown(ctx, api, e.PageID)
			if err != nil {
				return fmt.Errorf("%s: text appended but readback failed; inspect Notion: %w", a.Path, err)
			}
			normalized, _, err := normalizeManagedEmbeds(ctx, api, e, after.Markdown)
			if err != nil {
				return fmt.Errorf("%s: text appended but embed readback failed; inspect Notion: %w", a.Path, err)
			}
			e.LocalHash = a.File.Hash
			e.RemoteHash = remoteMarkdownHash(normalized)
			s.Entries[a.Key] = e
			if err := saveState(c, *s); err != nil {
				return err
			}
			print("updated %s -> %s (appended text; draw.io embeds preserved)", a.Path, pageURL(e.PageID))
			continue
		}
		switch a.Kind {
		case "directory-existing":
			continue
		case "unchanged":
			continue
		case "images-update":
			e := a.Entry
			if _, err := validateExisting(ctx, api, e); err != nil {
				return fmt.Errorf("%s: %w", a.Path, err)
			}
			images := changedImages(a.File.Images, e.ImageHashes)
			if err := replaceImages(ctx, api, e.PageID, Action{Path: a.Path, File: File{Body: a.File.Body, Images: images}}, a.File.Images, c.SourcePath, &e); err != nil {
				return err
			}
			s.Entries[a.Key] = e
			if err := saveState(c, *s); err != nil {
				return err
			}
			print("updated images (%d) %s -> %s", len(images), a.Path, pageURL(e.PageID))
		case "normalize":
			f := s.Documents[a.Path]
			if err := writeMetadata(c, f, f.Metadata); err != nil {
				return err
			}
			print("normalized %s -> %s", a.Path, pageURL(a.Entry.PageID))
		case "adopt":
			if _, err := validateExisting(ctx, api, a.Entry); err != nil {
				return fmt.Errorf("%s: %w", a.Path, err)
			}
			a.Entry.LocalHash = a.File.Hash
			s.Entries[a.Key] = a.Entry
			if err := saveState(c, *s); err != nil {
				return err
			}
			print("adopted %s -> %s", a.Path, pageURL(a.Entry.PageID))
		case "directory-create", "create":
			parentID := a.ParentID
			if parentID == "" {
				dir := path.Dir(a.Path)
				parentID = s.Entries["dir:"+dir].PageID
			}
			if !config.ValidID(parentID) {
				return fmt.Errorf("%s: parent has not been created", a.Path)
			}
			parentType := a.ParentType
			if parentType == "" {
				parentType = "page_id"
			}
			s.Pending = &Pending{Key: a.Key, ParentID: parentID, Title: a.Title}
			if err := saveState(c, *s); err != nil {
				return err
			}
			parent := map[string]any{parentType: parentID}
			propName := "title"
			if a.TitleProperty != "" {
				propName = a.TitleProperty
			}
			body := map[string]any{"parent": parent, "properties": map[string]any{propName: makeTitle(a.Title)}}
			if a.Kind == "create" && a.Body != "" {
				body["markdown"] = a.Body
			}
			var created page
			if err := api.API(ctx, "POST", "v1/pages", body, &created); err != nil {
				return fmt.Errorf("%s: creation outcome uncertain; pending state retained: %w", a.Path, err)
			}
			pid, ptype := parentOf(created)
			if created.Object != "page" || !config.ValidID(created.ID) || norm(pid) != norm(parentID) || ptype != parentType {
				return fmt.Errorf("%s: unexpected create response; pending state retained", a.Path)
			}
			s.Pending.PageID = created.ID
			if err := saveState(c, *s); err != nil {
				return err
			}
			var imageBlocks map[string]string
			if a.Kind == "create" {
				var err error
				imageBlocks, err = appendImages(ctx, api, created.ID, a, c)
				if err != nil {
					return err
				}
			}
			e := Entry{PageID: created.ID, ParentID: parentID, ParentType: parentType, Target: a.Target}
			if a.Kind == "create" {
				m, err := getMarkdown(ctx, api, created.ID)
				if err != nil {
					return fmt.Errorf("%s: created but readback failed; pending state retained: %w", a.Path, err)
				}
				e.RemoteHash = remoteMarkdownHash(m.Markdown)
				e.LocalHash = a.File.Hash
				e.ImageHashes = imageHashMap(a.File.Images)
				e.ImageBlocks = imageBlocks
				if c.Sync.DrawioEmbed {
					trackDrawioEmbeds(&e, a.File.Images)
				}
			}
			s.Entries[a.Key] = e
			s.Pending = nil
			if err := saveState(c, *s); err != nil {
				return err
			}
			print("created %s -> %s", a.Path, pageURL(created.ID))
		case "update":
			e := a.Entry
			if _, err := validateExisting(ctx, api, e); err != nil {
				return fmt.Errorf("%s: %w", a.Path, err)
			}
			if err := verifyFileUnchanged(c, s.Documents[a.Path]); err != nil {
				return err
			}
			body := map[string]any{"type": "replace_content", "replace_content": map[string]any{"new_str": a.Body}}
			var updated markdown
			if err := api.API(ctx, "PATCH", "v1/pages/"+e.PageID+"/markdown", body, &updated); err != nil {
				return fmt.Errorf("%s: update failed or outcome uncertain; recheck Notion before retry: %w", a.Path, err)
			}
			if issue := markdownUpdateIssue(updated, e.PageID); issue != "" {
				return fmt.Errorf("%s: update returned warnings or incomplete content: %s; inspect Notion before retrying", a.Path, issue)
			}
			if len(a.File.Images) > 0 {
				// replace_content rebuilds the page body, including its image blocks.
				// Recreate every referenced local image at its original text position.
				blockIDs, err := appendImages(ctx, api, e.PageID, a, c)
				if err != nil {
					return err
				}
				e.ImageHashes = imageHashMap(a.File.Images)
				e.ImageBlocks = blockIDs
				if c.Sync.DrawioEmbed {
					trackDrawioEmbeds(&e, a.File.Images)
				}
				updated, err = getMarkdown(ctx, api, e.PageID)
				if err != nil {
					return fmt.Errorf("%s: updated but final content readback failed; inspect Notion: %w", a.Path, err)
				}
			}
			e.LocalHash = a.File.Hash
			e.RemoteHash = remoteMarkdownHash(updated.Markdown)
			s.Entries[a.Key] = e
			if err := saveState(c, *s); err != nil {
				return err
			}
			if len(a.File.Images) > 0 {
				print("updated %s -> %s (images replaced: %d)", a.Path, pageURL(e.PageID), len(a.File.Images))
			} else {
				print("updated %s -> %s", a.Path, pageURL(e.PageID))
			}
		}
	}
	return nil
}
func Run(ctx context.Context, c config.Config, api Client, apply bool, print func(string, ...any), overwrite ...bool) error {
	var unlock func()
	if apply {
		var err error
		unlock, err = lock(c)
		if err != nil {
			return err
		}
		defer unlock()
	}
	files, err := Scan(c)
	if err != nil {
		return err
	}
	s, err := loadState(c)
	if err != nil {
		return err
	}
	for i := range files {
		if stored := s.Documents[files[i].Path]; stored != nil {
			files[i] = *stored
			s.Active[stored.Path] = true
		}
	}
	prepareLinks(files, s, c.SourcePath)
	p, err := Build(ctx, c, api, files, s, overwrite...)
	if err != nil {
		return err
	}
	printPlan(p, print)
	if c.Sync.DrawioEmbed {
		if err := exportDrawioURL(c, p, apply, print); err != nil {
			return err
		}
	}
	if !apply {
		if c.Sync.DrawioEmbed {
			print("draw.io SVGs: new embeds export viewer URLs to .notion-sync/; existing embeds are preserved")
		}
		return nil
	}
	// Create every new page before resolving links, so forward and cyclic links
	// have a page ID. A distinct baseline makes an interrupted shell creation
	// resume as an ordinary content update on the next run.
	shells := Plan{}
	for _, a := range p.Actions {
		switch a.Kind {
		case "directory-create", "directory-existing":
			shells.Actions = append(shells.Actions, a)
		case "create":
			a.Body = ""
			a.TextParts = nil
			a.File.Images = nil
			a.File.Hash = hash("notion-sync:empty-page-awaiting-content")
			shells.Actions = append(shells.Actions, a)
		}
	}
	if len(shells.Actions) > 0 {
		if err := Apply(ctx, c, api, &s, shells, print); err != nil {
			return err
		}
		for i := range files {
			files[i] = *s.Documents[files[i].Path]
		}
		prepareLinks(files, s, c.SourcePath)
		p, err = Build(ctx, c, api, files, s, overwrite...)
		if err != nil {
			return err
		}
	}
	if err := Apply(ctx, c, api, &s, p, print); err != nil {
		return err
	}
	return finishLinkSync(ctx, c, api, &s, files, print, overwrite...)
}

// RunFile limits synchronization to one scanned file; other state entries are retained.
func RunFile(ctx context.Context, c config.Config, api Client, filename string, apply bool, print func(string, ...any), overwrite ...bool) error {
	var unlock func()
	if apply {
		var err error
		unlock, err = lock(c)
		if err != nil {
			return err
		}
		defer unlock()
	}
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
		return fmt.Errorf("file must be inside the configured source")
	}
	rel = filepath.ToSlash(rel)
	inc, err := match(c.Sync.Include, rel)
	if err != nil {
		return err
	}
	exc, err := match(c.Sync.Exclude, rel)
	if err != nil {
		return err
	}
	if !inc || exc || strings.ToLower(path.Ext(rel)) != ".md" {
		return fmt.Errorf("file is not included by the configuration")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return err
	}
	file, err := parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	file.Path = rel
	selected := []File{file}
	s, err := loadState(c)
	if err != nil {
		return err
	}
	stored := s.Documents[rel]
	if stored == nil {
		return fmt.Errorf("file is not included by the configuration")
	}
	if !bytes.Equal(stored.Raw, selected[0].Raw) {
		return fmt.Errorf("%s changed while scanning", rel)
	}
	selected[0] = *stored
	s.Active[rel] = true
	prepareLinks(selected, s, c.SourcePath)
	p, err := Build(ctx, c, api, selected, s, overwrite...)
	if err != nil {
		return err
	}
	p.Deleted = nil
	printPlan(p, print)
	if c.Sync.DrawioEmbed {
		if err := exportDrawioURL(c, p, apply, print); err != nil {
			return err
		}
	}
	if !apply {
		if c.Sync.DrawioEmbed {
			print("draw.io SVGs: new embeds export viewer URLs to .notion-sync/; existing embeds are preserved")
		}
		return nil
	}
	var shells Plan
	for _, action := range p.Actions {
		switch action.Kind {
		case "directory-create", "directory-existing":
			shells.Actions = append(shells.Actions, action)
		case "create":
			action.Body, action.TextParts, action.File.Images = "", nil, nil
			action.File.Hash = hash("notion-sync:empty-page-awaiting-content")
			shells.Actions = append(shells.Actions, action)
		}
	}
	if len(shells.Actions) > 0 {
		if err := Apply(ctx, c, api, &s, shells, print); err != nil {
			return err
		}
		selected[0] = *s.Documents[rel]
		prepareLinks(selected, s, c.SourcePath)
		p, err = Build(ctx, c, api, selected, s, overwrite...)
		if err != nil {
			return err
		}
	}
	if err := Apply(ctx, c, api, &s, p, print); err != nil {
		return err
	}
	return finishLinkSync(ctx, c, api, &s, selected, print, overwrite...)
}

// Newly adopted pages can provide IDs for links prepared earlier in the run.
func finishLinkSync(ctx context.Context, c config.Config, api Client, s *State, files []File, print func(string, ...any), overwrite ...bool) error {
	refresh := func() []File {
		ready := make([]File, 0, len(files))
		for _, f := range files {
			if !s.Skipped[f.Path] {
				ready = append(ready, *s.Documents[f.Path])
			}
		}
		prepareLinks(ready, *s, c.SourcePath)
		return ready
	}
	ready := refresh()
	needsUpdate := false
	for _, f := range ready {
		if e, ok := s.Entries["file:"+f.Path]; ok && f.Hash != e.LocalHash {
			needsUpdate = true
		}
	}
	if !needsUpdate {
		return nil
	}
	plan, err := Build(ctx, c, api, ready, *s, overwrite...)
	if err != nil {
		return err
	}
	if err := Apply(ctx, c, api, s, plan, print); err != nil {
		return err
	}
	for _, f := range refresh() {
		if e, ok := s.Entries["file:"+f.Path]; ok && f.Hash != e.LocalHash {
			return fmt.Errorf("%s: link dependencies changed during sync; retry", f.Path)
		}
	}
	return nil
}
