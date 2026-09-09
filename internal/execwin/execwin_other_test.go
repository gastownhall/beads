//go:build !windows

package execwin

import (
	"os/exec"
	"testing"
)

// TestNoWindowSysProcAttrIsNilOffWindows pins the no-op guarantee. Hide is
// called from shared (non-build-tagged) code, so it must not perturb process
// creation on Unix: a nil SysProcAttr is exactly what those call sites had
// before execwin existed.
func TestNoWindowSysProcAttrIsNilOffWindows(t *testing.T) {
	if attr := NoWindowSysProcAttr(); attr != nil {
		t.Fatalf("NoWindowSysProcAttr() = %+v, want nil off windows", attr)
	}
}

// TestHideLeavesSysProcAttrNil is the same guarantee observed through Hide,
// which is the form the call sites use.
func TestHideLeavesSysProcAttrNil(t *testing.T) {
	cmd := Hide(exec.Command("true"))
	if cmd.SysProcAttr != nil {
		t.Fatalf("Hide set SysProcAttr = %+v off windows, want nil", cmd.SysProcAttr)
	}
}
