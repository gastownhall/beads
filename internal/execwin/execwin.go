// Package execwin keeps the console-subsystem children bd starts from
// allocating a console window of their own on Windows.
//
// The problem is Windows-specific and invisible on Unix. A console-subsystem
// child (dolt.exe, git.exe, powershell.exe, netstat.exe) started by a parent
// that has NO console of its own does not silently inherit one: the system
// allocates a brand new console for it. bd has no console whenever its stdout
// is redirected by an agent harness, a daemon or a CI step, and the db-proxy
// child is started with DETACHED_PROCESS (see
// internal/storage/dbproxy/proxy/endpoint_windows.go), so it never has one.
// When Windows Terminal is registered as the default terminal application
// ("defterm"), each such console is handed to Windows Terminal and a real
// window appears and takes focus. For a short-lived command it is worse than
// cosmetic: the child can exit before Windows Terminal finishes attaching to
// the pseudoconsole, and the launch fails with
//
//	[error 2147942632 (0x800700E8) when launching `"...\dolt.exe" init']
//
// where 0x800700E8 is HRESULT_FROM_WIN32(ERROR_NO_DATA), "The pipe is being
// closed." (gastownhall/beads#6076).
//
// Hide ORs CREATE_NO_WINDOW into the command's creation flags on Windows: the
// child still gets a console object, so anything that queries console state
// keeps working, but it is never displayed. Off Windows Hide is a strict
// no-op.
//
// CREATE_NO_WINDOW is ignored when DETACHED_PROCESS or CREATE_NEW_CONSOLE is
// also set, so Hide must not be used on a detached command: a detached child
// already has no console, and Hide would not change that. Call Hide after the
// last change to cmd.SysProcAttr and before the command is started;
// scripts/console_spawn_policy_test.go enforces that order in the packages
// that spawn from a console-less context.
//
// The package and its first design are davidcforbes' (gastownhall/beads#6077).
package execwin
