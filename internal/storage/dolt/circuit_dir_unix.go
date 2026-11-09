//go:build unix

package dolt

import (
	"os"
	"strconv"
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
