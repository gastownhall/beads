package execwin

import (
	"os/exec"
	"testing"
)

// TestHideNil guards the convenience contract: Hide is meant to be wrapped
// around an exec.Command call inline, so it must tolerate a nil command rather
// than panicking at the call site.
func TestHideNil(t *testing.T) {
	if got := Hide(nil); got != nil {
		t.Fatalf("Hide(nil) = %v, want nil", got)
	}
}

// TestHideReturnsSameCommand documents that Hide hands the command it was
// given back, which is what makes execwin.Hide(exec.Command(...)).Output()
// legal. The command is never started.
func TestHideReturnsSameCommand(t *testing.T) {
	cmd := exec.Command("dolt", "version")
	if got := Hide(cmd); got != cmd {
		t.Fatalf("Hide returned a different *exec.Cmd (%p) than it was given (%p)", got, cmd)
	}
}

// TestHidePolicy pins the platform-neutral decision Hide makes on Windows, so
// it runs (and fails) on every CI platform rather than only on a Windows
// runner. hiddenCreationFlags gets the creation flags a command already has
// (zero for a nil SysProcAttr) and whether bd itself has a console attached.
func TestHidePolicy(t *testing.T) {
	cases := []struct {
		name      string
		flags     uint32
		attached  bool
		wantFlags uint32
		wantApply bool
	}{
		{
			name:      "unattached parent, nil attributes: exactly CREATE_NO_WINDOW",
			flags:     0,
			attached:  false,
			wantFlags: createNoWindow,
			wantApply: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotFlags, gotApply := hiddenCreationFlags(tc.flags, tc.attached)
			if gotFlags != tc.wantFlags || gotApply != tc.wantApply {
				t.Fatalf("hiddenCreationFlags(%#x, attached=%v) = (%#x, %v), want (%#x, %v)",
					tc.flags, tc.attached, gotFlags, gotApply, tc.wantFlags, tc.wantApply)
			}
		})
	}
}
