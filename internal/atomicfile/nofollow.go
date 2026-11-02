package atomicfile

import (
	"fmt"
	"os"
)

// ErrSymlink reports that path's final component is a symbolic link (live or
// dangling). Write and delete primitives refuse it: following the link would
// redirect the effect to whatever the link points at — in a repository, a
// path a workspace contributor controls.
type ErrSymlink struct {
	Path string
}

func (e *ErrSymlink) Error() string {
	return fmt.Sprintf("%s: leaf path is a symlink; refusing to follow it", e.Path)
}

// checkNoLeafSymlink refuses a symlink at path's final component. Absence is
// fine — callers create the path — and every other Lstat error propagates
// (a permission problem on the parent must not be reported as "absent").
func checkNoLeafSymlink(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return &ErrSymlink{Path: path}
	}
	return nil
}

// OpenNoFollow opens path (create/truncate/read-write per flags) and refuses
// a symlink at the final component, so the descriptor can never reach the
// link's target. It is the open path for lock files and other long-lived
// handles where rename-over is not an option (inode identity matters).
//
// Two defenses: an Lstat refusal before the open, and (on platforms that
// have it) O_NOFOLLOW on the open itself. The flag narrows but does not close
// the race between the check and the open — a link swapped in between the two
// is caught by O_NOFOLLOW's ELOOP on supported Unix platforms, and only by
// the check on platforms without the flag. Ancestor-directory links are out
// of scope; only the leaf is protected.
func OpenNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	if err := checkNoLeafSymlink(path); err != nil {
		return nil, err
	}
	// nolint:gosec // G304: path is a caller-chosen file location, and this
	// helper's whole contract is refusing unsafe leaves of it.
	f, err := os.OpenFile(path, flags|noFollowFlag, perm)
	if err != nil {
		if isOpenRefusalSymlink(err) {
			return nil, &ErrSymlink{Path: path}
		}
		return nil, err
	}
	return f, nil
}

// CreateNoFollow returns a Writer that atomically replaces path on Close,
// refusing when path is a leaf symlink: the rename would replace the link
// itself rather than its target, which is not what a caller asked for.
func CreateNoFollow(path string, perm os.FileMode) (*Writer, error) {
	if err := checkNoLeafSymlink(path); err != nil {
		return nil, err
	}
	return Create(path, perm)
}

// WriteFileNoFollow atomically writes data to path, refusing a leaf symlink.
// A link swapped in after the check is replaced by the rename, not followed
// — the temp file is created in the target's own directory and renamed over
// the link itself.
func WriteFileNoFollow(path string, data []byte, perm os.FileMode) error {
	if err := checkNoLeafSymlink(path); err != nil {
		return err
	}
	return WriteFile(path, data, perm)
}

// noFollowFlag and isOpenRefusalSymlink are platform-specific; see
// nofollow_unix.go and nofollow_other.go.