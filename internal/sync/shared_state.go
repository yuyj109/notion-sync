package sync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuyj109/notion-sync/internal/config"
)

// sharedState keeps creation safety information independently of the files
// selected for a particular invocation. File baselines remain in Markdown.
type sharedState struct {
	Version            int              `json:"version"`
	RootID             string           `json:"root_id"`
	Directories        map[string]Entry `json:"directories,omitempty"`
	Pending            *Pending         `json:"pending,omitempty"`
	PendingOwner       string           `json:"pending_owner,omitempty"`
	LegacyScanComplete bool             `json:"legacy_scan_complete,omitempty"`
}

func sharedStatePath(c config.Config, root string) string {
	return filepath.Join(c.SourcePath, ".notion-sync", "state-"+norm(root)+".json")
}

func compatiblePending(a, b *Pending) bool {
	return a.Key == b.Key && norm(a.ParentID) == norm(b.ParentID) && a.Title == b.Title && (a.PageID == "" || b.PageID == "" || norm(a.PageID) == norm(b.PageID))
}

func sameDirectory(a, b Entry) bool {
	return norm(a.PageID) == norm(b.PageID) && norm(a.ParentID) == norm(b.ParentID) && a.ParentType == b.ParentType && a.Target == b.Target
}

func loadSharedState(c config.Config, s *State) error {
	data, err := os.ReadFile(sharedStatePath(c, s.RootID))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read shared sync state: %w", err)
	}
	var stored sharedState
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("invalid shared sync state: %w", err)
	}
	if stored.Version != 1 || norm(stored.RootID) != norm(s.RootID) {
		return fmt.Errorf("invalid shared sync state version or root")
	}
	s.sharedMigrated = stored.LegacyScanComplete
	for name, entry := range stored.Directories {
		if name == "" || name == "." || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || filepath.ToSlash(filepath.Clean(name)) != name || !config.ValidID(entry.PageID) || !config.ValidID(entry.ParentID) || entry.ParentType != "page_id" {
			return fmt.Errorf("invalid shared directory state for %q", name)
		}
		s.Entries["dir:"+name] = entry
	}
	if stored.Pending != nil {
		if stored.PendingOwner == "" || stored.Pending.Key == "" || !config.ValidID(stored.Pending.ParentID) || stored.Pending.PageID != "" && !config.ValidID(stored.Pending.PageID) {
			return fmt.Errorf("invalid shared pending state")
		}
		s.Pending, s.PendingOwner = stored.Pending, stored.PendingOwner
	}
	return nil
}

func saveSharedState(c config.Config, s State) error {
	stored := sharedState{Version: 1, RootID: s.RootID, Directories: map[string]Entry{}, Pending: s.Pending, LegacyScanComplete: s.sharedMigrated}
	if s.Pending != nil {
		stored.PendingOwner = s.PendingOwner
	}
	for key, entry := range s.Entries {
		if dir, ok := strings.CutPrefix(key, "dir:"); ok {
			stored.Directories[dir] = entry
		}
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	name := sharedStatePath(c, s.RootID)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), ".state-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}

func checkpointSharedMigration(c config.Config, s *State) error {
	if !s.needsSharedCheckpoint {
		return nil
	}
	if err := saveSharedState(c, *s); err != nil {
		return fmt.Errorf("save shared state migration: %w", err)
	}
	s.needsSharedCheckpoint = false
	return nil
}

// Older versions stored shared information only in Markdown. Inspect excluded
// files for that information without resolving their images or syncing them.
func loadExcludedMetadata(c config.Config, s *State) error {
	root, err := filepath.EvalSymlinks(c.SourcePath)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(name string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() || !strings.EqualFold(filepath.Ext(name), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if s.Documents[rel] != nil {
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if !bytes.Contains(data, []byte("notion-root-id")) && !bytes.Contains(data, []byte("notion-pending-")) {
			return nil
		}
		f, err := parse(data)
		if err != nil {
			return fmt.Errorf("%s: cannot inspect excluded file's synchronization metadata: %w", rel, err)
		}
		m := f.Metadata
		if m == nil || norm(m.RootID) != norm(s.RootID) {
			return nil
		}
		for dir, entry := range m.Directories {
			key := "dir:" + dir
			if prior, ok := s.Entries[key]; ok && !sameDirectory(prior, entry) {
				return fmt.Errorf("conflicting directory metadata for %s", dir)
			}
			s.Entries[key] = entry
		}
		if m.Pending != nil {
			if s.Pending != nil && !compatiblePending(s.Pending, m.Pending) {
				return fmt.Errorf("conflicting pending metadata")
			}
			if s.Pending == nil || s.Pending.PageID == "" {
				s.Pending = m.Pending
			}
			if s.PendingOwner != "" && s.PendingOwner != rel {
				return fmt.Errorf("conflicting pending owner")
			}
			s.PendingOwner = rel
		}
		return nil
	})
}
