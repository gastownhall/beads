//go:build unix

package atomicfile

import (
	"errors"
	"syscall"
)

// noFollowFlag makes the open itself refuse a leaf symlink swapped in after
// the Lstat check. The kernel rejects it with ELOOP, which isOpenRefusal
// Symlink normalizes into ErrSymlink.
const noFollowFlag = syscall.O_NOFOLLOW

// isOpenRefusalSymlink reports whether err is the ELOOP the open produces
// when O_NOFOLLOW met a symlink at the final path component.
func isOpenRefusalSymlink(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.ELOOP
	}
	return false
}