//go:build windows

package execwin

import (
	"os/exec"
	"syscall"
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
		{"CREATE_NEW_PROCESS_GROUP", createNewProcessGroup, windows.CREATE_NEW_PROCESS_GROUP},
		{"CREATE_NO_WINDOW", createNoWindow, windows.CREATE_NO_WINDOW},
	} {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// forceConsoleAttached pins the console probe to attached for the rest of
// the test. Not for parallel tests: consoleAttached is package state.
func forceConsoleAttached(t *testing.T, attached bool) {
	t.Helper()
	orig := consoleAttached
	consoleAttached = func() bool { return attached }
	t.Cleanup(func() { consoleAttached = orig })
}

// TestHideUnattachedNilAttr covers the path the call sites use from a
// console-less bd: the same command comes back carrying exactly
// CREATE_NO_WINDOW.
func TestHideUnattachedNilAttr(t *testing.T) {
	forceConsoleAttached(t, false)
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

// TestHideAttachedLeavesCommandUnchanged: a bd with a console leaves its
// children to inherit it, so Hide must not touch the command at all.
func TestHideAttachedLeavesCommandUnchanged(t *testing.T) {
	forceConsoleAttached(t, true)

	cmd := exec.Command("cmd", "/c", "ver")
	if got := Hide(cmd); got != cmd || cmd.SysProcAttr != nil {
		t.Fatalf("Hide on an attached parent: returned %p (want %p), SysProcAttr = %+v (want nil)", got, cmd, cmd.SysProcAttr)
	}

	existing := &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	cmd = exec.Command("cmd", "/c", "ver")
	cmd.SysProcAttr = existing
	cmd.Dir = `C:\`
	Hide(cmd)
	if cmd.SysProcAttr != existing {
		t.Fatalf("Hide replaced the caller's SysProcAttr (%p) with %p on an attached parent", existing, cmd.SysProcAttr)
	}
	if !existing.HideWindow || existing.CreationFlags != windows.CREATE_NEW_PROCESS_GROUP || cmd.Dir != `C:\` {
		t.Fatalf("Hide changed the command on an attached parent: attr %+v, dir %q", *existing, cmd.Dir)
	}
}
