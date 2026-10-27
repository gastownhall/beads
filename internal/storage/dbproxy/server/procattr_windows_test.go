//go:build windows

package server

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestManagedServerSysProcAttrHidesConsole pins the Windows creation flags of
// the long-lived `dolt sql-server` this package manages.
//
// CREATE_NO_WINDOW gives the server a console of its own that is never
// displayed, and the console programs dolt starts inherit it. DETACHED_PROCESS
// must NOT be used instead: it leaves dolt with no console at all, and dolt
// runs `uname -s/-r/-m` on every start, so each of those gets a fresh visible
// console — three Windows Terminal windows per server start when Windows
// Terminal is the registered default terminal.
func TestManagedServerSysProcAttrHidesConsole(t *testing.T) {
	attr := managedServerSysProcAttr()
	if attr == nil {
		t.Fatal("managedServerSysProcAttr() = nil on windows, want non-nil")
	}
	if attr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Errorf("CreationFlags = %#x, missing CREATE_NO_WINDOW (%#x)",
			attr.CreationFlags, uint32(windows.CREATE_NO_WINDOW))
	}
	if attr.CreationFlags&windows.DETACHED_PROCESS != 0 {
		t.Errorf("CreationFlags = %#x, must not include DETACHED_PROCESS (%#x): dolt's console children would each open a window",
			attr.CreationFlags, uint32(windows.DETACHED_PROCESS))
	}
	if attr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CreationFlags = %#x, missing CREATE_NEW_PROCESS_GROUP (%#x)",
			attr.CreationFlags, uint32(windows.CREATE_NEW_PROCESS_GROUP))
	}
}
