//go:build windows

package ownershiphandoff

import (
	"os/exec"
	"time"
)

const gcHandoffPipeDrainDelay = 100 * time.Millisecond

func configureGCHandoffCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = gcHandoffPipeDrainDelay
}
