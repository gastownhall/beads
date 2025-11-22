package main

import (
	"runtime"
	"testing"
)

// TestSandboxDetection verifies sandbox detection doesn't false-positive in normal environments
func TestSandboxDetection(t *testing.T) {
	// In a normal test environment, we should NOT be sandboxed
	// This is a regression test to prevent false positives
	if isSandboxed() {
		t.Errorf("isSandboxed() returned true in normal test environment (false positive)")
		t.Logf("OS: %s, Arch: %s", runtime.GOOS, runtime.GOARCH)
		t.Logf("This could indicate:")
		t.Logf("  1. Test is running in an actual sandboxed environment")
		t.Logf("  2. Detection heuristic has a false positive")
		t.Logf("If running in CI/sandboxed environment, this is expected and test should be skipped")
	}
}

// TestSandboxDetectionExists verifies the function exists and is callable
func TestSandboxDetectionExists(t *testing.T) {
	// This test just ensures the function compiles and returns a bool
	result := isSandboxed()
	t.Logf("isSandboxed() returned: %v", result)

	// No assertion - just verify it doesn't panic
	// The actual value depends on the environment
}
