//go:build linux

package doltserver

import (
	"os"
	"syscall"
)

func applyTestModeParentDeathSignal(attr *syscall.SysProcAttr) {
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		attr.Pdeathsig = syscall.SIGTERM
	}
}
