//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package sync

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRemoveStaleLockRequiresConfirmation(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "sync.lock")
	if err := os.WriteFile(name, []byte(fmt.Sprint(pid)), 0600); err != nil {
		t.Fatal(err)
	}
	lockErr := &LockExistsError{Path: name}
	removed, err := RemoveStaleLock(lockErr, func(_ string, got int) (bool, error) {
		if got != pid {
			t.Fatalf("got pid %d", got)
		}
		return false, nil
	})
	if err != nil || removed {
		t.Fatalf("declined: %v, %v", removed, err)
	}
	if _, err := os.Stat(name); err != nil {
		t.Fatal(err)
	}
	removed, err = RemoveStaleLock(lockErr, func(string, int) (bool, error) { return true, nil })
	if err != nil || !removed {
		t.Fatalf("confirmed: %v, %v", removed, err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("lock remains: %v", err)
	}
}

func TestRemoveStaleLockRejectsLiveOrReplacedLock(t *testing.T) {
	name := filepath.Join(t.TempDir(), "sync.lock")
	if err := os.WriteFile(name, []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := RemoveStaleLock(&LockExistsError{Path: name}, func(string, int) (bool, error) { called = true; return true, nil })
	if err == nil || called {
		t.Fatalf("live lock: %v, prompted=%v", err, called)
	}
	if _, err := os.Stat(name); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveStaleLockRejectsReplacedLock(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "sync.lock")
	if err := os.WriteFile(name, []byte(fmt.Sprint(pid)), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := RemoveStaleLock(&LockExistsError{Path: name}, func(string, int) (bool, error) {
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		return true, nil
	})
	if err == nil {
		t.Fatal("expected changed lock error")
	}
	if _, err := os.Stat(name); err != nil {
		t.Fatal(err)
	}
}
