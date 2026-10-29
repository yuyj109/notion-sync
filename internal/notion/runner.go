package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const APIVersion = "2026-03-11"

var statusPattern = regexp.MustCompile(`\(([0-9]{3}) [^)]*\)`)
var retryPattern = regexp.MustCompile(`(?im)^<\s*retry-after:\s*(\d+)\s*$`)

type Runner struct {
	Binary  string
	Timeout time.Duration
}
type APIError struct {
	ExitCode        int
	Status          string
	Detail          string
	RetryAfter      time.Duration
	RetryAfterKnown bool
	Blocked         bool
}

func (e *APIError) Error() string {
	if e.Status != "" {
		if e.RetryAfterKnown {
			return fmt.Sprintf("ntn API request failed (HTTP %s, exit %d, Retry-After %s)", e.Status, e.ExitCode, e.RetryAfter)
		}
		return fmt.Sprintf("ntn API request failed (HTTP %s, exit %d)", e.Status, e.ExitCode)
	}
	return fmt.Sprintf("ntn failed (exit %d): %s", e.ExitCode, e.Detail)
}
func (r Runner) command(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	binary := r.Binary
	if binary == "" {
		binary = "ntn"
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("ntn timed out or was cancelled: %w", ctx.Err())
	}
	if err != nil {
		e := &APIError{ExitCode: -1, Detail: "check ntn installation and login"}
		if ex, ok := err.(*exec.ExitError); ok {
			e.ExitCode = ex.ExitCode()
		}
		if m := statusPattern.FindStringSubmatch(stderr.String()); len(m) > 1 {
			e.Status = m[1]
		}
		if m := retryPattern.FindStringSubmatch(stderr.String()); len(m) > 1 {
			if seconds, err := strconv.Atoi(m[1]); err == nil && seconds >= 0 && seconds <= 86400 {
				e.RetryAfter = time.Duration(seconds) * time.Second
				e.RetryAfterKnown = true
			}
		}
		e.Blocked = strings.Contains(stderr.String(), "public_api_request_blocked")
		return nil, e
	}
	return out.Bytes(), nil
}
func (r Runner) Version(ctx context.Context) (string, error) {
	b, e := r.command(ctx, nil, "--version")
	return strings.TrimSpace(string(b)), e
}
func (r Runner) API(ctx context.Context, method, path string, body any, result any) error {
	// Verbose diagnostics are captured, never printed; they expose Retry-After.
	args := []string{"--verbose", "api", path, "-X", method, "--notion-version", APIVersion}
	var input []byte
	if body != nil {
		var err error
		input, err = json.Marshal(body)
		if err != nil {
			return err
		}
		args = append(args, "--data", "@-")
	}
	var out []byte
	var err error
	for attempt := 0; ; attempt++ {
		out, err = r.command(ctx, input, args...)
		if err == nil {
			break
		}
		var apiErr *APIError
		if method != "GET" || attempt >= 2 || !errors.As(err, &apiErr) || apiErr.Blocked {
			return err
		}
		delay := time.Duration(1<<attempt) * time.Second
		switch apiErr.Status {
		case "429", "529":
			if !apiErr.RetryAfterKnown {
				return err
			}
			delay = apiErr.RetryAfter
		case "500", "502", "503", "504":
		default:
			return err
		}
		delay += time.Duration(rand.Intn(250)) * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err = json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("invalid JSON from ntn: %w", err)
	}
	return nil
}

// Upload sends a local file through ntn's documented multipart --file option.
// The file upload object is created with JSON first, then the file bytes are sent.
func (r Runner) Upload(ctx context.Context, filename, contentType, localPath string) (string, error) {
	var created struct {
		ID     string `json:"id"`
		Object string `json:"object"`
	}
	if err := r.API(ctx, "POST", "v1/file_uploads", map[string]any{"filename": filename, "content_type": contentType, "mode": "single_part"}, &created); err != nil {
		return "", err
	}
	if created.Object != "file_upload" || !strings.Contains(created.ID, "-") {
		return "", fmt.Errorf("unexpected file upload response")
	}
	args := []string{"--verbose", "api", "v1/file_uploads/" + created.ID + "/send", "-X", "POST", "--notion-version", APIVersion, "--file", localPath}
	out, err := r.command(ctx, nil, args...)
	if err != nil {
		return "", err
	}
	var sent struct{ ID, Object, Status string }
	if err = json.Unmarshal(out, &sent); err != nil {
		return "", fmt.Errorf("invalid JSON from ntn upload: %w", err)
	}
	if sent.Object != "file_upload" || sent.ID != created.ID || sent.Status != "uploaded" {
		return "", fmt.Errorf("unexpected file upload completion response")
	}
	return sent.ID, nil
}
