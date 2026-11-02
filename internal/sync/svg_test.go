package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type inspectingUploader struct{ data []byte }

func (u *inspectingUploader) Upload(_ context.Context, _, _, path string) (string, error) {
	var err error
	u.data, err = os.ReadFile(path)
	return "uploaded", err
}

func TestUploadDrawioSVGRemovesOnlyEmbeddedDiagram(t *testing.T) {
	source := `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" data-note="content='keep'" content="&lt;mxfile&gt;&lt;diagram/&gt;&lt;/mxfile&gt;"><text>hello</text></svg>`
	want := `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" data-note="content='keep'"><text>hello</text></svg>`
	filename := filepath.Join(t.TempDir(), "drawing.svg")
	if err := os.WriteFile(filename, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	u := &inspectingUploader{}
	image := Image{Name: "drawing.svg", ContentType: "image/svg+xml", Hash: hash(source)}
	if _, err := uploadImage(context.Background(), u, image, filename); err != nil {
		t.Fatal(err)
	}
	if string(u.data) != want {
		t.Fatalf("uploaded SVG = %s", u.data)
	}
	local, err := os.ReadFile(filename)
	if err != nil || string(local) != source {
		t.Fatalf("source changed: %s, %v", local, err)
	}
	image.Hash = hash("outdated")
	if _, err := uploadImage(context.Background(), u, image, filename); err == nil || !strings.Contains(err.Error(), "changed during sync") {
		t.Fatalf("expected changed image error, got %v", err)
	}
}

func TestStripDrawioMetadataLeavesOtherSVGsIntact(t *testing.T) {
	for _, source := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg" content="ordinary"><text>hello</text></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><text>hello</text></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" content="&lt;mxfile&gt;`,
	} {
		if got := string(stripDrawioMetadata([]byte(source))); got != source {
			t.Fatalf("unexpected change: %s", got)
		}
	}
}
