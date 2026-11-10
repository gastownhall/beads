//go:build windows

package proxy

import (
	"syscall"

	"github.com/steveyegge/beads/internal/execwin"
)

// procAttrDetached returns SysProcAttr for bd db-proxy-child: a hidden
// console of its own, so nothing is ever displayed but the console programs
// dolt spawns (uname probes) still find one, in its own process group, so it
// survives its parent exiting. Deliberately no DETACHED_PROCESS, which main
// used here: a console-less child makes every console program it spawns
// allocate a visible window (GH#6076).
func procAttrDetached() *syscall.SysProcAttr {
	// Exactly what execwin.BackgroundSysProcAttr() pins: CREATE_NO_WINDOW |
	// CREATE_NEW_PROCESS_GROUP (the analog of Unix Setpgid).
	return execwin.BackgroundSysProcAttr()
}
