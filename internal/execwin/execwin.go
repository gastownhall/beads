// Package execwin keeps the console-subsystem commands bd starts (dolt, git,
// gh, PowerShell, hooks) from allocating a visible console window on Windows.
//
// The problem is Windows-specific and invisible on Unix. A console-subsystem
// child started from a parent that has NO console of its own does not inherit
// one: the system allocates a brand new console for it. When Windows Terminal
// is registered as the default terminal application ("defterm"), that console
// is handed to Windows Terminal and a real terminal window appears on the
// user's desktop.
//
// bd hits this whenever it runs without a console (an MCP server, a
// SessionStart hook, an agent harness with redirected stdio): every dolt or
// git command it then runs would pop a window. For short-lived commands the
// window is worse than cosmetic: the child exits before Windows Terminal
// finishes attaching to the pseudoconsole, so the handoff pipe is already
// closed and Windows Terminal reports
//
//	[error 2147942632 (0x800700E8) when launching `"...\dolt.exe" init']
//
// where 0x800700E8 is HRESULT_FROM_WIN32(ERROR_NO_DATA), "The pipe is being
// closed." Windows Terminal may then persist that session and replay it from
// C:\Windows\System32 on later launches (gastownhall/beads#6076).
//
// Hide is for ordinary commands and is a no-op on every non-Windows platform.
// BackgroundSysProcAttr is for the three long-lived database children; see its
// comment.
package execwin

import "os/exec"

// Windows process creation flags (winbase.h). They are duplicated here so the
// platform-neutral policy below compiles, and is tested, on every platform;
// execwin_windows_test.go pins them to golang.org/x/sys/windows.
const (
	createNoWindow uint32 = 0x08000000
)

// Hide keeps cmd from allocating a visible console window on Windows and
// returns cmd, so it wraps an exec.Command call inline:
//
//	out, err := execwin.Hide(exec.Command("git", "config", "user.name")).Output()
//
// A nil cmd is returned unchanged.
func Hide(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return nil
	}
	hide(cmd)
	return cmd
}

// hiddenCreationFlags is Hide's decision on Windows, kept free of Windows
// types so every platform tests it. flags are the creation flags cmd already
// carries (zero for a nil SysProcAttr) and attached reports whether bd itself
// has a console. It returns the flags to start cmd with, and whether they
// differ from flags at all.
func hiddenCreationFlags(flags uint32, _ bool) (uint32, bool) {
	return flags | createNoWindow, true
}
