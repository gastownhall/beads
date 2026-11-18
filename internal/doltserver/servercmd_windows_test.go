//go:build windows

package doltserver

import (
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// TestNewDoltServerCmdHidesDetachedConsole pins the shared dolt sql-server
// spawn's creation flags (gastownhall/beads#6076): detached
// (CREATE_NEW_PROCESS_GROUP, the procAttrDetached half) AND hidden
// (CREATE_NO_WINDOW, the execwin.Hide half, applied after the SysProcAttr
// assignment so the flags combine rather than replace). A console-less bd —
// an agent harness, a daemon, a CI step — must not give the server a visible
// console window. internal/doltserver has no native Windows CI lane; run
// `go test -tags gms_pure_go -run TestNewDoltServerCmd ./internal/doltserver/`
// on a Windows host for this one, and see scripts/console_spawn_policy_test.go
// for the lexical guard that keeps the spawn site honest on every platform.
func TestNewDoltServerCmdHidesDetachedConsole(t *testing.T) {
	cmd := newDoltServerCmd("dolt-fixture-never-started", []string{"sql-server", "--port", "0"}, t.TempDir(), nil)
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr = nil: the sql-server must stay detached via procAttrDetached")
	}
	if got, want := cmd.SysProcAttr.CreationFlags, uint32(syscall.CREATE_NEW_PROCESS_GROUP|windows.CREATE_NO_WINDOW); got != want {
		t.Fatalf("CreationFlags = %#x, want %#x (CREATE_NEW_PROCESS_GROUP|CREATE_NO_WINDOW)", got, want)
	}
}
