//go:build windows

package linear

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// Keep the parent-owned process handle open after exit to reproduce the state
// where OpenProcess succeeds for a process that is no longer running.
func TestIsProcessAliveReportsExitedProcessWithLingeringHandleAsDead(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit", "0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child process: %v", err)
	}
	t.Cleanup(func() {
		if err := cmd.Wait(); err != nil {
			t.Errorf("reap child process: %v", err)
		}
	})

	pid := cmd.Process.Pid
	probe, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open probe handle for process %d: %v", pid, err)
	}
	status, waitErr := windows.WaitForSingleObject(probe, 5000)
	closeErr := windows.CloseHandle(probe)
	if waitErr != nil {
		t.Fatalf("wait for process %d to exit: %v", pid, waitErr)
	}
	if closeErr != nil {
		t.Fatalf("close probe handle for process %d: %v", pid, closeErr)
	}
	if status != windows.WAIT_OBJECT_0 {
		t.Fatalf("wait for process %d returned status %#x, want WAIT_OBJECT_0", pid, status)
	}

	if IsProcessAlive(pid) {
		t.Fatalf("exited process %d reported alive while its parent handle remained open", pid)
	}
}
