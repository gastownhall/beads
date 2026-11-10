// Package execwin keeps child processes that a console-less bd starts on
// Windows from allocating a visible console window of their own.
//
// A Windows console-subsystem child started from a parent that has no
// console does not inherit one: the system allocates a brand new console
// for it, and with Windows Terminal registered as the default terminal
// application each such console surfaces as a real window on the desktop
// (GH#6076). bd hits this whenever it runs without a console — an MCP
// server, a hook, an agent harness with redirected stdio — because every
// dolt, git or PowerShell command it then ran was a console-subsystem
// child of a console-less parent.
//
// Hide wraps an ordinary, short-lived *exec.Cmd. When the parent has a
// console attached the command comes back untouched, so a foreground bd
// keeps its prompts, its output and its Ctrl+C; when the parent has none,
// and the caller made no explicit console choice, the child is created
// with CREATE_NO_WINDOW, so it gets a hidden console of its own instead of
// a visible one. It never overwrites attributes the caller already set.
// Off Windows, Hide is a strict no-op that returns the same command.
//
// BackgroundSysProcAttr is not conditional: the three long-lived database
// children (bd db-proxy-child, the managed sql-server and the shared
// sql-server) are pinned on Windows to CREATE_NO_WINDOW |
// CREATE_NEW_PROCESS_GROUP. A hidden console, not none — dolt runs uname
// probes on every start, and a console-less server makes each of those
// open its own visible window, which is exactly the regression
// DETACHED_PROCESS caused. The console is the child's own, not the
// parent's, so the child still survives its parent exiting, and nothing
// is ever displayed.
//
// scripts/console_spawn_policy_test.go guards the production sources
// whose spawns must be wrapped in Hide, and pins the three long-lived
// children to BackgroundSysProcAttr.
package execwin
