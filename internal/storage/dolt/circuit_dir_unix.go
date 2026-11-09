//go:build unix

package dolt

import (
	"fmt"
	"os"
	"syscall"
)

// circuitBreakerDirName names the live breaker directory under os.TempDir().
// The effective uid keeps each user's state private on a host whose users
// share one temp root: a shared directory let one user's 0600 state files
// block or shadow every other user's breaker (GH#4223).
func circuitBreakerDirName() string {
	return fmt.Sprintf("beads-circuit-%d", os.Geteuid())
}

// circuitDirOwnerUID is the uid the live directory must belong to: the
// effective uid that also names it. A variable so a test can stand in for
// another user's directory without root.
var circuitDirOwnerUID = os.Geteuid

// circuitDirOwnershipProblem reports why a live directory another user could
// have planted, or can change, must not hold this user's breaker state, or ""
// when it is safe. A self-owned directory that is not group- or
// other-writable is safe, whether 0700 or 0755: no other ordinary user can
// add, replace or remove its entries, and the state files in it are 0600.
func circuitDirOwnershipProblem(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "has no ownership information"
	}
	if want := circuitDirOwnerUID(); int(st.Uid) != want {
		return fmt.Sprintf("is owned by uid %d, not %d", st.Uid, want)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Sprintf("is writable by group or others (mode %04o)", perm)
	}
	return ""
}
