package sync

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// LockExistsError identifies a lock that prevented an operation from starting.
type LockExistsError struct{ Path string }

func (e *LockExistsError) Error() string {
	return fmt.Sprintf("cannot acquire sync lock %s: file exists", e.Path)
}

// RemoveStaleLock asks before removing a lock whose owning process has exited.
// Concurrent recovery attempts are serialized and the lock is rechecked after confirmation.
func RemoveStaleLock(e *LockExistsError, confirm func(path string, pid int) (bool, error)) (bool, error) {
	release, err := lockRecoveryGuard(e.Path)
	if err != nil {
		return false, err
	}
	defer release()
	read := func() (os.FileInfo, int, error) {
		info, err := os.Lstat(e.Path)
		if err != nil {
			return nil, 0, err
		}
		if !info.Mode().IsRegular() {
			return nil, 0, fmt.Errorf("lock is not a regular file: %s", e.Path)
		}
		f, err := os.Open(e.Path)
		if err != nil {
			return nil, 0, err
		}
		defer func() { _ = f.Close() }()
		opened, err := f.Stat()
		if err != nil || !os.SameFile(info, opened) {
			return nil, 0, fmt.Errorf("lock changed while reading: %s", e.Path)
		}
		data, err := io.ReadAll(io.LimitReader(f, 64))
		if err != nil {
			return nil, 0, err
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			return nil, 0, fmt.Errorf("cannot verify lock owner in %s; inspect it manually", e.Path)
		}
		return info, pid, nil
	}
	info, pid, err := read()
	if err != nil {
		return false, err
	}
	alive, err := lockProcessAlive(pid)
	if err != nil {
		return false, err
	}
	if alive {
		return false, fmt.Errorf("sync lock is held by running process %d: %s", pid, e.Path)
	}
	ok, err := confirm(e.Path, pid)
	if err != nil || !ok {
		return false, err
	}
	current, currentPID, err := read()
	if err != nil {
		return false, err
	}
	if !os.SameFile(info, current) || pid != currentPID {
		return false, fmt.Errorf("sync lock changed; retry the command")
	}
	alive, err = lockProcessAlive(pid)
	if err != nil {
		return false, err
	}
	if alive {
		return false, fmt.Errorf("process %d is running; lock was not removed", pid)
	}
	if err := os.Remove(e.Path); err != nil {
		return false, err
	}
	return true, nil
}
