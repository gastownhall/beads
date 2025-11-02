//go:build windows

package daemonrunner

import (
	"os"
	"syscall"
)

var daemonSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

func isReloadSignal(os.Signal) bool {
	return false
}
