package execwin_test

import (
	"os/exec"
	"runtime"
	"syscall"
	"testing"

	"github.com/steveyegge/beads/internal/execwin"
)

// TestHideOffWindowsNoOp pins Hide's off-Windows contract: the same
// *exec.Cmd pointer comes back and SysProcAttr is never touched — nil stays
// nil, a pre-set attr stays the identical pointer. The Windows behavior
// (CREATE_NO_WINDOW ORed in, existing flags preserved, idempotence, a hidden
// child that runs) is pinned by TestHideOrsCreateNoWindowIntoCreationFlags
// in internal/storage/dbproxy/server, which the test-windows-small lane runs
// natively; this file skips there.
func TestHideOffWindowsNoOp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows behavior is asserted by execwin_hide_windows_test.go in internal/storage/dbproxy/server")
	}
	t.Run("nil SysProcAttr stays nil", func(t *testing.T) {
		cmd := exec.Command("execwin-fixture-never-started", "version")
		got := execwin.Hide(cmd)
		if got != cmd {
			t.Fatalf("Hide returned %p, want the same *exec.Cmd (%p)", got, cmd)
		}
		if cmd.SysProcAttr != nil {
			t.Fatalf("SysProcAttr = %+v, want nil: Hide must not allocate one off Windows", cmd.SysProcAttr)
		}
	})
	t.Run("pre-set SysProcAttr stays the identical pointer", func(t *testing.T) {
		cmd := exec.Command("execwin-fixture-never-started", "version")
		attr := &syscall.SysProcAttr{}
		cmd.SysProcAttr = attr
		got := execwin.Hide(cmd)
		if got != cmd {
			t.Fatalf("Hide returned %p, want the same *exec.Cmd (%p)", got, cmd)
		}
		if got.SysProcAttr != attr {
			t.Fatalf("SysProcAttr pointer changed: got %p, want the identical %p", got.SysProcAttr, attr)
		}
	})
}
