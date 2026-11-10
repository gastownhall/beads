//go:build windows

package execwin

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// TestCreationFlagConstantsMatchWindows pins the constants duplicated in
// execwin.go (so the policy tests run on every platform) to the real ones.
func TestCreationFlagConstantsMatchWindows(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uint32
	}{
		{"CREATE_NO_WINDOW", createNoWindow, windows.CREATE_NO_WINDOW},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestHideUnattachedNilAttr covers the path the call sites use from a
// console-less bd: the same command comes back carrying exactly
// CREATE_NO_WINDOW.
func TestHideUnattachedNilAttr(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "ver")
	if got := Hide(cmd); got != cmd {
		t.Fatalf("Hide returned %p, want the command it was given (%p)", got, cmd)
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("Hide left SysProcAttr nil on an unattached windows parent")
	}
	if cmd.SysProcAttr.CreationFlags != windows.CREATE_NO_WINDOW {
		t.Fatalf("CreationFlags = %#x, want exactly CREATE_NO_WINDOW (%#x)",
			cmd.SysProcAttr.CreationFlags, uint32(windows.CREATE_NO_WINDOW))
	}
}
