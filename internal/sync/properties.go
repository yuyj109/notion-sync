package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/yuyj109/notion-sync/internal/config"
	"gopkg.in/yaml.v3"
)

type propertyPlan struct {
	write     map[string]any
	kinds     map[string]string
	before    map[string]any
	beforeRaw map[string]json.RawMessage
	wanted    map[string]any
	local     map[string]string
	preview   []string
}

// propertyInput converts a frontmatter value to the Notion API write format
// and a comparable representation. Arrays are complete replacements.
func propertyInput(kind string, value any) (any, any, error) {
	str := func() (string, error) {
		s, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("expected a string")
		}
		return s, nil
	}
	var write, comparable any
	switch kind {
	case "rich_text":
		if value == nil {
			value = ""
		}
		s, err := str()
		if err != nil {
			return nil, nil, err
		}
		if len([]rune(s)) > 2000 {
			return nil, nil, fmt.Errorf("rich text exceeds 2000 characters")
		}
		comparable = s
		write = []any{}
		if s != "" {
			write = []any{map[string]any{"text": map[string]any{"content": s}}}
		}
	case "select", "status":
		if value == nil {
			break
		}
		s, err := str()
		if err != nil {
			return nil, nil, err
		}
		if s != "" {
			write = map[string]any{"name": s}
			comparable = s
		}
	case "url", "email", "phone_number":
		if value == nil {
			break
		}
		s, err := str()
		if err != nil {
			return nil, nil, err
		}
		if s != "" {
			write, comparable = s, s
		}
	case "checkbox":
		var b bool
		switch v := value.(type) {
		case bool:
			b = v
		case string:
			var err error
			b, err = strconv.ParseBool(v)
			if err != nil {
				return nil, nil, fmt.Errorf("expected true or false")
			}
		default:
			return nil, nil, fmt.Errorf("expected true or false")
		}
		write, comparable = b, b
	case "number":
		if value == nil || value == "" {
			break
		}
		v, err := strconv.ParseFloat(fmt.Sprint(value), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, nil, fmt.Errorf("expected a number")
		}
		write, comparable = v, v
	case "date":
		if value == nil || value == "" {
			break
		}
		s, err := str()
		if err != nil {
			return nil, nil, err
		}
		write, comparable = map[string]any{"start": s}, s
	case "multi_select":
		items := []string{}
		switch v := value.(type) {
		case string:
			if v != "" {
				for x := range strings.SplitSeq(v, ",") {
					items = append(items, strings.TrimSpace(x))
				}
			}
		case []any:
			for _, x := range v {
				s, ok := x.(string)
				if !ok {
					return nil, nil, fmt.Errorf("expected a list of strings")
				}
				items = append(items, s)
			}
		case nil:
		default:
			return nil, nil, fmt.Errorf("expected a list of strings or comma-separated string")
		}
		seen := map[string]bool{}
		out := make([]any, 0, len(items))
		for _, s := range items {
			if s == "" || seen[s] {
				return nil, nil, fmt.Errorf("empty or duplicate option")
			}
			seen[s] = true
			out = append(out, map[string]any{"name": s})
		}
		sort.Strings(items)
		write, comparable = out, items
	default:
		return nil, nil, fmt.Errorf("unsupported or read-only property type %q", kind)
	}
	return map[string]any{kind: write}, comparable, nil
}

func remoteProperty(raw json.RawMessage, kind string) (any, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("property missing from page response")
	}
	var value struct {
		Type  string                     `json:"type"`
		Value map[string]json.RawMessage `json:"-"`
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fields["type"], &value.Type); err != nil || value.Type != kind {
		return nil, fmt.Errorf("property type changed or is missing")
	}
	v, ok := fields[kind]
	if !ok {
		return nil, fmt.Errorf("property value missing")
	}
	if bytes.Equal(v, []byte("null")) {
		return nil, nil
	}
	switch kind {
	case "select", "status":
		var x struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(v, &x); err != nil {
			return nil, err
		}
		return x.Name, nil
	case "date":
		var x struct {
			Start    string  `json:"start"`
			End      *string `json:"end"`
			TimeZone *string `json:"time_zone"`
		}
		if err := json.Unmarshal(v, &x); err != nil {
			return nil, err
		}
		if x.End != nil || x.TimeZone != nil {
			return nil, fmt.Errorf("date range or time zone requires manual handling")
		}
		return x.Start, nil
	case "multi_select":
		var x []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(v, &x); err != nil {
			return nil, err
		}
		names := make([]string, 0, len(x))
		for _, i := range x {
			names = append(names, i.Name)
		}
		sort.Strings(names)
		return names, nil
	case "rich_text":
		var x []struct {
			PlainText string `json:"plain_text"`
		}
		if err := json.Unmarshal(v, &x); err != nil {
			return nil, err
		}
		if len(x) >= 25 {
			return nil, fmt.Errorf("rich text may be truncated")
		}
		var s strings.Builder
		for _, i := range x {
			s.WriteString(i.PlainText)
		}
		return s.String(), nil
	default:
		var x any
		if err := json.Unmarshal(v, &x); err != nil {
			return nil, err
		}
		return x, nil
	}
}

