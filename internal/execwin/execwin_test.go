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
