//go:build windows

package execwin

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// TestHideSetsCreateNoWindowOnWindows pins AC-1 inside the package: a nil
// SysProcAttr gains exactly CREATE_NO_WINDOW; an existing one keeps every flag
// and field the caller set, gains CREATE_NO_WINDOW, and stays the same
// pointer; a second call changes nothing. If this regresses, every dolt/git/
// PowerShell command a console-less bd runs allocates a visible console again
// (GH#6076), which no Linux CI lane can see.
func TestHideSetsCreateNoWindowOnWindows(t *testing.T) {
	t.Run("nil SysProcAttr", func(t *testing.T) {
		cmd := exec.Command(hideFixtureTool)
		for i := 0; i < 2; i++ {
			if got := Hide(cmd); got != cmd {
				t.Fatalf("Hide call %d returned %p, want the same *exec.Cmd %p", i+1, got, cmd)
			}
			if cmd.SysProcAttr == nil {
				t.Fatalf("Hide call %d left SysProcAttr nil", i+1)
			}
			if got, want := cmd.SysProcAttr.CreationFlags, uint32(windows.CREATE_NO_WINDOW); got != want {
				t.Fatalf("after Hide call %d CreationFlags = %#x, want %#x", i+1, got, want)
			}
		}
	})
	t.Run("existing flags and fields are preserved", func(t *testing.T) {
		attr := &syscall.SysProcAttr{
			CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
			HideWindow:    true,
			CmdLine:       "fixture --flag",
		}
		cmd := exec.Command(hideFixtureTool)
		cmd.SysProcAttr = attr
		for i := 0; i < 2; i++ {
			if got := Hide(cmd); got != cmd {
				t.Fatalf("Hide call %d returned %p, want the same *exec.Cmd %p", i+1, got, cmd)
			}
			if cmd.SysProcAttr != attr {
				t.Fatalf("Hide call %d replaced SysProcAttr %p with %p", i+1, attr, cmd.SysProcAttr)
			}
			want := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
			if got := attr.CreationFlags; got != want {
				t.Fatalf("after Hide call %d CreationFlags = %#x, want %#x", i+1, got, want)
			}
			if !attr.HideWindow || attr.CmdLine != "fixture --flag" {
				t.Fatalf("after Hide call %d a caller field changed: HideWindow=%v CmdLine=%q", i+1, attr.HideWindow, attr.CmdLine)
			}
		}
	})
}
