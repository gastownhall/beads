//go:build windows

package execwin

// Windows-only pins for Hide and BackgroundSysProcAttr. The attachment
// probe is a var seam (attachedToConsole) so both of its outcomes can be
// forced without detaching the test runner's own console; tests that
// override it must not run in parallel with each other.

import (
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// withConsoleAttachment forces the attachment probe for the duration of
// the test and restores it afterwards.
func withConsoleAttachment(t *testing.T, attached bool) {
	t.Helper()
	prev := attachedToConsole
	attachedToConsole = func() bool { return attached }
	t.Cleanup(func() { attachedToConsole = prev })
}

func TestHideWindowsConstantEquality(t *testing.T) {
	// The policy constants are duplicated in policy.go so the core is
	// testable off Windows; they must not drift from x/sys/windows.
	for name, pair := range map[string][2]uint32{
		"CREATE_NO_WINDOW":         {flagCreateNoWindow, windows.CREATE_NO_WINDOW},
		"DETACHED_PROCESS":         {flagDetachedProcess, windows.DETACHED_PROCESS},
		"CREATE_NEW_CONSOLE":       {flagCreateNewConsole, windows.CREATE_NEW_CONSOLE},
		"CREATE_NEW_PROCESS_GROUP": {flagCreateNewProcessGroup, windows.CREATE_NEW_PROCESS_GROUP},
	} {
		if pair[0] != pair[1] {
			t.Errorf("policy.go flag %s = %#x, want the x/sys/windows value %#x", name, pair[0], pair[1])
		}
	}
}

func TestHideWindowsUnattachedNilAttributes(t *testing.T) {
	withConsoleAttachment(t, false)
	cmd := newHiddenPolicyCmd()
	returned := Hide(cmd)
	if returned != cmd {
		t.Fatalf("Hide returned a different command (%p != %p); it must return the same one", returned, cmd)
	}
	if returned.SysProcAttr == nil {
		t.Fatal("Hide left SysProcAttr nil for an unattached parent; the child would allocate a visible console")
	}
	if returned.SysProcAttr.CreationFlags != windows.CREATE_NO_WINDOW {
		t.Fatalf("Hide set CreationFlags = %#x, want exactly CREATE_NO_WINDOW (%#x)", returned.SysProcAttr.CreationFlags, windows.CREATE_NO_WINDOW)
	}
}

func TestHideWindowsAttachedLeavesCommandAlone(t *testing.T) {
	withConsoleAttachment(t, true)
	cmd := newHiddenPolicyCmd()
	dir := t.TempDir()
	cmd.Dir = dir
	cmd.Env = []string{"EXECWIN_TEST=1"}
	returned := Hide(cmd)
	if returned != cmd {
		t.Fatalf("Hide returned a different command (%p != %p); an attached parent must get the same one", returned, cmd)
	}
	if returned.Dir != dir || len(returned.Env) != 1 || returned.Env[0] != "EXECWIN_TEST=1" {
		t.Fatalf("Hide changed unrelated command configuration: Dir=%q Env=%v", returned.Dir, returned.Env)
	}
	if cmd.SysProcAttr != nil {
		t.Fatalf("Hide set SysProcAttr = %+v for an attached parent; the attribute pointer and values must stay unchanged (nil)", cmd.SysProcAttr)
	}
}

func TestHideWindowsMergesCallerAttributesOnACopy(t *testing.T) {
	withConsoleAttachment(t, false)
	original := &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
	cmd := newHiddenPolicyCmd()
	cmd.SysProcAttr = original
	returned := Hide(cmd)
	if returned != cmd {
		t.Fatalf("Hide returned a different command (%p != %p); it must return the same one", returned, cmd)
	}
	if returned.SysProcAttr == original {
		t.Fatal("Hide merged into the caller's SysProcAttr in place; it must work on a copy so no caller-owned struct is mutated")
	}
	if original.CreationFlags != windows.CREATE_NEW_PROCESS_GROUP || !original.HideWindow {
		t.Fatalf("Hide changed the caller's original SysProcAttr: %+v", original)
	}
	attr := returned.SysProcAttr
	if attr.CreationFlags != windows.CREATE_NO_WINDOW|windows.CREATE_NEW_PROCESS_GROUP {
		t.Fatalf("Hide set CreationFlags = %#x, want CREATE_NEW_PROCESS_GROUP preserved plus CREATE_NO_WINDOW (%#x)", attr.CreationFlags, windows.CREATE_NO_WINDOW|windows.CREATE_NEW_PROCESS_GROUP)
	}
	if !attr.HideWindow {
		t.Fatal("Hide dropped the caller's HideWindow field while copying; every existing field must survive the merge")
	}
}

func TestHideWindowsKeepsExplicitConsoleChoice(t *testing.T) {
	withConsoleAttachment(t, false)
	for _, flags := range []uint32{windows.DETACHED_PROCESS, windows.CREATE_NEW_CONSOLE, windows.CREATE_NO_WINDOW} {
		cmd := newHiddenPolicyCmd()
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		attr := cmd.SysProcAttr
		returned := Hide(cmd)
		if returned != cmd || returned.SysProcAttr != attr {
			t.Fatalf("flags %#x: Hide replaced the attribute pointer; an explicit console choice must come back untouched", flags)
		}
		if attr.CreationFlags != flags {
			t.Fatalf("flags %#x: Hide rewrote them to %#x; an explicit console choice must be left alone", flags, attr.CreationFlags)
		}
	}
}

func TestBackgroundSysProcAttrWindows(t *testing.T) {
	attr := BackgroundSysProcAttr()
	if attr == nil {
		t.Fatal("BackgroundSysProcAttr() = nil on Windows; the long-lived children need pinned flags")
	}
	if attr.CreationFlags != windows.CREATE_NO_WINDOW|windows.CREATE_NEW_PROCESS_GROUP {
		t.Fatalf("BackgroundSysProcAttr().CreationFlags = %#x, want CREATE_NO_WINDOW|CREATE_NEW_PROCESS_GROUP (%#x)", attr.CreationFlags, windows.CREATE_NO_WINDOW|windows.CREATE_NEW_PROCESS_GROUP)
	}
	if attr.CreationFlags&windows.DETACHED_PROCESS != 0 {
		t.Fatal("BackgroundSysProcAttr() includes DETACHED_PROCESS; a console-less child makes every console program it spawns open a visible window")
	}
	first, second := BackgroundSysProcAttr(), BackgroundSysProcAttr()
	if first == second {
		t.Fatal("BackgroundSysProcAttr() returned the same pointer twice; each call must allocate fresh attributes no caller can share")
	}
}
