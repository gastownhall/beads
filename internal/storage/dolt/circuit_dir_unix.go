//go:build unix

package dolt

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// circuitDirName returns the per-user name of the circuit breaker state
// directory under the temp root. One shared directory on a multi-user host
// meant one user's 0600 state files were unreadable and unwritable by every
// other user, silently discarding their breaker state — and a shared
// writable directory would let any local user trip or reset another user's
// breaker (GH#4223). The uid suffix gives every Unix user a private
// directory; same-user processes still coordinate through it, and host-wide
// cross-user coordination is deliberately dropped. Other platforms
// (circuit_dir_other.go) keep the unsuffixed name because their temp root
// is already per-user (%TEMP% on Windows).
func circuitDirName() string {
	return "beads-circuit-" + strconv.Itoa(os.Geteuid())
}

// circuitDirOwnerFn returns the uid whose circuit state directory is being
// validated. A var (matching statCAPathFn in internal/httpclient) so a test
// can simulate foreign ownership — an owner other than the uid running the
// process — without a second account or root on the host.
var circuitDirOwnerFn = os.Geteuid

// validateCircuitDirOwnership refuses a circuit state directory another
// ordinary user could write through (GH#4223): it must belong to the uid
// running this process, and neither group nor other may have write
// permission (mode & 0022). A self-owned 0755 directory is accepted: another
// ordinary user cannot create or replace entries in it, and the state files
// themselves stay 0600. An unsafe directory is refused, never chmod'd or
// chown'd — a directory someone else created may already hold planted
// entries, and widening it would keep them.
func validateCircuitDirOwnership(dir string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("ownership of %s is unavailable", dir)
	}
	if st.Uid != uint32(circuitDirOwnerFn()) {
		return fmt.Errorf("%s is owned by uid %d, not uid %d running this process",
			dir, st.Uid, circuitDirOwnerFn())
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("%s has mode %04o, writable by group or others", dir, perm)
	}
	return nil
}
