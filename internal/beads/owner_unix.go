//go:build unix

package beads

import (
	"os"
	"syscall"
)

// dirOwnedBy reports whether dir is an existing directory owned by uid. It
// mirrors Git's is_path_owned_by_current_uid shape (lstat, compare owner) as
// already used by internal/git's dubious-ownership handling; here it gates
// the /var/lib service-home exception (GH#6323), where the owner match — not
// just the account-database home lookup — is what makes the nominated home
// this account's own.
func dirOwnedBy(dir string, uid int) bool {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}
