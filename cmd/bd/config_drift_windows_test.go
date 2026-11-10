//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/steveyegge/beads/internal/doltserver"
)

// TestIsServerProbablyRunningReportsDeadPIDWithLingeringHandle guards the
// Windows liveness probe against the lingering-handle false positive.
//
// On Windows a terminated process's kernel object stays openable for as long
// as any handle to it remains open anywhere on the system. An existence-only
// probe (OpenProcess / os.FindProcess succeeds) therefore reports a dead
// server as alive. This test reproduces exactly that: it starts a child that
// exits immediately and deliberately never Wait()s it, so os/exec keeps a
// lingering handle open; it confirms the child has actually terminated, then
// asserts the drift probe reports it dead.
func TestIsServerProbablyRunningReportsDeadPIDWithLingeringHandle(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	// Reap the child at the end; until then cmd.Process keeps a handle open,
	// which is the lingering reference that defeats an existence-only probe.
	defer func() { _ = cmd.Wait() }()

	// Confirm the child has really exited, via a SEPARATE probe handle so we do
	// not disturb the lingering one held by cmd.Process.
	probe, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Skipf("cannot open probe handle for pid %d: %v", pid, err)
	}
	status, err := windows.WaitForSingleObject(probe, 5000)
	if err != nil {
		windows.CloseHandle(probe)
		t.Fatalf("waiting for child to exit: %v", err)
	}
	if status != windows.WAIT_OBJECT_0 {
		windows.CloseHandle(probe)
		t.Fatalf("wait for process %d returned status %#x, want WAIT_OBJECT_0", pid, status)
	}
	windows.CloseHandle(probe)

	beadsDir := t.TempDir()
	pidFile := filepath.Join(beadsDir, doltserver.PIDFileName)
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	if isServerProbablyRunning(beadsDir) {
		t.Errorf("dead pid %d reported as running (lingering-handle false positive)", pid)
	}
}
