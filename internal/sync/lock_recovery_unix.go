//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package sync

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func lockRecoveryGuard(path string) (func(), error) {
	f, err := os.OpenFile(path+".recovery", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot open lock recovery guard: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another lock recovery is in progress: %w", err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func lockProcessAlive(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, fmt.Errorf("cannot check process %d: %w", pid, err)
}
