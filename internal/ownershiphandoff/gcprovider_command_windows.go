//go:build windows

package ownershiphandoff

import "os/exec"

// configureGCHandoffCommand bounds the pipe drain only. Windows has no process
// group to signal, so a hung protocol command's children are not reaped here;
// the GC handoff provider is not supported on Windows.
func configureGCHandoffCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = gcHandoffPipeDrainDelay
}
