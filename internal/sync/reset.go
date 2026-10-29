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
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	var c config.Config
	c.WorkDir = cwd
	if !dryRun {
		unlock, err := lock(c)
		if err != nil {
			return err
		}
		defer unlock()
	}
	var paths []string
	if info.IsDir() {
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
	} else if info.Mode().IsRegular() && strings.EqualFold(filepath.Ext(target), ".md") {
		paths = append(paths, target)
	} else {
		return fmt.Errorf("target must be a Markdown file or directory")
	}
	sort.Strings(paths)
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
	return os.Rename(tmp.Name(), f.name)
}
