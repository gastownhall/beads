//go:build unix || linux || darwin

package daemonrunner

import (
	"os"
	"syscall"
)

var daemonSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

func isReloadSignal(sig os.Signal) bool {
	return sig == syscall.SIGHUP
}
