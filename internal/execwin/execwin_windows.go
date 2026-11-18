//go:build windows

package execwin

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Hide returns cmd with CREATE_NO_WINDOW ORed into its creation flags, so a
// child of a console-less bd (redirected stdout, DETACHED_PROCESS parent)
// does not make Windows allocate a visible console window for it
// (gastownhall/beads#6076).
//
// A nil cmd.SysProcAttr is allocated first; every flag and field already set
// (CREATE_NEW_PROCESS_GROUP, HideWindow, ...) is preserved, and calling Hide
// twice is idempotent.
//
// Do not use Hide on a detached command: CREATE_NO_WINDOW is ignored next to
// DETACHED_PROCESS (and CREATE_NEW_CONSOLE), and a detached child has no
// console to hide anyway. Leave a DETACHED_PROCESS child alone and hide the
// console children IT spawns instead.
func Hide(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
	return cmd
}
