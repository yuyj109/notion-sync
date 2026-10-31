package sync

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/yuyj109/notion-sync/internal/config"
	"gopkg.in/yaml.v3"
)

type Entry struct {
	PageID      string            `json:"pageId" yaml:"page-id"`
	ParentID    string            `json:"parentId" yaml:"parent-id"`
	ParentType  string            `json:"parentType" yaml:"parent-type"`
	Target      string            `json:"target" yaml:"target"`
	LocalHash   string            `json:"localHash,omitempty" yaml:"local-hash,omitempty"`
	RemoteHash  string            `json:"remoteHash,omitempty" yaml:"remote-hash,omitempty"`
	ImageHashes map[string]string `json:"imageHashes,omitempty" yaml:"image-hashes,omitempty"`
	ImageBlocks map[string]string `json:"imageBlocks,omitempty" yaml:"image-blocks,omitempty"`
}
type Pending struct {
	Key      string `json:"key" yaml:"key"`
	ParentID string `json:"parentId" yaml:"parent-id"`
	Title    string `json:"title" yaml:"title"`
	PageID   string `json:"pageId,omitempty" yaml:"page-id,omitempty"`
}
type Metadata struct {
	Version     int              `yaml:"version"`
	RootID      string           `yaml:"root-id"`
	Entry       *Entry           `yaml:"entry,omitempty"`
	Directories map[string]Entry `yaml:"directories,omitempty"`
	Pending     *Pending         `yaml:"pending,omitempty"`
}
type State struct {
	Version   int              `json:"version"`
	RootID    string           `json:"rootId"`
	Source    string           `json:"source"`
	Entries   map[string]Entry `json:"entries"`
	Pending   *Pending         `json:"pending,omitempty"`
	Documents map[string]*File `json:"-"`
	Active    map[string]bool  `json:"-"`
}

func loadState(c config.Config) (State, error) {
	return loadStateWithPending(c, false)
}
func loadStateWithPending(c config.Config, allowPending bool) (State, error) {
	s := State{Version: 1, RootID: config.FormatID(c.Notion.ParentPageID), Source: c.SourcePath, Entries: map[string]Entry{}, Documents: map[string]*File{}, Active: map[string]bool{}}
	if c.Notion.ParentDatabaseID != "" {
		s.RootID = config.FormatID(c.Notion.ParentDatabaseID)
	}
	files, err := Scan(c)
	if err != nil {
		return s, err
	}
	for i := range files {
		f := &files[i]
		s.Documents[f.Path] = f
		m := f.Metadata
		if m == nil {
			continue
		}
		if m.Version != 1 {
			return s, fmt.Errorf("%s: unsupported frontmatter state version", f.Path)
		}
		if norm(m.RootID) != norm(s.RootID) {
			return s, fmt.Errorf("%s: frontmatter state does not match configured parent (recorded: %s; requested: %s); use the original destination or reset the local metadata before syncing to a new destination; existing pages are not moved automatically", f.Path, m.RootID, s.RootID)
		}
		if m.Entry != nil {
			if f.PageID == "" || norm(f.PageID) != norm(m.Entry.PageID) {
				return s, fmt.Errorf("%s: notion-page-id does not match sync metadata", f.Path)
			}
			s.Entries["file:"+f.Path] = *m.Entry
		}
		for d, e := range m.Directories {
			if prior, ok := s.Entries["dir:"+d]; ok && !reflect.DeepEqual(prior, e) {
				return s, fmt.Errorf("conflicting directory metadata for %s", d)
			}
			s.Entries["dir:"+d] = e
		}
		if m.Pending != nil {
			if s.Pending != nil && !reflect.DeepEqual(s.Pending, m.Pending) {
				return s, fmt.Errorf("conflicting pending metadata")
			}
			s.Pending = m.Pending
		}
	}
	if s.Pending != nil && !allowPending {
		return s, fmt.Errorf("an earlier create for %s may have succeeded; inspect Notion and use recover --page-id or recover --not-created", s.Pending.Key)
	}
	return s, nil
}
func saveState(c config.Config, s State) error {
	names := make([]string, 0, len(s.Active))
	for name := range s.Active {
		names = append(names, name)
	}
	sort.Strings(names)
	pendingOwner := ""
	if s.Pending != nil {
		if after, ok := strings.CutPrefix(s.Pending.Key, "file:"); ok {
			pendingOwner = after
		} else {
			dir := strings.TrimPrefix(s.Pending.Key, "dir:")
			for _, name := range names {
				if strings.HasPrefix(name, dir+"/") {
					pendingOwner = name
					break
				}
			}
		}
		if pendingOwner == "" || s.Documents[pendingOwner] == nil {
			return fmt.Errorf("cannot persist pending state")
		}
	}
	for _, name := range names {
		f := s.Documents[name]
		if f == nil {
			return fmt.Errorf("missing source file %s", name)
		}
		m := Metadata{Version: 1, RootID: s.RootID, Directories: map[string]Entry{}}
		if e, ok := s.Entries["file:"+name]; ok {
			e2 := e
			m.Entry = &e2
		}
		for d := path.Dir(name); d != "." && d != "/"; d = path.Dir(d) {
			if e, ok := s.Entries["dir:"+d]; ok {
				m.Directories[d] = e
			}
		}
		if pendingOwner == name {
			pending := *s.Pending
			m.Pending = &pending
		}
		if m.Entry == nil && len(m.Directories) == 0 && m.Pending == nil && f.Metadata == nil {
			continue
		}
		if reflect.DeepEqual(f.Metadata, &m) {
			continue
		}
		if err := writeMetadata(c, f, &m); err != nil {
			return err
		}
	}
	return nil
}

