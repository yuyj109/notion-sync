//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	syncer "github.com/yuyj109/notion-sync/internal/sync"
)

func TestLockRecoveryPromptAndRetry(t *testing.T) {
	proc := exec.Command("sh", "-c", "exit 0")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	pid := proc.Process.Pid
	if err := proc.Wait(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sync.lock")
	if err := os.WriteFile(path, fmt.Append(nil, pid), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("y\n"))
	var prompt bytes.Buffer
	cmd.SetErr(&prompt)
	attempts := 0
	err := withLockRecovery(cmd, func() error {
		attempts++
		if attempts == 1 {
			return &syncer.LockExistsError{Path: path}
		}
		return nil
	})
	if err != nil || attempts != 2 || !strings.Contains(prompt.String(), "Remove it and retry?") {
		t.Fatalf("err %v, attempts %d, prompt %q", err, attempts, prompt.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock remains: %v", err)
	}
}

func TestResetRecoversStaleLock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	proc := exec.Command("sh", "-c", "exit 0")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	pid := proc.Process.Pid
	if err := proc.Wait(); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(tmp, fmt.Sprintf("notion-sync-%d.lock", os.Getuid()))
	if err := os.WriteFile(lock, fmt.Append(nil, pid), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tmp, "note.md")
	if err := os.WriteFile(file, []byte("---\nnotion-page-id: 11111111-1111-1111-1111-111111111111\n---\ntext\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := newRootCommand()
	root.SetIn(strings.NewReader("y\n"))
	root.SetOut(&bytes.Buffer{})
	var prompt bytes.Buffer
	root.SetErr(&prompt)
	root.SetArgs([]string{"reset", file})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "text\n" || !strings.Contains(prompt.String(), "Remove it and retry?") {
		t.Fatalf("data=%q, prompt=%q, err=%v", data, prompt.String(), err)
	}
}
