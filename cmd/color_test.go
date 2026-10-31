package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type testDiffView struct{}

func (testDiffView) String() string       { return "--- notion/a\n-old" }
func (testDiffView) NumberedDiff() string { return "--- notion/a\n-   8      │ old" }

type testDiffError struct{ testDiffView }

func (testDiffError) Error() string                 { return "conflict\n--- notion/a\n-old" }
func (d testDiffError) DiffViews() (string, string) { return d.String(), d.NumberedDiff() }

func TestNumberedDiffWithAndWithoutColor(t *testing.T) {
	d := testDiffView{}
	if got := syncMessage(true, "%s", d); got != d.NumberedDiff() {
		t.Fatalf("terminal output: %q", got)
	}
	if got := syncMessage(false, "%s", d); got != d.String() {
		t.Fatalf("redirected output: %q", got)
	}
	err := fmt.Errorf("a.md: %w", testDiffError{d})
	plainTerminal := formatError(err, true, false)
	if !strings.Contains(plainTerminal, "-   8      │ old") || strings.Contains(plainTerminal, "\x1b[") {
		t.Fatalf("numbered output without color: %q", plainTerminal)
	}
	colored := formatError(err, true, true)
	if !strings.Contains(colored, "\x1b[38;2;110;25;25;48;2;255;226;226m-   8      │ old\x1b[0m") {
		t.Fatalf("numbered colored output: %q", colored)
	}
	redirected := formatError(err, false, false)
	if strings.Contains(redirected, "│") {
		t.Fatalf("redirected output changed: %q", redirected)
	}
}

func TestColorDiff(t *testing.T) {
	text := "conflict\n--- notion/a.md\n+++ local/a.md\n@@ -1,1 +1,1 @@\n-old\n+new\n context"
	got := colorDiff(text, true)
	for _, part := range []string{"\x1b[1m--- notion/a.md\x1b[0m", "\x1b[36m@@ -1,1 +1,1 @@\x1b[0m", "\x1b[38;2;110;25;25;48;2;255;226;226m-old\x1b[0m", "\x1b[38;2;0;80;34;48;2;225;245;228m+new\x1b[0m"} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %q in %q", part, got)
		}
	}
	if colorDiff(text, false) != text {
		t.Fatal("disabled color changed output")
	}
	if colorDiff("-not a diff", true) != "-not a diff" {
		t.Fatal("colored unrelated text")
	}
}

func TestColorDisabledForRedirectAndNoColor(t *testing.T) {
	var output bytes.Buffer
	if colorEnabled(&output) {
		t.Fatal("buffer must not be treated as terminal")
	}
	if got := FormatError(errors.New("--- notion/a\n+new"), &output); strings.Contains(got, "\x1b[") {
		t.Fatalf("redirected error has color: %q", got)
	}
	t.Setenv("NO_COLOR", "1")
	if colorEnabled(&output) {
		t.Fatal("NO_COLOR did not disable color")
	}
}
