//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package sync

import "fmt"

func lockRecoveryGuard(path string) (func(), error) {
	return nil, fmt.Errorf("automatic lock recovery is not supported on this platform; inspect %s manually", path)
}

func lockProcessAlive(pid int) (bool, error) {
	return false, fmt.Errorf("cannot check process %d on this platform", pid)
}
