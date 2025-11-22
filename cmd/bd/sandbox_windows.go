//go:build windows

package main

// isSandboxed detects if we're running in a sandboxed environment.
//
// On Windows, sandboxing detection is more complex and platform-specific.
// For now, we conservatively return false to avoid false positives.
//
// Future improvements could check:
// - AppContainer isolation
// - Job object restrictions
// - Integrity levels
//
// Implements bd-u3t: Phase 2 auto-detection for GH #353
func isSandboxed() bool {
	// TODO(bd-u3t): Implement Windows sandbox detection if needed
	// For now, Windows users can manually use --sandbox flag
	return false
}