func planProperties(ctx context.Context, c config.Config, api Client, p *Plan, skipped map[string]bool, progress func(string, ...any)) error {
	type schema struct {
		Object, ID string
		Properties map[string]struct {
			Type   string `json:"type"`
			Select struct {
				Options []struct {
					Name string `json:"name"`
				} `json:"options"`
			} `json:"select"`
			Status struct {
				Options []struct {
					Name string `json:"name"`
				} `json:"options"`
			} `json:"status"`
			MultiSelect struct {
				Options []struct {
					Name string `json:"name"`
				} `json:"options"`
			} `json:"multi_select"`
		} `json:"properties"`
	}
	schemas := map[string]schema{}
	for i := range p.Actions {
		a := &p.Actions[i]
		if !strings.HasPrefix(a.Key, "file:") || skipped[a.Key] {
			continue
		}
		values := map[string]any{}
		maps.Copy(values, a.File.Properties)
		for k, v := range c.Sync.Properties {
			values[k] = v
		}
		if len(values) == 0 {
			continue
		}
		if progress != nil {
			progress("checking database properties: %s", a.Path)
		}
		parent, kind := a.ParentID, a.ParentType
		if a.Entry.PageID != "" {
			parent, kind = a.Entry.ParentID, a.Entry.ParentType
		}
		if kind != "data_source_id" {
			return fmt.Errorf("%s: notion-properties require a database destination", a.Path)
		}
		key := norm(parent)
		sc, ok := schemas[key]
		if !ok {
			if err := api.API(ctx, "GET", "v1/data_sources/"+parent, nil, &sc); err != nil {
				return fmt.Errorf("%s: read database schema: %w", a.Path, err)
			}
			if sc.Object != "data_source" || norm(sc.ID) != key {
				return fmt.Errorf("%s: unexpected database schema response", a.Path)
			}
			schemas[key] = sc
		}
		plan := &propertyPlan{write: map[string]any{}, kinds: map[string]string{}, before: map[string]any{}, beforeRaw: map[string]json.RawMessage{}, wanted: map[string]any{}, local: map[string]string{}}
		var current page
		if a.Entry.PageID != "" {
			var err error
			current, err = getPage(ctx, api, a.Entry.PageID)
			if err != nil {
				return fmt.Errorf("%s: read page properties: %w", a.Path, err)
			}
			id, t := parentOf(current)
			if t != kind || norm(id) != key {
				return fmt.Errorf("%s: page was moved", a.Path)
			}
		}
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			prop, ok := sc.Properties[name]
			if !ok {
				return fmt.Errorf("%s: database property %q does not exist", a.Path, name)
			}
			write, wanted, err := propertyInput(prop.Type, values[name])
			if err != nil {
				return fmt.Errorf("%s: property %q: %w", a.Path, name, err)
			}
			options := map[string]bool{}
			switch prop.Type {
			case "select":
				for _, o := range prop.Select.Options {
					options[o.Name] = true
				}
			case "status":
				for _, o := range prop.Status.Options {
					options[o.Name] = true
				}
			case "multi_select":
				for _, o := range prop.MultiSelect.Options {
					options[o.Name] = true
				}
			}
			var requested []string
			switch v := wanted.(type) {
			case string:
				if prop.Type == "select" || prop.Type == "status" {
					requested = []string{v}
				}
			case []string:
				requested = v
			}
			for _, option := range requested {
				if !options[option] {
					return fmt.Errorf("%s: property %q option %q does not exist in database schema", a.Path, name, option)
				}
			}
			plan.kinds[name], plan.wanted[name] = prop.Type, wanted
			if input, exists := c.Sync.Properties[name]; exists {
				if old, exists := a.File.Properties[name]; !exists || !reflect.DeepEqual(old, input) {
					plan.local[name] = input
					plan.preview = append(plan.preview, fmt.Sprintf("frontmatter %s = %q", name, input))
				}
			}
			if a.Entry.PageID != "" {
				before, err := remoteProperty(current.Properties[name], prop.Type)
				if err != nil {
					return fmt.Errorf("%s: property %q: %w", a.Path, name, err)
				}
				plan.before[name] = before
				plan.beforeRaw[name] = append(json.RawMessage(nil), current.Properties[name]...)
				if reflect.DeepEqual(before, wanted) {
					continue
				}
			}
			plan.write[name] = write
			if a.Entry.PageID != "" {
				plan.preview = append(plan.preview, fmt.Sprintf("database property %s: %v -> %v", name, plan.before[name], values[name]))
			} else {
				plan.preview = append(plan.preview, fmt.Sprintf("set database property %s = %v", name, values[name]))
			}
		}
		a.propertyPlan = plan
	}
	return nil
}

