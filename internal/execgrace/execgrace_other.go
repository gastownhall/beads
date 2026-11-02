//go:build !unix

package execgrace

import "os/exec"

// applySignalCancel keeps the default cancellation (Process.Kill): windows
// has no portable SIGTERM for arbitrary children, so the child is killed
// hard; Configure still bounds pipe waiting through WaitDelay.
func applySignalCancel(*exec.Cmd) {}