package sync

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/yuyj109/notion-sync/internal/config"
	"gopkg.in/yaml.v3"
)

const dirKeyPrefix = "notion-directory."

var flatKeys = map[string]bool{"notion-root-id": true, "notion-parent": true, "notion-target": true, "notion-local-hash": true, "notion-remote-hash": true, "notion-pending-key": true, "notion-pending-parent-id": true, "notion-pending-title": true, "notion-pending-page-id": true, "notion-images": true}

func isFlatKey(key string) bool { return flatKeys[key] || strings.HasPrefix(key, dirKeyPrefix) }
func readFlat(node *yaml.Node, f *File) error {
	values := map[string]string{}
	imageHashes := map[string]string{}
	imageBlocks := map[string]string{}
	hasFlat := false
	for i := 0; i < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		if !isFlatKey(k.Value) {
			continue
		}
		hasFlat = true
		if k.Value == "notion-images" {
			if v.Kind != yaml.MappingNode {
				return fmt.Errorf("%s must be a mapping", k.Value)
			}
			for j := 0; j < len(v.Content); j += 2 {
				name, fields := v.Content[j], v.Content[j+1]
				if name.Kind != yaml.ScalarNode || name.Tag != "!!str" || fields.Kind != yaml.MappingNode {
					return fmt.Errorf("invalid notion-images entry")
				}
				if _, ok := imageHashes[name.Value]; ok {
					return fmt.Errorf("duplicate notion-images entry %q", name.Value)
				}
				seenFields := map[string]bool{}
				for fieldIndex := 0; fieldIndex < len(fields.Content); fieldIndex += 2 {
					field, value := fields.Content[fieldIndex], fields.Content[fieldIndex+1]
					if field.Kind != yaml.ScalarNode || field.Tag != "!!str" || value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
						return fmt.Errorf("invalid notion-images entry %q", name.Value)
					}
					if seenFields[field.Value] {
						return fmt.Errorf("duplicate notion-images field %q in %q", field.Value, name.Value)
					}
					seenFields[field.Value] = true
					switch field.Value {
					case "hash":
						if len(value.Value) != 64 {
							return fmt.Errorf("invalid notion-images hash")
						}
						if _, err := hex.DecodeString(value.Value); err != nil {
							return fmt.Errorf("invalid notion-images hash")
						}
						imageHashes[name.Value] = value.Value
					case "block-id":
						if !config.ValidID(value.Value) {
							return fmt.Errorf("invalid notion-images block ID")
						}
						imageBlocks[name.Value] = config.FormatID(value.Value)
					default:
						return fmt.Errorf("unknown notion-images field %q", field.Value)
					}
				}
				if imageHashes[name.Value] == "" {
					return fmt.Errorf("notion-images entry %q is missing hash", name.Value)
				}
			}
			continue
		}
		if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
			return fmt.Errorf("%s must be a string", k.Value)
		}
		values[k.Value] = v.Value
	}
	if !hasFlat {
		return nil
	}
	root := values["notion-root-id"]
	if !config.ValidID(root) {
		return fmt.Errorf("invalid notion-root-id")
	}
	f.RootNeedsFormatting = root != config.FormatID(root)
	root = config.FormatID(root)
	m := &Metadata{Version: 1, RootID: root, Directories: map[string]Entry{}}
	for key, value := range values {
		if !strings.HasPrefix(key, dirKeyPrefix) {
			continue
		}
		dir, err := url.PathUnescape(strings.TrimPrefix(key, dirKeyPrefix))
		if err != nil || dir == "." || dir == "" || strings.HasPrefix(dir, "/") || strings.HasPrefix(dir, "../") || path.Clean(dir) != dir || !config.ValidID(value) {
			return fmt.Errorf("invalid directory metadata %q", key)
		}
		m.Directories[dir] = Entry{PageID: config.FormatID(value)}
	}
	for dir, e := range m.Directories {
		p := path.Dir(dir)
		parent := root
		if p != "." {
			ancestor, ok := m.Directories[p]
			if !ok {
				return fmt.Errorf("missing directory metadata for %s", p)
			}
			parent = ancestor.PageID
		}
		e.ParentID = parent
		e.ParentType = "page_id"
		e.Target = "dir:" + p
		m.Directories[dir] = e
	}
	_, hasLocal := values["notion-local-hash"]
	_, hasRemote := values["notion-remote-hash"]
	_, hasParent := values["notion-parent"]
	_, hasTarget := values["notion-target"]
	if hasLocal || hasRemote || hasParent || hasTarget {
		if f.PageID == "" {
			return fmt.Errorf("sync metadata requires notion-page-id")
		}
		e := Entry{PageID: f.PageID, ParentID: root, ParentType: "page_id", Target: "hierarchy:", LocalHash: values["notion-local-hash"], RemoteHash: values["notion-remote-hash"]}
		e.ImageHashes = imageHashes
		e.ImageBlocks = imageBlocks
		if hasTarget {
			e.Target = values["notion-target"]
			if e.Target == "page" {
				e.Target = "page:" + norm(f.PageID)
			} else if kind, id, ok := strings.Cut(e.Target, ":"); ok && (kind == "parent" || kind == "database") && config.ValidID(id) {
				e.Target = kind + ":" + norm(id)
			}
		} else if f.ParentID != "" {
			e.Target = "parent:" + norm(f.ParentID)
		} else if f.DatabaseID != "" {
			e.Target = "database:" + norm(f.DatabaseID)
		}
		if hasParent {
			kind, id, ok := strings.Cut(values["notion-parent"], ":")
			if !ok || !config.ValidID(id) || (kind != "page_id" && kind != "data_source_id") {
				return fmt.Errorf("invalid notion-parent")
			}
			e.ParentType, e.ParentID = kind, config.FormatID(id)
		} else if after, ok := strings.CutPrefix(e.Target, "hierarchy:"); ok {
			dir := after
			if dir != "" {
				parent, ok := m.Directories[dir]
				if !ok {
					return fmt.Errorf("missing directory metadata for %s", dir)
				}
				e.ParentID = parent.PageID
			}
		} else if after, ok := strings.CutPrefix(e.Target, "parent:"); ok {
			e.ParentID = after
			if !config.ValidID(e.ParentID) {
				return fmt.Errorf("invalid parent target")
			}
		} else if strings.HasPrefix(e.Target, "database:") {
			return fmt.Errorf("database target requires notion-parent")
		}
		m.Entry = &e
	}
	pendingKey, hasPending := values["notion-pending-key"]
	if hasPending {
		m.Pending = &Pending{Key: pendingKey, ParentID: config.FormatID(values["notion-pending-parent-id"]), Title: values["notion-pending-title"], PageID: config.FormatID(values["notion-pending-page-id"])}
		if pendingKey == "" || !config.ValidID(m.Pending.ParentID) || m.Pending.Title == "" || m.Pending.PageID != "" && !config.ValidID(m.Pending.PageID) {
			return fmt.Errorf("invalid pending sync metadata")
		}
	} else if values["notion-pending-parent-id"] != "" || values["notion-pending-title"] != "" || values["notion-pending-page-id"] != "" {
		return fmt.Errorf("incomplete pending sync metadata")
	}
	f.Metadata = m
	return nil
}
func writeFlat(mapping *yaml.Node, m *Metadata) error {
	content := mapping.Content[:0]
	for i := 0; i < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		if isFlatKey(k.Value) {
			continue
		}
		content = append(content, mapping.Content[i], mapping.Content[i+1])
	}
	mapping.Content = content
	set := func(key, value string) {
		if value == "" {
			return
		}
		for i := 0; i < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				mapping.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
				return
			}
		}
		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	}
	set("notion-root-id", m.RootID)
	if e := m.Entry; e != nil {
		set("notion-page-id", config.FormatID(e.PageID))
		defaultTarget := "hierarchy:"
		for i := 0; i < len(mapping.Content); i += 2 {
			key, value := mapping.Content[i].Value, mapping.Content[i+1].Value
			if key == "notion-parent-page-id" {
				defaultTarget = "parent:" + norm(value)
			}
			if key == "notion-database-id" {
				defaultTarget = "database:" + norm(value)
			}
		}
		derivedParent := m.RootID
		derivedType := "page_id"
		if after, ok := strings.CutPrefix(e.Target, "hierarchy:"); ok {
			dir := after
			if dir != "" {
				p, ok := m.Directories[dir]
				if !ok {
					return fmt.Errorf("missing directory metadata for %s", dir)
				}
				derivedParent = p.PageID
			}
		} else if after, ok := strings.CutPrefix(e.Target, "parent:"); ok {
			derivedParent = after
		} else if strings.HasPrefix(e.Target, "database:") {
			derivedType = "data_source_id"
			derivedParent = ""
		}
		if e.ParentType != derivedType || norm(e.ParentID) != norm(derivedParent) {
			set("notion-parent", e.ParentType+":"+config.FormatID(e.ParentID))
		}
		if e.Target != defaultTarget {
			target := e.Target
			if target == "page:"+norm(e.PageID) {
				target = "page"
			} else if kind, id, ok := strings.Cut(target, ":"); ok && (kind == "parent" || kind == "database") && config.ValidID(id) {
				target = kind + ":" + config.FormatID(id)
			}
			set("notion-target", target)
		}
		set("notion-local-hash", e.LocalHash)
		set("notion-remote-hash", e.RemoteHash)
		imageKeys := map[string]bool{}
		for key := range e.ImageHashes {
			imageKeys[key] = true
		}
		for key := range e.ImageBlocks {
			imageKeys[key] = true
		}
		if len(imageKeys) > 0 {
			keys := make([]string, 0, len(imageKeys))
			for key := range imageKeys {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			items := make([]*yaml.Node, 0, len(keys))
			for _, key := range keys {
				fields := []*yaml.Node{}
				if hash := e.ImageHashes[key]; hash != "" {
					fields = append(fields, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "hash"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: hash})
				}
				if blockID := e.ImageBlocks[key]; blockID != "" {
					fields = append(fields, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "block-id"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: config.FormatID(blockID)})
				}
				items = append(items, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: fields})
			}
			mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "notion-images"}, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: items})
		}
	}
	dirs := make([]string, 0, len(m.Directories))
	for d := range m.Directories {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		e := m.Directories[d]
		p := path.Dir(d)
		parent := m.RootID
		if p != "." {
			a, ok := m.Directories[p]
			if !ok {
				return fmt.Errorf("missing directory metadata for %s", p)
			}
			parent = a.PageID
		}
		if e.ParentType != "page_id" || norm(e.ParentID) != norm(parent) || e.Target != "dir:"+p {
			return fmt.Errorf("invalid directory metadata for %s", d)
		}
		set(dirKeyPrefix+url.PathEscape(d), config.FormatID(e.PageID))
	}
	if p := m.Pending; p != nil {
		set("notion-pending-key", p.Key)
		set("notion-pending-parent-id", config.FormatID(p.ParentID))
		set("notion-pending-title", p.Title)
		set("notion-pending-page-id", config.FormatID(p.PageID))
	}
	return nil
}
