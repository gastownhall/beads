//go:build windows

package config

import (
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

// CheckBeadsDirPermissions is a no-op on Windows where filesystem
// permissions use ACLs rather than Unix permission bits.
func CheckBeadsDirPermissions(path string) {}
