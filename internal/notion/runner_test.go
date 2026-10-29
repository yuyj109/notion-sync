package notion

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerBodyAndError(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ntn")
	source := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_DIR/args\"\ncat > \"$CAPTURE_DIR/body\"\nprintf '%s\\n' '{\"object\":\"page\"}'\n"
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_DIR", dir)
	var result map[string]any
	if err := (Runner{Binary: script}).API(context.Background(), "POST", "v1/pages", map[string]any{"markdown": "text"}, &result); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "body"))
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(body), `"markdown":"text"`) || !strings.Contains(string(args), "--data\n@-") || result["object"] != "page" {
		t.Fatalf("bad request: args=%q body=%q result=%v", args, body, result)
	}
}

func TestRunnerUploadUsesDocumentedNtnOptions(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ntn")
	source := `#!/bin/sh
printf '%s\n' "$@" >> "$CAPTURE_DIR/args"
case "$*" in
  *file_uploads/22222222-2222-2222-2222-222222222222/send*) printf '%s\n' '{"object":"file_upload","id":"22222222-2222-2222-2222-222222222222","status":"uploaded"}' ;;
  *) printf '%s\n' '{"object":"file_upload","id":"22222222-2222-2222-2222-222222222222"}' ;;
esac
`
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "image.png")
	if err := os.WriteFile(file, []byte("png"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_DIR", dir)
	id, err := (Runner{Binary: script}).Upload(context.Background(), "image.png", "image/png", file)
	if err != nil {
		t.Fatal(err)
	}
	if id != "22222222-2222-2222-2222-222222222222" {
		t.Fatal(id)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--file\n"+file) || !strings.Contains(string(args), "v1/file_uploads") {
		t.Fatalf("args=%q", args)
	}
}
func TestRunnerRetriesReadRateLimit(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ntn")
	source := "#!/bin/sh\nif [ ! -e \"$CAPTURE_DIR/attempt\" ]; then touch \"$CAPTURE_DIR/attempt\"; printf '< retry-after: 1\\nerror: Public API request failed (429 Too Many Requests rate_limited)\\n' >&2; exit 5; fi\nprintf '%s\\n' '{\"object\":\"user\"}'\n"
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_DIR", dir)
	var result map[string]any
	if err := (Runner{Binary: script}).API(context.Background(), "GET", "v1/users/me", nil, &result); err != nil {
		t.Fatal(err)
	}
	if result["object"] != "user" {
		t.Fatal(result)
	}
}
func TestRunnerDoesNotRetryWrite(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ntn")
	source := "#!/bin/sh\nprintf x >> \"$CAPTURE_DIR/attempts\"\nprintf '< retry-after: 1\\nerror: Public API request failed (429 Too Many Requests rate_limited)\\n' >&2\nexit 5\n"
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_DIR", dir)
	var result map[string]any
	err := (Runner{Binary: script}).API(context.Background(), "POST", "v1/pages", map[string]any{}, &result)
	if err == nil {
		t.Fatal("expected error")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "attempts"))
	if string(data) != "x" {
		t.Fatalf("write repeated: %q", data)
	}
}
