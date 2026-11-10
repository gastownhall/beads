//go:build windows

package server

import (
	"syscall"

	"github.com/steveyegge/beads/internal/execwin"
)

// managedServerSysProcAttr pins the managed sql-server to a hidden console
// of its own (CREATE_NO_WINDOW, so nothing is ever displayed but dolt's
// uname probes still find one) in its own process group
// (CREATE_NEW_PROCESS_GROUP), so it survives its parent exiting.
// Deliberately no DETACHED_PROCESS: a console-less child makes every console
// program it spawns allocate a visible window (GH#6076).
func managedServerSysProcAttr() *syscall.SysProcAttr {
	return execwin.BackgroundSysProcAttr()
}