func writePropertyFrontmatter(c config.Config, f *File, updates map[string]string) error {
	if len(updates) == 0 {
		return nil
	}
	filename := filepath.Join(c.SourcePath, filepath.FromSlash(f.Path))
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", f.Path)
	}
	if err = verifyFileUnchanged(c, f); err != nil {
		return err
	}
	root, body, nl, err := yamlFrontmatter(f.Raw)
	if err != nil {
		return err
	}
	mapping := root.Content[0]
	var props *yaml.Node
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "notion-properties" {
			props = mapping.Content[i+1]
			break
		}
	}
	if props == nil {
		props = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "notion-properties"}, props)
	}
	if props.Kind != yaml.MappingNode {
		return fmt.Errorf("notion-properties must be a mapping")
	}
	names := make([]string, 0, len(updates))
	for k := range updates {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: updates[k]}
		found := false
		for i := 0; i < len(props.Content); i += 2 {
			if props.Content[i].Value == k {
				props.Content[i+1] = v
				found = true
				break
			}
		}
		if !found {
			props.Content = append(props.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, v)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err = enc.Encode(root); err != nil {
		return err
	}
	if err = enc.Close(); err != nil {
		return err
	}
	header := strings.ReplaceAll(strings.TrimSuffix(buf.String(), "\n"), "\n", nl)
	result := []byte("---" + nl + header + nl + "---" + nl + body)
	tmp, err := os.CreateTemp(filepath.Dir(filename), ".notion-sync-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
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
	if err = replaceUnchangedFile(filename, tmp.Name(), f.Raw, info); err != nil {
		return err
	}
	f.Raw = result
	if f.Properties == nil {
		f.Properties = map[string]any{}
	}
	for k, v := range updates {
		f.Properties[k] = v
	}
	return nil
}

func applyProperties(ctx context.Context, c config.Config, api Client, s *State, a Action, print func(string, ...any)) error {
	p := a.propertyPlan
	if err := writePropertyFrontmatter(c, s.Documents[a.Path], p.local); err != nil {
		return fmt.Errorf("%s: write frontmatter: %w", a.Path, err)
	}
	if len(p.local) > 0 {
		print("updated frontmatter: %s", a.Path)
	}
	if a.Entry.PageID == "" || len(p.write) == 0 {
		return nil
	}
	current, err := getPage(ctx, api, a.Entry.PageID)
	if err != nil {
		return err
	}
	id, kind := parentOf(current)
	if kind != a.Entry.ParentType || norm(id) != norm(a.Entry.ParentID) {
		return fmt.Errorf("%s: page moved before property update", a.Path)
	}
	for name := range p.write {
		value, err := remoteProperty(current.Properties[name], p.kinds[name])
		if err != nil {
			return fmt.Errorf("%s: property %q: %w", a.Path, name, err)
		}
		if !reflect.DeepEqual(value, p.before[name]) {
			return fmt.Errorf("%s: property %q changed since planning; review and retry", a.Path, name)
		}
		var oldRaw, newRaw any
		if json.Unmarshal(p.beforeRaw[name], &oldRaw) != nil || json.Unmarshal(current.Properties[name], &newRaw) != nil || !reflect.DeepEqual(oldRaw, newRaw) {
			return fmt.Errorf("%s: property %q changed since planning; review and retry", a.Path, name)
		}
	}
	var result page
	if err = api.API(ctx, "PATCH", "v1/pages/"+a.Entry.PageID, map[string]any{"properties": p.write}, &result); err != nil {
		return fmt.Errorf("%s: property update outcome uncertain; inspect Notion before retrying: %w", a.Path, err)
	}
	if result.Object != "page" || norm(result.ID) != norm(a.Entry.PageID) {
		return fmt.Errorf("%s: unexpected property update response; inspect Notion", a.Path)
	}
	for name := range p.write {
		value, err := remoteProperty(result.Properties[name], p.kinds[name])
		if err != nil || !reflect.DeepEqual(value, p.wanted[name]) {
			return fmt.Errorf("%s: property %q update could not be verified; inspect Notion", a.Path, name)
		}
	}
	print("updated database properties: %s", a.Path)
	return nil
}
