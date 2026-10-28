//go:build unix

package beads

import (
	"os"
	"syscall"
)

// dirOwnedBy reports whether dir (following symlinks) is an existing directory
// owned by uid. The denied-prefix home carve-out in isPathInSafeBoundary uses it
// as the check a forged $HOME cannot pass: naming someone else's directory, or
// a root-owned system home, fails here. An unreadable or unexpected stat shape
// reports false.
func dirOwnedBy(dir string, uid int) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}
