//go:build !windows

package util

import "os"

// Outside Windows an open lease never blocks removal of its pathname, so a
// retirable lease is an ordinary one and retirement is a plain removal.

func openRetirableLockFile(lockPath string) (*os.File, error) {
	return openLockFile(lockPath)
}

func retireLockFile(f *os.File) error {
	if err := os.Remove(f.Name()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
