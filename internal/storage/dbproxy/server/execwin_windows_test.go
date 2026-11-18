//go:build windows

package server_test

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/steveyegge/beads/internal/execwin"
)

// TestExecwinHideOnWindows pins AC-1 (GH#6076) in a package the Windows CI leg
// (test-windows-small) already runs natively; internal/execwin itself is not
// in that leg. This package's managed dolt sql-server and its dolt/git helper
// spawns all go through execwin.Hide, and the db-proxy child that runs them is
// DETACHED_PROCESS, so without CREATE_NO_WINDOW each one opens a console
// window (and a short-lived `dolt init` can fail with 0x800700E8).
func TestExecwinHideOnWindows(t *testing.T) {
	const never = "execwin-fixture-never-started"
	t.Run("nil SysProcAttr gains CREATE_NO_WINDOW", func(t *testing.T) {
		cmd := exec.Command(never)
		for i := 0; i < 2; i++ {
			if got := execwin.Hide(cmd); got != cmd {
				t.Fatalf("Hide call %d returned %p, want the same *exec.Cmd %p", i+1, got, cmd)
			}
			if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags != windows.CREATE_NO_WINDOW {
				t.Fatalf("after Hide call %d SysProcAttr = %+v, want CreationFlags %#x", i+1, cmd.SysProcAttr, uint32(windows.CREATE_NO_WINDOW))
			}
		}
	})
	t.Run("CREATE_NEW_PROCESS_GROUP and HideWindow are preserved", func(t *testing.T) {
		attr := &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
		cmd := exec.Command(never)
		cmd.SysProcAttr = attr
		for i := 0; i < 2; i++ {
			if got := execwin.Hide(cmd); got != cmd {
				t.Fatalf("Hide call %d returned %p, want the same *exec.Cmd %p", i+1, got, cmd)
			}
			want := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
			if cmd.SysProcAttr != attr || attr.CreationFlags != want || !attr.HideWindow {
				t.Fatalf("after Hide call %d SysProcAttr = %p %+v, want %p with CreationFlags %#x and HideWindow", i+1, cmd.SysProcAttr, cmd.SysProcAttr, attr, want)
			}
		}
	})
	t.Run("a hidden console child runs and exits 0", func(t *testing.T) {
		shell := os.Getenv("ComSpec")
		if shell == "" {
			shell = "cmd.exe"
		}
		out, err := execwin.Hide(exec.Command(shell, "/c", "echo", "hidden")).CombinedOutput()
		if err != nil {
			t.Fatalf("hidden %s /c echo: %v\n%s", shell, err, out)
		}
	})
}
