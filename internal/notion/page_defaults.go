package notion

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
)

// These native icon names were checked against Notion's public gray SVG set.
var randomIcons = [...]string{"document", "pizza", "airplane", "rocket", "book", "star", "calendar", "heart", "leaf", "globe", "folder", "home", "flag", "camera", "coffee", "music"}

// StableRandomIcon assigns the same icon to the same synchronization target.
func StableRandomIcon(key string) string {
	sum := sha256.Sum256([]byte(key))
	return randomIcons[binary.BigEndian.Uint64(sum[:8])%uint64(len(randomIcons))]
}

// RandomIcon selects an icon for an unmanaged page, which has no stable key.
func RandomIcon() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(randomIcons))))
	if err != nil {
		return "", fmt.Errorf("choose random icon: %w", err)
	}
	return randomIcons[n.Int64()], nil
}

// NativeIcon returns a native Notion icon in its default color.
func NativeIcon(name string) map[string]any {
	return map[string]any{"type": "icon", "icon": map[string]any{"name": name}}
}

type Icon struct {
	Type string `json:"type"`
	Icon struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"icon"`
}

func iconKey(s string) string {
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(s))
}

func (i *Icon) Matches(name string) bool {
	return i != nil && i.Type == "icon" && iconKey(i.Icon.Name) == iconKey(name) && i.Icon.Color == "gray"
}

func SetIcon(ctx context.Context, api interface {
	API(context.Context, string, string, any, any) error
}, id, name string) error {
	var result struct {
		Object string `json:"object"`
		ID     string `json:"id"`
		Icon   *Icon  `json:"icon"`
	}
	if err := api.API(ctx, "PATCH", "v1/pages/"+id, map[string]any{"icon": NativeIcon(name)}, &result); err != nil {
		return err
	}
	if result.Object != "page" || result.ID != id || result.Icon == nil || result.Icon.Type != "icon" || result.Icon.Icon.Name == "" || result.Icon.Icon.Color != "gray" {
		return fmt.Errorf("unexpected icon update response")
	}
	return nil
}

// LockPage enables the Notion UI lock without restricting API updates.
func LockPage(ctx context.Context, api interface {
	API(context.Context, string, string, any, any) error
}, id string) error {
	var result struct {
		Object string `json:"object"`
		ID     string `json:"id"`
		Locked *bool  `json:"is_locked"`
	}
	if err := api.API(ctx, "PATCH", "v1/pages/"+id, map[string]any{"is_locked": true}, &result); err != nil {
		return err
	}
	if result.Object != "page" || result.ID != id || result.Locked == nil || !*result.Locked {
		return fmt.Errorf("unexpected lock response")
	}
	return nil
}
