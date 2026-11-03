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

func TestRunnerUploadSplitsLargeVideo(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ntn")
	source := `#!/bin/sh
case "$*" in
  *file_uploads/22222222-2222-2222-2222-222222222222/send*)
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "--file" ]; then shift; wc -c < "$1" >> "$CAPTURE_DIR/sizes"; fi
      case "$1" in part_number=*) printf '%s\n' "$1" >> "$CAPTURE_DIR/parts" ;; esac
      shift
    done
    printf '%s\n' '{"object":"file_upload","id":"22222222-2222-2222-2222-222222222222","status":"pending"}' ;;
  *file_uploads/22222222-2222-2222-2222-222222222222/complete*)
    printf '%s\n' '{"object":"file_upload","id":"22222222-2222-2222-2222-222222222222","status":"uploaded"}' ;;
  *) cat > "$CAPTURE_DIR/body"; printf '%s\n' '{"object":"file_upload","id":"22222222-2222-2222-2222-222222222222","status":"pending"}' ;;
esac
`
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "clip.mp4")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(20*1024*1024 + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_DIR", dir)
	id, err := (Runner{Binary: script}).Upload(context.Background(), "clip.mp4", "video/mp4", file)
	if err != nil || id != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("upload id=%q err=%v", id, err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "body"))
	parts, _ := os.ReadFile(filepath.Join(dir, "parts"))
	sizes, _ := os.ReadFile(filepath.Join(dir, "sizes"))
	if !strings.Contains(string(body), `"mode":"multi_part"`) || !strings.Contains(string(body), `"number_of_parts":3`) || strings.TrimSpace(string(parts)) != "part_number=1\npart_number=2\npart_number=3" || strings.Join(strings.Fields(string(sizes)), ",") != "10485760,10485760,1" {
		t.Fatalf("incorrect multipart upload: body=%s parts=%s sizes=%s", body, parts, sizes)
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
