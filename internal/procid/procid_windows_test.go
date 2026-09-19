//go:build windows

package procid

import (
	"os/exec"
	"testing"
	"time"
)

// A terminated process stays PID-resolvable for as long as anyone holds a
// handle to it (our own os.Process here models Task Manager, antivirus, or a
// debugger). Verify must still classify it as not matching: the invariant is
// "Verify == true implies running" on every platform.
func TestVerifyAfterTerminateWhileHandleHeld(t *testing.T) {
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	// cmd.Process keeps a handle open until Wait, pinning the PID; Wait only
	// in cleanup so the pin outlives every assertion below.
	defer func() { _ = cmd.Wait() }()
	pid := cmd.Process.Pid

	tok, err := Capture(pid)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("Capture(child): %v", err)
	}
	handle, err := Open(pid, tok)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("Open(child): %v", err)
	}
	defer func() { _ = handle.Close() }()

	if err := handle.Kill(); err != nil {
		t.Fatalf("Kill(child): %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		matched, verifyErr := Verify(pid, tok)
		if verifyErr != nil {
			t.Fatalf("Verify(terminated child): %v", verifyErr)
		}
		if !matched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Verify kept matching a terminated process whose handle is still held")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, captureErr := Capture(pid); captureErr == nil {
		t.Fatal("Capture(terminated child) succeeded, want process-gone error")
	} else if !IsProcessGone(captureErr) {
		t.Fatalf("IsProcessGone(Capture(terminated child)) = false for %v", captureErr)
	}

	// A repeat fatal signal against the already-terminated target is a
	// success, not a shutdown failure.
	if err := handle.Kill(); err != nil {
		t.Fatalf("Kill(terminated child) = %v, want nil", err)
	}
}
