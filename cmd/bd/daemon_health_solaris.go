//go:build illumos || solaris

package main

import (
	"golang.org/x/sys/unix"
)

// checkDiskSpace returns the available disk space in MB for the given path.
// Returns (availableMB, true) on success, (0, false) on failure.
func checkDiskSpace(path string) (uint64, bool) {
	var stat unix.Statvfs_t
	if err := unix.Statvfs(path, &stat); err != nil {
		return 0, false
	}

	// Calculate available space in bytes, then convert to MB.
	// On Solaris/illumos, Frsize is the fragment size (fundamental block size).
	availableBytes := stat.Bavail * stat.Frsize
	availableMB := availableBytes / (1024 * 1024)

	return availableMB, true
}
