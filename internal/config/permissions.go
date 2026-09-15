//go:build !windows

package config

import (
	"fmt"
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

// CheckBeadsDirPermissions warns to stderr if the .beads directory is
// world-accessible. Group access may be intentional on a trusted host.
// The check is non-fatal.
func CheckBeadsDirPermissions(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return // directory doesn't exist yet
	}
	perm := info.Mode().Perm()
	if perm&0007 != 0 {
		fmt.Fprintf(os.Stderr, "Warning: %s has world-accessible permissions %04o. Run: chmod o-rwx %s\n", path, perm, path)
	}
}

// FixBeadsDirPermissions sets the .beads directory to BeadsDirPerm when its
// owner/group/world permissions differ. Existing special bits, such as setgid,
// are preserved. Returns true if permissions changed.
func FixBeadsDirPermissions(path string) (bool, error) {
	return fixBeadsDirPermissions(path, openBeadsDirHandle)
}

type beadsDirHandle interface {
	Stat() (os.FileInfo, error)
	Chmod(os.FileMode) error
	Close() error
}

func fixBeadsDirPermissions(path string, openDir func(string) (beadsDirHandle, error)) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // directory doesn't exist yet
		}
		return false, fmt.Errorf("failed to inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("refusing to chmod %s: path is a symbolic link", path)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("refusing to chmod %s: path is not a directory", path)
	}
	if info.Mode().Perm() == BeadsDirPerm {
		return false, nil
	}

	dir, err := openDir(path)
	if err != nil {
		return false, fmt.Errorf("failed to open %s securely: %w", path, err)
	}
	defer func() { _ = dir.Close() }()

	openedInfo, err := dir.Stat()
	if err != nil {
		return false, fmt.Errorf("failed to inspect opened directory %s: %w", path, err)
	}
	if !openedInfo.IsDir() || !os.SameFile(info, openedInfo) {
		return false, fmt.Errorf("refusing to chmod %s: path changed during permission repair", path)
	}
	mode := BeadsDirPerm | (info.Mode() & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky))
	if err := dir.Chmod(mode); err != nil {
		return false, fmt.Errorf("failed to chmod %s to %04o: %w", path, BeadsDirPerm, err)
	}
	return true, nil
}
