package sync

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuyj109/notion-sync/internal/config"
	"gopkg.in/yaml.v3"
)

type resetFile struct {
	name          string
	before, after []byte
	mode          os.FileMode
}

// Reset removes Notion destination and sync metadata only from the specified local files.
func Reset(target string, dryRun bool, print func(string, ...any)) error {
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("target must not be a symlink")
	}
	if !dryRun {
		unlock, err := lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	var paths []string
	switch {
	case info.IsDir():
		err = filepath.WalkDir(target, func(name string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.Type().IsRegular() && strings.EqualFold(filepath.Ext(name), ".md") {
				paths = append(paths, name)
			}
			return nil
		})
		if err != nil {
			return err
		}
	case info.Mode().IsRegular() && strings.EqualFold(filepath.Ext(target), ".md"):
		paths = append(paths, target)
	default:
		return fmt.Errorf("target must be a Markdown file or directory")
	}
	sort.Strings(paths)
	if err := rejectSharedPendingReset(paths); err != nil {
		return err
	}
	var changes []resetFile
	for _, name := range paths {
		f, err := prepareReset(name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if !bytes.Equal(f.before, f.after) {
			changes = append(changes, f)
		}
	}
	if dryRun {
		for _, f := range changes {
			print("would clear %s", f.name)
		}
		return nil
	}
	for _, f := range changes {
		if err := writeReset(f); err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		print("cleared %s", f.name)
	}
	return nil
}

// A pending journal can exist without Markdown metadata if a write was
// interrupted. Check every possible source root containing a selected file.
func rejectSharedPendingReset(paths []string) error {
	selected := map[string]bool{}
	roots := map[string]bool{}
	for _, name := range paths {
		resolved, err := filepath.EvalSymlinks(name)
		if err != nil {
			return err
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return err
		}
		selected[resolved] = true
		for dir := filepath.Dir(resolved); ; dir = filepath.Dir(dir) {
			roots[dir] = true
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	for root := range roots {
		dir, err := sharedStateDir(root)
		if err != nil {
			return fmt.Errorf("locate shared sync state: %w", err)
		}
		for _, stateDir := range []string{dir, filepath.Join(root, ".notion-sync")} {
			entries, err := os.ReadDir(stateDir)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("inspect shared sync state: %w", err)
			}
			for _, entry := range entries {
				id, ok := strings.CutPrefix(entry.Name(), "state-")
				if !ok || !strings.HasSuffix(id, ".json") {
					continue
				}
				id = strings.TrimSuffix(id, ".json")
				if !config.ValidID(id) || id != norm(id) || !entry.Type().IsRegular() {
					return fmt.Errorf("invalid shared sync state file %s", entry.Name())
				}
				c := config.Config{SourcePath: root}
				s := State{RootID: id, Entries: map[string]Entry{}}
				if err := loadSharedState(c, &s); err != nil {
					return err
				}
				if s.Pending != nil && selected[filepath.Join(root, filepath.FromSlash(s.PendingOwner))] {
					return fmt.Errorf("%s: pending creation must be resolved with recover before clearing metadata", s.PendingOwner)
				}
			}
		}
	}
	return nil
}

func prepareReset(name string) (resetFile, error) {
	f := resetFile{name: name}
	info, err := os.Lstat(name)
	if err != nil {
		return f, err
	}
	if !info.Mode().IsRegular() {
		return f, fmt.Errorf("not a regular file")
	}
	f.mode = info.Mode().Perm()
	f.before, err = os.ReadFile(name)
	if err != nil {
		return f, err
	}
	parsed, err := parse(f.before)
	if err != nil {
		return f, err
	}
	if parsed.Metadata != nil && parsed.Metadata.Pending != nil {
		return f, fmt.Errorf("pending creation must be resolved with recover before clearing metadata")
	}
	root, body, nl, err := frontmatter(f.before)
	if err != nil {
		return f, err
	}
	mapping := root.Content[0]
	content := make([]*yaml.Node, 0, len(mapping.Content))
	for i := 0; i < len(mapping.Content); i += 2 {
		key := mapping.Content[i].Value
		if key == "notion-page-id" || key == "notion-parent-page-id" || key == "notion-database-id" || isFlatKey(key) {
			continue
		}
		content = append(content, mapping.Content[i], mapping.Content[i+1])
	}
	if len(content) == len(mapping.Content) {
		f.after = f.before
		return f, nil
	}
	if len(content) == 0 {
		f.after = []byte(body)
		return f, nil
	}
	mapping.Content = content
	if parsed.PropertiesFormat == "comment" {
		f.after, err = renderMetadata(root, body, nl, "comment", f.before)
		if err != nil {
			return f, err
		}
		if _, err := parse(f.after); err != nil {
			return f, fmt.Errorf("remaining comment would be invalid: %w", err)
		}
		return f, nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return f, err
	}
	if err := enc.Close(); err != nil {
		return f, err
	}
	header := strings.TrimSuffix(buf.String(), "\n")
	if nl == "\r\n" {
		header = strings.ReplaceAll(header, "\n", nl)
	}
	f.after = []byte("---" + nl + header + nl + "---" + nl + body)
	if _, err := parse(f.after); err != nil {
		return f, fmt.Errorf("remaining frontmatter would be invalid: %w", err)
	}
	return f, nil
}

func writeReset(f resetFile) error {
	info, err := os.Lstat(f.name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	current, err := os.ReadFile(f.name)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, f.before) {
		return fmt.Errorf("file changed while clearing metadata")
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.name), ".notion-sync-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err = tmp.Chmod(f.mode); err == nil {
		_, err = tmp.Write(f.after)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceUnchangedFile(f.name, tmp.Name(), f.before, info)
}
