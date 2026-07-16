package flatfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// validateID rejects issue IDs that could escape the target directory via path
// traversal. IDs must not contain path separators or ".." components.
func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("flatfile: empty issue ID")
	}
	if strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("flatfile: invalid issue ID %q: contains path separator", id)
	}
	// The "." prefix check catches ".", "..", and hidden files. Any other
	// ".." component would need a separator, rejected above.
	if strings.HasPrefix(id, ".") {
		return fmt.Errorf("flatfile: invalid issue ID %q: must not start with '.'", id)
	}
	return nil
}

// safePath constructs a path under parentDir and verifies the result doesn't
// escape. Containment is checked lexically, and parentDir and the result are
// Lstat'd to reject symlinks: .beads is git-synced, so a hostile repo can
// commit a symlink under it (e.g. comments/BD-1 -> ~/.config) that a plain
// git checkout materializes; following it would read or write outside the
// store. The Lstat is a pre-flight check, which is sufficient for that
// static, checkout-materialized threat; it does not defend against a local
// process racing symlinks into place between check and use.
func safePath(parentDir, child, ext string) (string, error) {
	if err := validateID(child); err != nil {
		return "", err
	}
	result := filepath.Join(parentDir, child+ext)
	// Verify the resolved path is actually under parentDir.
	abs, err := filepath.Abs(result)
	if err != nil {
		return "", fmt.Errorf("flatfile: resolve path: %w", err)
	}
	absParent, err := filepath.Abs(parentDir)
	if err != nil {
		return "", fmt.Errorf("flatfile: resolve parent: %w", err)
	}
	if !strings.HasPrefix(abs, absParent+string(filepath.Separator)) && abs != absParent {
		return "", fmt.Errorf("flatfile: path %q escapes directory %q", result, parentDir)
	}
	for _, p := range []string{parentDir, result} {
		if err := rejectSymlink(p); err != nil {
			return "", err
		}
	}
	return result, nil
}

// rejectSymlink returns an error if path exists and is a symlink. A missing
// path is fine: the caller creates it as a regular file or directory.
func rejectSymlink(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("flatfile: lstat %s: %w", path, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("flatfile: path %q is a symlink; refusing to follow it outside the store", path)
	}
	return nil
}

// atomicWriteFile writes data to path durably and atomically: a uniquely-named
// temp file in the same directory is written, fsynced, closed, chmodded to
// 0644, then renamed over path. The unique temp name (os.CreateTemp) keeps two
// concurrent writers of the same path from renaming each other's half-written
// temp into place; the fsync keeps a crash right after the rename from
// persisting an empty or truncated file. The directory is deliberately not
// fsynced: losing the rename itself just resurfaces the previous consistent
// version, which is acceptable here and avoids doubling the fsync cost.
func atomicWriteFile(path string, data []byte) error {
	f, err := tempFile(path)
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// tempFile creates the uniquely-named temp file atomicWriteFile renames over
// path. The "<base>.*.tmp" pattern (os.CreateTemp replaces the last '*') keeps
// the name unique while still ENDING in ".tmp": a temp orphaned by a crash
// between creation and rename must stay inside the "*.tmp" rule of the
// .beads/.gitignore written at init and the ".tmp" suffix guard in
// loadAllIssues, or the next 'git add .beads/' stages the litter and syncs it
// to every peer permanently.
func tempFile(path string) (*os.File, error) {
	return os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
}

// readDirSafe reads a directory, returning nil (not error) if it doesn't exist.
func readDirSafe(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}
