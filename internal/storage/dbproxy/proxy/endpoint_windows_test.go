//go:build windows

package proxy

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestProcAttrDetachedHidesConsole pins the Windows creation flags of
// bd db-proxy-child.
//
// The proxy child is the root of every dolt, git and PowerShell process the
// proxied server path starts, so its console decides what they inherit.
// CREATE_NO_WINDOW gives it a console of its own that is never displayed;
// DETACHED_PROCESS must NOT be used instead, because it leaves the child with
// no console and every console program anywhere below it — including the
// `uname` calls dolt makes on every start — then gets a fresh visible one.
func TestProcAttrDetachedHidesConsole(t *testing.T) {
	attr := procAttrDetached()
	if attr == nil {
		t.Fatal("procAttrDetached() = nil on windows, want non-nil")
	}
	if attr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Errorf("CreationFlags = %#x, missing CREATE_NO_WINDOW (%#x)",
			attr.CreationFlags, uint32(windows.CREATE_NO_WINDOW))
	}
	if attr.CreationFlags&windows.DETACHED_PROCESS != 0 {
		t.Errorf("CreationFlags = %#x, must not include DETACHED_PROCESS (%#x): every console program below the proxy child would open a window",
			attr.CreationFlags, uint32(windows.DETACHED_PROCESS))
	}
	if attr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CreationFlags = %#x, missing CREATE_NEW_PROCESS_GROUP (%#x)",
			attr.CreationFlags, uint32(windows.CREATE_NEW_PROCESS_GROUP))
	}
}
