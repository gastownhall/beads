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
		{
			// An attached console is inherited, and so is foreground
			// behavior: prompts, the terminal, and Ctrl+C reaching the
			// child. Nothing to suppress, nothing to change.
			name:      "attached parent, nil attributes: unchanged",
			flags:     0,
			attached:  true,
			wantFlags: 0,
			wantApply: false,
		},
		{
			name:      "attached parent, caller flags: unchanged",
			flags:     createNewProcessGroup,
			attached:  true,
			wantFlags: createNewProcessGroup,
			wantApply: false,
		},
		{
			name:      "unattached parent, caller flags without a console choice: CREATE_NO_WINDOW added, rest kept",
			flags:     createNewProcessGroup | createUnicodeEnvironment,
			attached:  false,
			wantFlags: createNewProcessGroup | createUnicodeEnvironment | createNoWindow,
			wantApply: true,
		},
		{
			name:      "unattached parent, already hidden: unchanged",
			flags:     createNoWindow | createNewProcessGroup,
			attached:  false,
			wantFlags: createNoWindow | createNewProcessGroup,
			wantApply: false,
		},
		{
			// CREATE_NO_WINDOW is ignored alongside DETACHED_PROCESS, and
			// OR-ing it in would only hide that the caller chose no console.
			name:      "unattached parent, DETACHED_PROCESS chosen: unchanged",
			flags:     detachedProcess,
			attached:  false,
			wantFlags: detachedProcess,
			wantApply: false,
		},
		{
			name:      "unattached parent, CREATE_NEW_CONSOLE chosen: unchanged",
			flags:     createNewConsole | createNewProcessGroup,
			attached:  false,
			wantFlags: createNewConsole | createNewProcessGroup,
			wantApply: false,
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

// TestBackgroundCreationFlags pins, on every platform, the flags
// BackgroundSysProcAttr gives the long-lived children on Windows: a hidden
// console of their own and their own process group, and never no console.
func TestBackgroundCreationFlags(t *testing.T) {
	if backgroundCreationFlags != createNoWindow|createNewProcessGroup {
		t.Fatalf("backgroundCreationFlags = %#x, want CREATE_NO_WINDOW|CREATE_NEW_PROCESS_GROUP (%#x)",
			backgroundCreationFlags, createNoWindow|createNewProcessGroup)
	}
	if backgroundCreationFlags&(detachedProcess|createNewConsole) != 0 {
		t.Fatalf("backgroundCreationFlags = %#x includes DETACHED_PROCESS or CREATE_NEW_CONSOLE", backgroundCreationFlags)
	}
}
