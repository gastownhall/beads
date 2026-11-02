package atomicfile

import (
	"errors"
	"fmt"
	"os"
)

// ErrSymlink reports that a no-follow helper found a symbolic link (live or
// dangling) at the final component of its path.
var ErrSymlink = errors.New("refusing to follow symlink")

// rejectSymlink returns an error wrapping ErrSymlink when the final component
// of path is a symbolic link. An absent path is accepted; other Lstat errors
// propagate.
func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return symlinkError(path)
	}
	return nil
}

func symlinkError(path string) error {
	return fmt.Errorf("%w at %q", ErrSymlink, path)
}

// OpenNoFollow is os.OpenFile that refuses a symbolic link at the final
// component of path, live or dangling, with an error wrapping ErrSymlink.
//
// Parent directories are followed and must be trusted. On platforms with
// O_NOFOLLOW the kernel enforces the refusal at open time. Elsewhere (for
// example Windows) only the Lstat check runs, so a link swapped in between
// the check and the open is followed, and Windows directory junctions are
// not reported as links.
func OpenNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, flag|noFollowFlag, perm) // #nosec G304 -- caller selects path; final-component symlinks are refused.
	if err != nil {
		// O_NOFOLLOW reports a link with a platform-specific errno (ELOOP,
		// EMLINK, EFTYPE); report it the same way as the Lstat check.
		if noFollowFlag != 0 {
			if info, lerr := os.Lstat(path); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
				return nil, symlinkError(path)
			}
		}
		return nil, err
	}
	return f, nil
}

// CreateNoFollow is Create that refuses a symbolic link at the final
// component of path. The returned Writer streams to a temp file and renames
// it over path on Close; a link swapped in after the check is replaced by the
// rename, never followed. Parent directories must be trusted.
func CreateNoFollow(path string, perm os.FileMode) (*Writer, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	return Create(path, perm)
}

// WriteFileNoFollow is WriteFile that refuses a symbolic link at the final
// component of path. Parent directories must be trusted.
func WriteFileNoFollow(path string, data []byte, perm os.FileMode) error {
	if err := rejectSymlink(path); err != nil {
		return err
	}
	return WriteFile(path, data, perm)
}
