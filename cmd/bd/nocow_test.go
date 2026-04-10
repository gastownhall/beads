package main

import (
	"runtime"
	"testing"
)

// TestApplyNoCOW_NonLinuxNoError verifies applyNoCOW is a safe no-op on
// non-Linux platforms. On Linux this is covered by nocow_linux_test.go.
func TestApplyNoCOW_NonLinuxNoError(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux-specific behavior tested in nocow_linux_test.go")
	}
	dir := t.TempDir()
	if err := applyNoCOW(dir); err != nil {
		t.Fatalf("applyNoCOW on non-Linux should return nil, got %v", err)
	}
	ok, err := hasNoCOW(dir)
	if err != nil {
		t.Fatalf("hasNoCOW on non-Linux should return nil error, got %v", err)
	}
	if ok {
		t.Errorf("hasNoCOW on non-Linux should report false, got true")
	}
	onBtrfs, err := isBtrfs(dir)
	if err != nil {
		t.Fatalf("isBtrfs on non-Linux should return nil error, got %v", err)
	}
	if onBtrfs {
		t.Errorf("isBtrfs on non-Linux should report false, got true")
	}
}
