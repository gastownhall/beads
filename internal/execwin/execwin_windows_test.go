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
		{"DETACHED_PROCESS", detachedProcess, windows.DETACHED_PROCESS},
		{"CREATE_NEW_CONSOLE", createNewConsole, windows.CREATE_NEW_CONSOLE},
		{"CREATE_NEW_PROCESS_GROUP", createNewProcessGroup, windows.CREATE_NEW_PROCESS_GROUP},
		{"CREATE_UNICODE_ENVIRONMENT", createUnicodeEnvironment, windows.CREATE_UNICODE_ENVIRONMENT},
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

// TestHideUnattachedMergesCallerAttrOnACopy: a caller-owned SysProcAttr with
// no console choice gets CREATE_NO_WINDOW on a copy that keeps every field and
// flag, and the caller's structure (which it may share with other commands) is
// not written to.
func TestHideUnattachedMergesCallerAttrOnACopy(t *testing.T) {
	forceConsoleAttached(t, false)

	token := syscall.Token(42)
	existing := &syscall.SysProcAttr{
		HideWindow:                 true,
		CmdLine:                    `cmd /c ver`,
		CreationFlags:              windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_UNICODE_ENVIRONMENT,
		Token:                      token,
		NoInheritHandles:           true,
		AdditionalInheritedHandles: []syscall.Handle{7},
		ParentProcess:              syscall.Handle(9),
	}
	before := *existing
	cmd := exec.Command("cmd", "/c", "ver")
	cmd.SysProcAttr = existing

	if got := Hide(cmd); got != cmd {
		t.Fatalf("Hide returned %p, want the command it was given (%p)", got, cmd)
	}
	if cmd.SysProcAttr == existing {
		t.Fatal("Hide wrote into the caller's SysProcAttr instead of a copy")
	}
	if existing.CreationFlags != before.CreationFlags || existing.HideWindow != before.HideWindow ||
		existing.CmdLine != before.CmdLine || existing.Token != before.Token {
		t.Fatalf("caller's SysProcAttr changed: got %+v, want %+v", *existing, before)
	}
	got := *cmd.SysProcAttr
	want := before
	want.CreationFlags |= windows.CREATE_NO_WINDOW
	if got.CreationFlags != want.CreationFlags {
		t.Fatalf("CreationFlags = %#x, want %#x", got.CreationFlags, want.CreationFlags)
	}
	if got.HideWindow != want.HideWindow || got.CmdLine != want.CmdLine || got.Token != want.Token ||
		got.NoInheritHandles != want.NoInheritHandles || got.ParentProcess != want.ParentProcess ||
		len(got.AdditionalInheritedHandles) != 1 || got.AdditionalInheritedHandles[0] != 7 {
		t.Fatalf("copy lost a field: got %+v, want %+v", got, want)
	}
}

// TestHideKeepsExplicitConsoleChoices: a caller that already decided the
// child's console (hidden, none, or a new visible one) keeps its structure.
func TestHideKeepsExplicitConsoleChoices(t *testing.T) {
	forceConsoleAttached(t, false)
	for _, flags := range []uint32{
		windows.CREATE_NO_WINDOW,
		windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		windows.CREATE_NEW_CONSOLE,
	} {
		existing := &syscall.SysProcAttr{CreationFlags: flags}
		cmd := exec.Command("cmd", "/c", "ver")
		cmd.SysProcAttr = existing
		Hide(cmd)
		if cmd.SysProcAttr != existing || existing.CreationFlags != flags {
			t.Errorf("flags %#x: Hide replaced or changed the caller's choice (now %p, %#x)",
				flags, cmd.SysProcAttr, cmd.SysProcAttr.CreationFlags)
		}
	}
}
