//go:build !unix

package atomicfile

// noFollowFlag: platforms without O_NOFOLLOW rely on the Lstat check alone;
// see OpenNoFollow's race-limitation note.
const noFollowFlag = 0

// isOpenRefusalSymlink: without O_NOFOLLOW there is no open error to
// normalize; the Lstat refusal already reported the symlink.
func isOpenRefusalSymlink(err error) bool {
	return false
}