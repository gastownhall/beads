//go:build windows

package proxy

import (
	"syscall"

	"github.com/steveyegge/beads/internal/execwin"
)

// procAttrDetached starts bd db-proxy-child in its own process group with a
// console of its own that is never displayed, so it survives the parent
// exiting and never shows a window. See execwin.BackgroundSysProcAttr.
//
// The proxy child is the root of every dolt, git and PowerShell process the
// proxied server path starts, so its console decides what they inherit: a
// hidden one is inherited by all of them, while no console at all makes each
// of them allocate a visible one.
func procAttrDetached() *syscall.SysProcAttr {
	// CREATE_NO_WINDOW|CREATE_NEW_PROCESS_GROUP: a hidden console of its own.
	return execwin.BackgroundSysProcAttr()
}
