//go:build windows

package config

import (
	"io/fs"
	"os"
)

const (
	// BeadsDirPerm is the permission mode for .beads/ directories (owner and group).
	BeadsDirPerm fs.FileMode = 0770
	// BeadsFilePerm is the permission mode for state files inside .beads/ (owner and group).
	BeadsFilePerm fs.FileMode = 0660
)

// EnsureBeadsDir creates the .beads directory with secure permissions.
func EnsureBeadsDir(path string) error {
	return os.MkdirAll(path, BeadsDirPerm)
}

// CheckBeadsDirPermissions is a no-op on Windows where filesystem
// permissions use ACLs rather than Unix permission bits.
func CheckBeadsDirPermissions(path string) {}

// FixBeadsDirPermissions is a no-op on Windows where filesystem
// permissions use ACLs rather than Unix permission bits.
func FixBeadsDirPermissions(path string) (bool, error) { return false, nil }
