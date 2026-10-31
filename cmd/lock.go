package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	syncer "github.com/yuyj109/notion-sync/internal/sync"
)

func withLockRecovery(cmd *cobra.Command, run func() error) error {
	err := run()
	var lockErr *syncer.LockExistsError
	if !errors.As(err, &lockErr) {
		return err
	}
	removed, recoveryErr := syncer.RemoveStaleLock(lockErr, func(path string, pid int) (bool, error) {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Stale sync lock %s (process %d is not running). Remove it and retry? [y/N] ", path, pid); err != nil {
			return false, err
		}
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		return answer == "y" || answer == "yes", nil
	})
	if recoveryErr != nil {
		return recoveryErr
	}
	if !removed {
		return err
	}
	return run()
}