func verifyFileUnchanged(c config.Config, f *File) error {
	current, err := os.ReadFile(filepath.Join(c.SourcePath, filepath.FromSlash(f.Path)))
	if err != nil {
		return err
	}
	if !bytes.Equal(current, f.Raw) {
		return fmt.Errorf("%s changed during sync; run diff again before retrying", f.Path)
	}
	return nil
}
func writeMetadata(c config.Config, f *File, m *Metadata) error {
	return writeMetadataBody(c, f, m, nil)
}

func writeMetadataBody(c config.Config, f *File, m *Metadata, replacement *string) error {
	filename := filepath.Join(c.SourcePath, filepath.FromSlash(f.Path))
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", f.Path)
	}
	current, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, f.Raw) {
		return fmt.Errorf("%s changed during sync; inspect Notion before retrying", f.Path)
	}
	root, body, nl, err := frontmatter(current)
	if err != nil {
		return err
	}
	if replacement != nil {
		body = *replacement
	}
	mapping := root.Content[0]
	if err := writeFlat(mapping, m); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	header := strings.TrimSuffix(buf.String(), "\n")
	if nl == "\r\n" {
		header = strings.ReplaceAll(header, "\n", nl)
	}
	result := []byte("---" + nl + header + nl + "---" + nl + body)
	if bytes.Equal(result, current) {
		f.Metadata = m
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(filename), ".notion-sync-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err = tmp.Chmod(info.Mode().Perm()); err == nil {
		_, err = tmp.Write(result)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmpName, filename); err != nil {
		return err
	}
	f.Raw = result
	f.Metadata = m
	f.RootNeedsFormatting = false
	if m.Entry != nil {
		f.PageID = m.Entry.PageID
	}
	return nil
}
func lock(c config.Config) (func(), error) {
	workDir, err := filepath.Abs(c.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	workDir, err = filepath.EvalSymlinks(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	digest := sha256.Sum256([]byte(workDir))
	name := filepath.Join(os.TempDir(), fmt.Sprintf("notion-sync-%x.lock", digest))
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil, &LockExistsError{Path: name}
		}
		return nil, fmt.Errorf("cannot acquire sync lock %s: %w", name, err)
	}
	if _, err := fmt.Fprintln(f, os.Getpid()); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return nil, fmt.Errorf("write sync lock: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return nil, fmt.Errorf("close sync lock: %w", err)
	}
	return func() { _ = os.Remove(name) }, nil
}
