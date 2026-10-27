//go:build windows

package execwin

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// TestNoWindowSysProcAttrSetsCreateNoWindow pins the flag itself. If this ever
// regresses to zero, every dolt/git/PowerShell command bd runs from a
// console-less parent starts allocating a visible console again — which is the
// defect this package exists to prevent, and which is invisible in CI on Linux.
func TestNoWindowSysProcAttrSetsCreateNoWindow(t *testing.T) {
	attr := NoWindowSysProcAttr()
	if attr == nil {
		t.Fatal("NoWindowSysProcAttr() = nil on windows, want non-nil")
	}
	if attr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("CreationFlags = %#x, missing CREATE_NO_WINDOW (%#x)",
			attr.CreationFlags, uint32(windows.CREATE_NO_WINDOW))
	}
}

// TestHideAppliesCreateNoWindow covers the path the call sites actually use.
func TestHideAppliesCreateNoWindow(t *testing.T) {
	cmd := Hide(exec.Command("cmd", "/c", "ver"))
	if cmd.SysProcAttr == nil {
		t.Fatal("Hide left SysProcAttr nil on windows")
	}
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("CreationFlags = %#x, missing CREATE_NO_WINDOW", cmd.SysProcAttr.CreationFlags)
	}
}
