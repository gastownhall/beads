// Package execgrace configures bd-owned child processes for a graceful,
// bounded teardown when their context expires (GH#7989): the immediate child
// is asked to stop (SIGTERM on unix) so it can reap the transport children it
// spawned, and Wait stays bounded by a WaitDelay so a child — or a grandchild
// holding the inherited pipes — cannot block its reaping forever.
package execgrace

import (
	"os/exec"
	"time"
)

// GracePeriod is the WaitDelay Configure applies when the caller has not set
// one. It is a variable so tests can shrink the window; production code must
// not mutate it.
var GracePeriod = 15 * time.Second

// Configure gives cmd the teardown contract above. cmd must have been built
// by exec.CommandContext: Configure replaces the hard-kill Cancel that
// CommandContext installs by default, and leaves a caller-set WaitDelay alone
// (prepareDoltCLITransferCommand's shorter pipe bound, for example). On
// windows the default kill cancellation stays; the bounded pipe wait still
// applies.
func Configure(cmd *exec.Cmd) {
	applySignalCancel(cmd)
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = GracePeriod
	}
}