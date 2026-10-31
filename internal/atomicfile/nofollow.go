package atomicfile

import (
	"fmt"
	"os"
)

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink at %s", path)
	}
	return nil
}

// OpenNoFollow opens path without following a symlink at its final component.
// Parent directories must be trusted. Non-Unix platforms use only Lstat
// (os.ModeSymlink), which does not report Windows directory junctions.
func OpenNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	return os.OpenFile(path, flag|noFollowFlag, perm) // #nosec G304 -- caller selects path; final-component symlinks are rejected.
}

// CreateNoFollow exclusively creates a new regular file at path.
func CreateNoFollow(path string, perm os.FileMode) (*os.File, error) {
	return OpenNoFollow(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
}

// WriteFileNoFollow atomically replaces path, refusing a final-component symlink.
// Parent directories must be trusted.
func WriteFileNoFollow(path string, data []byte, perm os.FileMode) error {
	if err := rejectSymlink(path); err != nil {
		return err
	}
	return WriteFile(path, data, perm)
}

// WriterNoFollow returns a Writer that streams to a temp file and atomically
// renames it over path on Close, refusing a final-component symlink.
// Parent directories must be trusted.
func WriterNoFollow(path string, perm os.FileMode) (*Writer, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	return Create(path, perm)
}
