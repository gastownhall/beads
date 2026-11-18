//go:build windows

package server

import (
	"os/exec"
	"syscall"
	"testing"

	"github.com/steveyegge/beads/internal/execwin"
	"golang.org/x/sys/windows"
)

// TestHideOrsCreateNoWindowIntoCreationFlags pins execwin.Hide's Windows
// contract, the fix for gastownhall/beads#6076: CREATE_NO_WINDOW lands in
// CreationFlags, flags and fields already set survive, the command stays the
// same object, and a hidden child of this test process actually runs. This
// package is one of the two test-windows-small runs natively
// (.github/workflows/pr.yml), so these assertions run on real Windows in CI;
// the off-Windows no-op is pinned in internal/execwin/execwin_test.go.
func TestHideOrsCreateNoWindowIntoCreationFlags(t *testing.T) {
	t.Run("nil SysProcAttr is allocated with CREATE_NO_WINDOW", func(t *testing.T) {
		cmd := exec.Command("execwin-fixture-never-started", "version")
		got := execwin.Hide(cmd)
		if got != cmd {
			t.Fatalf("Hide returned %p, want the same *exec.Cmd (%p)", got, cmd)
		}
		if cmd.SysProcAttr == nil {
			t.Fatal("SysProcAttr still nil: Hide must allocate one to carry CREATE_NO_WINDOW")
		}
		if cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
			t.Fatalf("CreationFlags = %#x, want CREATE_NO_WINDOW (%#x) set", cmd.SysProcAttr.CreationFlags, windows.CREATE_NO_WINDOW)
		}
	})

	t.Run("existing flags and fields are preserved", func(t *testing.T) {
		cmd := exec.Command("execwin-fixture-never-started", "version")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
			HideWindow:    true,
		}
		execwin.Hide(cmd)
		if got, want := cmd.SysProcAttr.CreationFlags, uint32(syscall.CREATE_NEW_PROCESS_GROUP|windows.CREATE_NO_WINDOW); got != want {
			t.Fatalf("CreationFlags = %#x, want %#x (CREATE_NEW_PROCESS_GROUP|CREATE_NO_WINDOW): OR, not replace", got, want)
		}
		if !cmd.SysProcAttr.HideWindow {
			t.Error("HideWindow = false, want true: Hide must preserve unrelated SysProcAttr fields")
		}
	})

	t.Run("applying Hide twice is idempotent", func(t *testing.T) {
		cmd := exec.Command("execwin-fixture-never-started", "version")
		execwin.Hide(execwin.Hide(cmd))
		if got, want := cmd.SysProcAttr.CreationFlags, uint32(windows.CREATE_NO_WINDOW); got != want {
			t.Fatalf("CreationFlags = %#x, want %#x after two hides", got, want)
		}
	})

	t.Run("a hidden child of this test process runs and exits 0", func(t *testing.T) {
		cmd := execwin.Hide(exec.Command("cmd.exe", "/c", "exit", "0"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hidden child did not run: %v\noutput: %s", err, out)
		}
	})
}
