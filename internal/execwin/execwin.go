// Package execwin centralizes the Windows-only process creation flags beads
// needs when it shells out to dolt, git, or PowerShell.
//
// The problem it solves is Windows-specific and invisible on Unix. A
// console-subsystem child (dolt.exe, git.exe, powershell.exe) started from a
// parent that has NO console of its own does not silently inherit one — the
// system allocates a brand new console for it. When Windows Terminal is
// registered as the default terminal application ("defterm"), that console is
// handed to Windows Terminal and a real terminal window appears on the user's
// desktop.
//
// beads hits this on its most common path: bd db-proxy-child is deliberately
// spawned with DETACHED_PROCESS (see dbproxy/proxy/endpoint_windows.go), so it
// has no console, and every dolt command it then runs would pop a window. For
// short-lived commands the window is worse than cosmetic: the child exits
// before Windows Terminal finishes attaching to the pseudoconsole, so the
// handoff pipe is already closed and Windows Terminal reports
//
//	[error 2147942632 (0x800700E8) when launching `"...\dolt.exe" init']
//
// where 0x800700E8 is HRESULT_FROM_WIN32(ERROR_NO_DATA), "The pipe is being
// closed." Windows Terminal may then persist that session and replay it from
// C:\Windows\System32 on subsequent launches.
//
// Hide is a no-op on every non-Windows platform: NoWindowSysProcAttr returns
// nil there, and Hide only ever assigns to a nil SysProcAttr, so behavior off
// Windows is byte-identical to not calling it at all.
package execwin

import "os/exec"

// Hide suppresses console-window allocation for cmd on Windows and returns cmd
// so it can be wrapped around an exec.Command call inline:
//
//	out, err := execwin.Hide(exec.Command("git", "config", "user.name")).Output()
//
// It is intended for short-lived commands whose output the caller captures. For
// a long-lived child that must outlive its parent, use that package's own
// detached SysProcAttr instead — those need DETACHED_PROCESS, and the Unix side
// needs Setsid/Setpgid, which is not something this helper models.
//
// Hide never overwrites a SysProcAttr the caller already set, so it is safe to
// call on a command that has been configured elsewhere. A nil cmd is returned
// unchanged.
func Hide(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = NoWindowSysProcAttr()
	}
	return cmd
}
