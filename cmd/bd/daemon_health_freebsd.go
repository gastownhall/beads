//go:build freebsd && !wasm

package main

import (
	"golang.org/x/sys/unix"
)

// checkDiskSpace returns the available disk space in MB for the given path.
// Returns (availableMB, true) on success, (0, false) on failure.
func checkDiskSpace(path string) (uint64, bool) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, false
	}

	if stat.Bavail < 0 {
		return 0, true
	}

	availableBytes := uint64(stat.Bavail) * stat.Bsize //nolint:gosec
	availableMB := availableBytes / (1024 * 1024)

	return availableMB, true
}
