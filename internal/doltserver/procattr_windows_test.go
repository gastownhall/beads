//go:build windows

package doltserver

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestProcAttrDetachedSuppressesConsole guards the Windows half of the
// "detached" contract.
//
// CREATE_NEW_PROCESS_GROUP alone is NOT enough: it detaches signal handling but
// leaves console allocation untouched, so a dolt sql-server spawned from a bd
// that has no console of its own (MCP server, or the detached db-proxy-child)
// gets a brand new console. With Windows Terminal registered as the default
// terminal application that surfaces as a real window on the user's desktop,
// and Windows Terminal may persist and replay the session afterwards.
//
// The server's stdout/stderr are redirected to a log file, so it has no
// legitimate use for a console at all.
func TestProcAttrDetachedSuppressesConsole(t *testing.T) {
	attr := procAttrDetached()
	if attr == nil {
		t.Fatal("procAttrDetached() = nil on windows, want non-nil")
	}
	if attr.CreationFlags&windows.DETACHED_PROCESS == 0 {
		t.Errorf("CreationFlags = %#x, missing DETACHED_PROCESS (%#x)",
			attr.CreationFlags, uint32(windows.DETACHED_PROCESS))
	}
	if attr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CreationFlags = %#x, missing CREATE_NEW_PROCESS_GROUP (%#x)",
			attr.CreationFlags, uint32(windows.CREATE_NEW_PROCESS_GROUP))
	}
}
