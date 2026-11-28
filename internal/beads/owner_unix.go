//go:build unix

package beads

import (
	"os"
	"syscall"
)

// dirOwnedBy reports whether path is a directory, not a symlink to one, whose
// owner is uid. It backs the service-home exception in isPathInSafeBoundary.
func dirOwnedBy(path string, uid int) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(st.Uid) == int64(uid)
}
