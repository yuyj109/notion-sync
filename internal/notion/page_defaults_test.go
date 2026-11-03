package notion

import "testing"

func TestRandomIconsAreNativeAndStableForSync(t *testing.T) {
	allowed := map[string]bool{}
	for _, name := range randomIcons {
		allowed[name] = true
	}
	first := StableRandomIcon("root:file:a.md")
	if !allowed[first] || StableRandomIcon("root:file:a.md") != first {
		t.Fatalf("unstable or unknown icon: %q", first)
	}
	for range 20 {
		name, err := RandomIcon()
		if err != nil || !allowed[name] {
			t.Fatalf("invalid random icon: %q, %v", name, err)
		}
	}
}
