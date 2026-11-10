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
	detachedProcess          uint32 = 0x00000008
	createNewConsole         uint32 = 0x00000010
	createNewProcessGroup    uint32 = 0x00000200
	createUnicodeEnvironment uint32 = 0x00000400
	createNoWindow           uint32 = 0x08000000

	// consoleChoiceFlags are the flags by which a caller has already decided
	// what console the child gets.
	consoleChoiceFlags = createNoWindow | detachedProcess | createNewConsole

	// backgroundCreationFlags are BackgroundSysProcAttr's flags on Windows.
	backgroundCreationFlags = createNoWindow | createNewProcessGroup
)

// Hide keeps cmd from allocating a visible console window on Windows and
// returns cmd, so it wraps an exec.Command call inline:
//
//	out, err := execwin.Hide(exec.Command("git", "config", "user.name")).Output()
//
// Only a bd with no console of its own needs this, so Hide probes that per
// call: when bd has a console the command is left exactly as it was and
// inherits it (foreground prompts and Ctrl+C behave as before), and only a
// console-less bd gives the command CREATE_NO_WINDOW, a console of its own
// that is never displayed. Off Windows Hide changes nothing.
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
//
// An attached bd changes nothing: its console children inherit that console
// as before, so prompts, terminal output and Ctrl+C keep reaching them. Nor
// does a command whose flags already decide its console (hidden, none, or a
// new visible one): CREATE_NO_WINDOW is ignored alongside DETACHED_PROCESS or
// CREATE_NEW_CONSOLE, so adding it would only obscure that choice. Every other
// flag the caller set is kept.
func hiddenCreationFlags(flags uint32, attached bool) (uint32, bool) {
	if attached || flags&consoleChoiceFlags != 0 {
		return flags, false
	}
	return flags | createNoWindow, true
}
