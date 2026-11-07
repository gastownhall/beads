// Package execgrace makes context cancellation of a Beads-owned child process
// graceful. os/exec's default cancellation kills the immediate child at once,
// which gives a dolt CLI no chance to stop the git transport it runs in its
// own process group; that transport can then outlive the transfer. Configure
// asks the child to stop first and keeps os/exec's forced kill as the bounded
// fallback.
package execgrace

import (
	"os/exec"
	"time"
)

// Configure sets cmd, which must come from exec.CommandContext and not yet be
// started, to stop gracefully when its context ends. On Unix the immediate
// child receives SIGTERM, so a cooperative child (dolt) can cancel and reap
// its own transport before exiting; elsewhere the default cancellation (kill)
// is kept. Either way, if the child has not exited, or a descendant still
// holds its output pipes, waitDelay after cancellation, os/exec kills the
// child and closes the pipes so Wait returns.
//
// Only the immediate child is signaled: the dolt CLI owns cancellation of
// the process group it starts for its transport, so no process-group signal
// or tree walk is needed here, and none reaches unrelated processes.
func Configure(cmd *exec.Cmd, waitDelay time.Duration) {
	setGracefulCancel(cmd)
	cmd.WaitDelay = waitDelay
}
