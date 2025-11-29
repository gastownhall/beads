//go:build !windows && !wasm

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

	// Calculate available space in bytes, then convert to MB
	// Bavail is uint64, Bsize is int64; overflow is intentional/safe in this context
	availableBytes := stat.Bavail * uint64(stat.Bsize) //nolint:gosec
	availableMB := availableBytes / (1024 * 1024)

	return availableMB, true
}
