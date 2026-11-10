package execwin

// Tests for the console-suppression policy. The policy core
// (hideCreationFlags, backgroundCreationFlags) is a pure seam so it runs
// and fails on every platform; the Windows-only halves of the behavior are
// pinned by execwin_windows_test.go, and the end-to-end subprocess
// evidence lives in console_spawn_subprocess_windows_test.go.

import (
	"os/exec"
	"runtime"
	"testing"
)

// newHiddenPolicyCmd builds a Cmd that no test ever starts: the pointer is
// only passed through Hide to pin its attribute policy. "go" because bd
// itself shells out to the go toolchain (bd doctor, bd preflight), so the
// command is a realistic short-lived spawn; the host-tool exec policy
// allowlists it in scripts/host_tool_exec_policy_test.go as never started.
func newHiddenPolicyCmd() *exec.Cmd {
	return exec.Command("go", "version")
}

func TestHidePinsNoWindowWhenUnattached(t *testing.T) {
	merged, changed := hideCreationFlags(false, 0)
	if !changed {
		t.Fatal("hideCreationFlags(false, 0) changed = false, want an unattached parent with no caller attributes to gain CREATE_NO_WINDOW")
	}
	if merged != flagCreateNoWindow {
		t.Fatalf("hideCreationFlags(false, 0) = %#x, want exactly CREATE_NO_WINDOW (%#x)", merged, flagCreateNoWindow)
	}
}

func TestHideLeavesAttachedConsoleAlone(t *testing.T) {
	// An attached parent must keep everything it set, whatever flags it
	// chose: the child inherits the console it can already see.
	for _, flags := range []uint32{0, flagCreateNewProcessGroup, flagCreateNoWindow, flagDetachedProcess} {
		merged, changed := hideCreationFlags(true, flags)
		if changed || merged != flags {
			t.Errorf("hideCreationFlags(true, %#x) = (%#x, %v), want the caller's flags unchanged", flags, merged, changed)
		}
	}
}

func TestHideOffWindowsIsIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows behavior is pinned by execwin_windows_test.go")
	}
	dir := t.TempDir()
	cmd := newHiddenPolicyCmd()
	cmd.Dir = dir
	cmd.Env = []string{"EXECWIN_TEST=1"}
	returned := Hide(cmd)
	if returned != cmd {
		t.Fatalf("Hide returned a different command (%p != %p); off Windows it must return the same one", returned, cmd)
	}
	if returned.Dir != dir || len(returned.Env) != 1 || returned.Env[0] != "EXECWIN_TEST=1" {
		t.Fatalf("Hide changed unrelated command configuration: Dir=%q Env=%v", returned.Dir, returned.Env)
	}
	if cmd.SysProcAttr != nil {
		t.Fatalf("Hide set SysProcAttr = %+v off Windows; it must not touch attributes there", cmd.SysProcAttr)
	}
}

func TestBackgroundSysProcAttrCreationFlags(t *testing.T) {
	flags := backgroundCreationFlags()
	if flags != flagCreateNoWindow|flagCreateNewProcessGroup {
		t.Fatalf("backgroundCreationFlags() = %#x, want CREATE_NO_WINDOW|CREATE_NEW_PROCESS_GROUP (%#x)", flags, flagCreateNoWindow|flagCreateNewProcessGroup)
	}
}

func TestBackgroundSysProcAttrOffWindowsIsNil(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows half returns real attributes; pinned by execwin_windows_test.go")
	}
	if attr := BackgroundSysProcAttr(); attr != nil {
		t.Fatalf("BackgroundSysProcAttr() = %+v, want nil off Windows so the spawn site is unchanged there", attr)
	}
}
