//go:build !windows

package config

import (
	"fmt"
	"io/fs"
	"os"
)

const (
	// BeadsDirPerm is the permission mode for .beads/ directories (owner-only).
	BeadsDirPerm fs.FileMode = 0700
	// BeadsFilePerm is the permission mode for state files inside .beads/ (owner-only).
	BeadsFilePerm fs.FileMode = 0600
)

// EnsureBeadsDir creates the .beads directory with secure permissions.
func EnsureBeadsDir(path string) error {
	return os.MkdirAll(path, BeadsDirPerm)
}

// CheckBeadsDirPermissions warns to stderr if the .beads directory has
// group or world-accessible permissions. The check is non-fatal.
func CheckBeadsDirPermissions(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return // directory doesn't exist yet
	}
	perm := info.Mode().Perm()
	if perm&0077 != 0 {
		fmt.Fprintf(os.Stderr, "Warning: %s has permissions %04o (recommended: 0700). Run: chmod 700 %s\n", path, perm, path)
	}
}

// FixBeadsDirPermissions sets the .beads directory to BeadsDirPerm when it
// has group or world-accessible bits. Returns true if permissions changed.
func FixBeadsDirPermissions(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, nil // directory doesn't exist yet
	}
	perm := info.Mode().Perm()
	if perm&0077 == 0 {
		return false, nil // no group or world-accessible bits
	}
	if err := os.Chmod(path, BeadsDirPerm); err != nil {
		return false, fmt.Errorf("failed to chmod %s to %04o: %w", path, BeadsDirPerm, err)
	}
	return true, nil
}
