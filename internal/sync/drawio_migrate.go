package sync

import (
	"context"
	"fmt"
	"html"
	"maps"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/yuyj109/notion-sync/internal/config"
)

type drawioEmbedBlock struct {
	Object, ID, Type string
	LastEditedTime   string `json:"last_edited_time"`
	InTrash          bool   `json:"in_trash"`
	Embed            struct {
		URL     string `json:"url"`
		Caption []any  `json:"caption"`
	} `json:"embed"`
}

type drawioMigration struct {
	Image  Image
	Before drawioEmbedBlock
}

func legacyDrawioURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" && strings.EqualFold(u.Hostname(), "viewer.diagrams.net")
}

func checkedDrawioEmbed(b drawioEmbedBlock, id string) error {
	if b.Object != "block" || b.Type != "embed" || !config.ValidID(b.ID) || norm(b.ID) != norm(id) || b.LastEditedTime == "" || b.InTrash {
		return fmt.Errorf("unexpected draw.io embed response")
	}
	return nil
}

func readDrawioEmbed(ctx context.Context, api Client, id string) (drawioEmbedBlock, error) {
	var b drawioEmbedBlock
	if err := api.API(ctx, "GET", "v1/blocks/"+id, nil, &b); err != nil {
		return b, err
	}
	return b, checkedDrawioEmbed(b, id)
}

// Only migrate verified, top-level managed draw.io blocks. Other embeds and
// existing file-backed HTML blocks keep their current contents.
func planDrawioMigrations(ctx context.Context, api Client, a Action) ([]drawioMigration, error) {
	if a.ForceReplace || len(a.Entry.managedEmbeds) == 0 {
		return nil, nil
	}
	m, err := validateExisting(ctx, api, a.Entry)
	if err != nil {
		return nil, err
	}
	_, media, safe, err := drawioMediaParts(ctx, api, a, m.Markdown)
	if err != nil || !safe {
		return nil, err
	}
	var migrations []drawioMigration
	for i, image := range a.File.Images {
		if !isDrawioSVG(image) {
			continue
		}
		src := embedSrc.FindStringSubmatch(media[i])
		if len(src) != 2 {
			return nil, fmt.Errorf("cannot recognize draw.io embed URL")
		}
		raw := html.UnescapeString(src[1][1 : len(src[1])-1])
		if !legacyDrawioURL(raw) {
			continue
		}
		id := a.Entry.ImageBlocks[imageStateKey(image)]
		b, err := readDrawioEmbed(ctx, api, id)
		if err != nil {
			return nil, err
		}
		if b.Embed.URL != raw {
			return nil, fmt.Errorf("draw.io embed changed while planning HTML migration")
		}
		migrations = append(migrations, drawioMigration{Image: image, Before: b})
	}
	return migrations, nil
}

func migratingDrawio(a Action, image Image) bool {
	for _, migration := range a.drawioMigrations {
		if imageStateKey(migration.Image) == imageStateKey(image) {
			return true
		}
	}
	return false
}

func applyDrawioMigrations(ctx context.Context, c config.Config, api Client, s *State, a *Action, print func(string, ...any)) error {
	uploader, ok := api.(MultipartClient)
	if !ok {
		return fmt.Errorf("file uploads are not supported by the Notion client")
	}
	for _, migration := range a.drawioMigrations {
		before := migration.Before
		image := migration.Image
		remote, err := validateExisting(ctx, api, a.Entry)
		if err != nil {
			return err
		}
		baseline, _, err := normalizeManagedEmbeds(ctx, api, a.Entry, remote.Markdown)
		if err != nil {
			return err
		}
		full := filepath.Join(c.SourcePath, filepath.FromSlash(image.Path))
		if err := verifyImageUnchanged(full, image.Hash); err != nil {
			return err
		}
		current, err := readDrawioEmbed(ctx, api, before.ID)
		if err != nil || !reflect.DeepEqual(before, current) {
			return fmt.Errorf("draw.io embed changed before HTML upload")
		}
		uploadID, err := uploadDrawioHTML(ctx, uploader, image, full)
		if err != nil {
			return fmt.Errorf("HTML upload outcome uncertain; inspect Notion before retry: %w", err)
		}
		if err := verifyImageUnchanged(full, image.Hash); err != nil {
			return err
		}
		if err := verifyFileUnchanged(c, s.Documents[a.Path]); err != nil {
			return err
		}
		if _, err := validateExisting(ctx, api, a.Entry); err != nil {
			return err
		}
		current, err = readDrawioEmbed(ctx, api, before.ID)
		if err != nil || !reflect.DeepEqual(before, current) {
			return fmt.Errorf("draw.io embed changed during HTML upload; inspect Notion")
		}
		body := map[string]any{"embed": map[string]any{"file_upload": map[string]any{"id": uploadID}}}
		var updated drawioEmbedBlock
		if err := api.API(ctx, "PATCH", "v1/blocks/"+before.ID, body, &updated); err != nil {
			return fmt.Errorf("HTML embed update outcome uncertain; inspect Notion before retry: %w", err)
		}
		if checkedDrawioEmbed(updated, before.ID) != nil || imageFileKey(updated.Embed.URL) == "" || legacyDrawioURL(updated.Embed.URL) || !reflect.DeepEqual(before.Embed.Caption, updated.Embed.Caption) {
			return fmt.Errorf("HTML embed update could not be verified; inspect Notion before retry")
		}
		actual, err := readDrawioEmbed(ctx, api, before.ID)
		if err != nil || checkedDrawioEmbed(actual, before.ID) != nil || actual.LastEditedTime != updated.LastEditedTime || imageFileKey(actual.Embed.URL) != imageFileKey(updated.Embed.URL) || !reflect.DeepEqual(actual.Embed.Caption, updated.Embed.Caption) {
			return fmt.Errorf("HTML embed changed or could not be verified after update; inspect Notion")
		}
		after, err := getMarkdown(ctx, api, a.Entry.PageID)
		if err != nil {
			return fmt.Errorf("HTML updated but page readback failed; inspect Notion: %w", err)
		}
		normalized, _, err := normalizeManagedEmbeds(ctx, api, a.Entry, after.Markdown)
		if err != nil || remoteMarkdownHash(normalized) != remoteMarkdownHash(baseline) {
			return fmt.Errorf("other page content changed during HTML migration; inspect Notion")
		}
		_, media, safe, err := drawioMediaParts(ctx, api, *a, after.Markdown)
		if err != nil || !safe {
			return fmt.Errorf("HTML embed layout could not be verified; inspect Notion")
		}
		matched := false
		for i, ref := range a.File.Images {
			if imageStateKey(ref) != imageStateKey(image) {
				continue
			}
			src := embedSrc.FindStringSubmatch(media[i])
			if len(src) == 2 {
				raw := html.UnescapeString(src[1][1 : len(src[1])-1])
				matched = imageFileKey(raw) == imageFileKey(actual.Embed.URL)
			}
		}
		if !matched {
			return fmt.Errorf("HTML embed cannot be verified in page Markdown; inspect Notion")
		}
		a.Entry.RemoteHash = remoteMarkdownHash(normalized)
		// Do not claim changes to other media or to the Markdown body.
		a.Entry.ImageHashes = maps.Clone(a.Entry.ImageHashes)
		a.Entry.ImageHashes[imageStateKey(image)] = imageSyncHash(image)
		s.Entries[a.Key] = a.Entry
		if err := saveState(c, *s); err != nil {
			return err
		}
		print("updated draw.io HTML: %s (%s; existing block retained)", a.Path, image.Path)
	}
	return nil
}
