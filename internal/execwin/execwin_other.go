//go:build !windows

// Package execwin keeps a console-less bd's child processes from opening
// console windows on Windows.
//
// bd runs children from contexts that have no console to hand down: an agent
// harness, daemon or CI step redirects bd's stdout, and the db-proxy child is
// DETACHED_PROCESS. Windows then allocates a fresh, visible console for every
// console-subsystem child, and with Windows Terminal registered as the
// default terminal a short-lived child such as `dolt init` can fail with
// 0x800700E8 (HRESULT_FROM_WIN32(ERROR_NO_DATA)) before the terminal
// handoff completes (gastownhall/beads#6076).
//
// Hide is the one entry point, and off Windows it is a strict no-op.
package execwin

import "os/exec"

// Hide returns cmd unchanged, touching nothing: cmd.SysProcAttr stays as the
// caller left it (nil stays nil). It exists so a spawn site can be written
// once for every platform; see execwin_windows.go for the Windows behavior.
func Hide(cmd *exec.Cmd) *exec.Cmd {
	return cmd
}
