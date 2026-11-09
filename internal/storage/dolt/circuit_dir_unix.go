//go:build unix

package dolt

import (
	"fmt"
	"os"
)

// circuitBreakerDirName names the live breaker directory under os.TempDir().
// The effective uid keeps each user's state private on a host whose users
// share one temp root: a shared directory let one user's 0600 state files
// block or shadow every other user's breaker (GH#4223).
func circuitBreakerDirName() string {
	return fmt.Sprintf("beads-circuit-%d", os.Geteuid())
}
