//go:build windows

// Package sqlite implements the storage interface using SQLite.
package sqlite

import (
	"os"
)

// getFileInode returns 0 on Windows since inodes are not available.
// File replacement detection will rely on mtime/size instead.
func getFileInode(info os.FileInfo) uint64 {
	// Windows doesn't have inodes, return 0 to skip inode-based detection.
	// The FreshnessChecker will fall back to mtime-based detection.
	return 0
}
