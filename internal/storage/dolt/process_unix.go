//go:build cgo && !windows

package dolt

import (
	"os"
	"strings"
	"syscall"
)

func processMayBeAlive(p *os.Process) bool {
	// Signal 0 checks for existence without sending a real signal.
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

func terminateProcess(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		// Process may already be dead; treat as success.
		if strings.Contains(err.Error(), "process already finished") {
			return nil
		}
		return err
	}
	return nil
}
