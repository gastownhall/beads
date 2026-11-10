//go:build windows

package doltserver

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestProcAttrDetachedSuppressesConsole guards the Windows half of the
// "detached" contract of the shared dolt sql-server.
//
// CREATE_NEW_PROCESS_GROUP alone is NOT enough: it detaches signal handling but
// leaves console allocation untouched, so a dolt sql-server spawned from a bd
// that has no console of its own (MCP server, or the db-proxy-child) gets a
// brand new visible console. With Windows Terminal registered as the default
// terminal application that surfaces as a real window on the user's desktop,
// and Windows Terminal may persist and replay the session afterwards.
//
// DETACHED_PROCESS must NOT be used instead: it leaves dolt with no console,
// and the `uname` calls dolt makes on every start each get a visible console
// of their own. CREATE_NO_WINDOW gives dolt a hidden console those children
// inherit.
func TestProcAttrDetachedSuppressesConsole(t *testing.T) {
	attr := procAttrDetached()
	if attr == nil {
		t.Fatal("procAttrDetached() = nil on windows, want non-nil")
	}
	if want := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP); attr.CreationFlags != want {
		t.Errorf("CreationFlags = %#x, want exactly CREATE_NO_WINDOW|CREATE_NEW_PROCESS_GROUP (%#x)", attr.CreationFlags, want)
	}
	if attr.CreationFlags&windows.DETACHED_PROCESS != 0 {
		t.Errorf("CreationFlags = %#x, must not include DETACHED_PROCESS (%#x): dolt's console children would each open a window",
			attr.CreationFlags, uint32(windows.DETACHED_PROCESS))
	}
}
