package sync

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/yuyj109/notion-sync/internal/config"
)

// Recover resolves a pending creation after the user inspects the destination.
func Recover(ctx context.Context, c config.Config, api Client, filename, pageID string, notCreated bool) (string, error) {
	if pageID != "" && notCreated {
		return "", fmt.Errorf("--page-id and --not-created cannot be combined")
	}
	unlock, err := lock(c)
	if err != nil {
		return "", err
	}
	defer unlock()
	root, err := filepath.EvalSymlinks(c.SourcePath)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filename)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("file must be a regular file")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file must be inside the source directory")
	}
	rel = filepath.ToSlash(rel)
	s, err := loadStateWithPending(c, true)
	if err != nil {
		return "", err
	}
	f := s.Documents[rel]
	if f == nil || f.Metadata == nil || f.Metadata.Pending == nil {
		return "", fmt.Errorf("%s has no pending creation", rel)
	}
	pending := f.Metadata.Pending
	if s.Pending == nil || s.Pending.Key != pending.Key {
		return "", fmt.Errorf("pending metadata does not match")
	}
	if notCreated {
		if pending.PageID != "" {
			return "", fmt.Errorf("pending creation has a known page ID; use --page-id")
		}
		s.Pending = nil
		s.Active[rel] = true
		if err := saveState(c, s); err != nil {
			return "", err
		}
		return "Cleared pending creation after manual verification.", nil
	}
	if pageID == "" {
		pageID = pending.PageID
	}
	if !config.ValidID(pageID) {
		return "", fmt.Errorf("provide the verified page ID with --page-id")
	}
	if pending.PageID != "" && norm(pageID) != norm(pending.PageID) {
		return "", fmt.Errorf("page ID differs from the recorded creation response")
	}
	p, err := getPage(ctx, api, pageID)
	if err != nil {
		return "", err
	}
	parentID, parentType := parentOf(p)
	if norm(parentID) != norm(pending.ParentID) || (parentType != "page_id" && parentType != "data_source_id") {
		return "", fmt.Errorf("page does not have the recorded parent")
	}
	if strings.HasPrefix(pending.Key, "file:") {
		if pending.Key != "file:"+rel {
			return "", fmt.Errorf("pending file does not match selected file")
		}
		m, err := getMarkdown(ctx, api, pageID)
		if err != nil {
			return "", err
		}
		localHash := hash("recovery-pending:" + f.Hash)
		imageHashes := map[string]string(nil)
		if len(f.Images) > 0 && m.Markdown != "" {
			// For image pages, recovery is an explicit user confirmation that the
			// created page and its attachments match the local source. Record that
			// baseline so the next sync does not replace or duplicate image blocks.
			localHash = f.Hash
			imageHashes = imageHashMap(f.Images)
		} else if localHash == f.Hash {
			localHash = hash("recovery-pending-again:" + f.Hash)
		}
		s.Entries[pending.Key] = Entry{PageID: pageID, ParentID: parentID, ParentType: parentType, Target: "page:" + norm(pageID), LocalHash: localHash, RemoteHash: remoteMarkdownHash(m.Markdown), ImageHashes: imageHashes}
	} else if after, ok := strings.CutPrefix(pending.Key, "dir:"); ok {
		dir := after
		if !strings.HasPrefix(rel, dir+"/") || parentType != "page_id" {
			return "", fmt.Errorf("pending directory does not match selected file")
		}
		s.Entries[pending.Key] = Entry{PageID: pageID, ParentID: parentID, ParentType: parentType, Target: "dir:" + path.Dir(dir)}
	} else {
		return "", fmt.Errorf("unsupported pending key")
	}
	s.Pending = nil
	s.Active[rel] = true
	if err := saveState(c, s); err != nil {
		return "", err
	}
	return "Recovered " + pageURL(pageID) + "; run diff and sync to finish.", nil
}
