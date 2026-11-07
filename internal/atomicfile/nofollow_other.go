//go:build !unix

package atomicfile

// noFollowFlag is 0 on non-Unix platforms where O_NOFOLLOW is unavailable.
// Callers fall back to os.Lstat (os.ModeSymlink) only, which rejects symbolic
// links at the final component but does not report Windows directory junctions.
const noFollowFlag = 0
