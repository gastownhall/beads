//go:build windows

package execwin

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Hide ORs CREATE_NO_WINDOW into cmd's creation flags and returns cmd, so it
// can wrap an exec.Command call inline:
//
//	out, err := execwin.Hide(exec.Command("git", "config", "user.name")).Output()
//
// A nil SysProcAttr is allocated. An existing one is updated in place: every
// other creation flag (CREATE_NEW_PROCESS_GROUP, ...) and field (HideWindow,
// CmdLine, ...) the caller set is preserved, and calling Hide again changes
// nothing. A nil cmd is returned unchanged.
//
// See the package comment for why Hide must not be used on a command that
// carries DETACHED_PROCESS or CREATE_NEW_CONSOLE.
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
