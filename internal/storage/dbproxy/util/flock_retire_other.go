//go:build !windows

package util

import (
	"fmt"
	"os"
)

// openRetirable opens the lock file for TryRetirableLock. Off Windows a
// held flock never blocks removal of the path, so the ordinary open is
// enough.
func openRetirable(lockPath string) (*os.File, error) {
	return os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // lockPath comes from caller-derived dirs, not user input
}

// retireFile removes the lock path. Removing a file while holding its flock
// is well-defined off Windows, and a path that is already gone counts as
// retired.
func (l *Lock) retireFile() error {
	if err := os.Remove(l.f.Name()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("util: removing lock file: %w", err)
	}
	return nil
}
