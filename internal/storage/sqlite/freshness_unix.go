//go:build !windows

// Package sqlite implements the storage interface using SQLite.
package sqlite

import (
	"os"
	"syscall"
)

// getFileInode extracts the inode from file info on Unix systems.
func getFileInode(info os.FileInfo) uint64 {
	if sys := info.Sys(); sys != nil {
		if stat, ok := sys.(*syscall.Stat_t); ok {
			return stat.Ino
		}
	}
	return 0
}
