//go:build unix

package beads

import (
	"os"
	"syscall"
)

// dirOwnedBy reports whether dir is an existing directory owned by uid. It is
// the ownership leg of the own-home carve-out in isPathInSafeBoundary
// (GH#6323): that carve-out pierces the SEC-003 deny list on the strength of
// the account database naming the home, so the directory itself must belong to
// the process uid — ownership is the check a forged $HOME cannot pass, and it
// keeps root-owned system homes (/usr/sbin daemons, /var/www) out. An
// unreadable path or an unexpected stat shape reports false: fail closed.
// home_owner_other.go holds the non-unix stub.
func dirOwnedBy(dir string, uid int) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}
