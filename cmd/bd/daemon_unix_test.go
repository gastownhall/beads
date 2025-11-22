//go:build unix

package main

import (
	"os"
	"testing"
)

// TestIsProcessRunning_SelfCheck verifies that we can always detect our own process
func TestIsProcessRunning_SelfCheck(t *testing.T) {
	myPID := os.Getpid()
	if !isProcessRunning(myPID) {
		t.Errorf("isProcessRunning(%d) returned false for our own PID", myPID)
	}
}

// TestIsProcessRunning_Init verifies that PID 1 (init/systemd/launchd) is always running
func TestIsProcessRunning_Init(t *testing.T) {
	// PID 1 should always be running on Unix systems
	if !isProcessRunning(1) {
		t.Errorf("isProcessRunning(1) returned false, but init/systemd should always be running")
	}
}

// TestIsProcessRunning_NonexistentProcess verifies that we correctly detect dead processes
func TestIsProcessRunning_NonexistentProcess(t *testing.T) {
	// Pick a PID that's very unlikely to exist (max PID on most systems is < 100000)
	impossiblePID := 9999999
	if isProcessRunning(impossiblePID) {
		t.Errorf("isProcessRunning(%d) returned true for likely nonexistent PID", impossiblePID)
		t.Logf("If this fails, the test PID may actually exist on this system")
	}
}

// TestIsProcessRunning_ParentProcess verifies that we can detect our parent process
func TestIsProcessRunning_ParentProcess(t *testing.T) {
	parentPID := os.Getppid()
	if parentPID == 0 {
		t.Skip("Parent PID is 0 (orphaned process), skipping test")
	}
	if parentPID == 1 {
		t.Skip("Parent PID is 1 (adopted by init), skipping test")
	}

	// Our parent process should be running (it spawned us)
	if !isProcessRunning(parentPID) {
		t.Errorf("isProcessRunning(%d) returned false for our parent process", parentPID)
	}
}
