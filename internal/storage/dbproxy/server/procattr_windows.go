//go:build windows

package server

import (
	"syscall"

	"github.com/steveyegge/beads/internal/execwin"
)

// managedServerSysProcAttr returns the creation flags for the long-lived
// `dolt sql-server` this package manages: a console of its own that is never
// displayed, in its own process group. See execwin.BackgroundSysProcAttr.
//
// Without it, a parent with no console to share (bd db-proxy-child, an MCP
// server) makes the system allocate a visible console for dolt, which Windows
// Terminal shows as a real window when it is the default terminal. It must not
// be DETACHED_PROCESS, even though the server's output already goes to a log:
// dolt's `uname` children would then each get a visible console.
//
// None of this changes lifetime management: the server is still a child this
// package Waits on and kills by handle through the managed context.
func managedServerSysProcAttr() *syscall.SysProcAttr {
	// CREATE_NO_WINDOW|CREATE_NEW_PROCESS_GROUP: a hidden console of its own.
	return execwin.BackgroundSysProcAttr()
}
