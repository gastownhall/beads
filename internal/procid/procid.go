// Package procid provides process-birth identity for safe reattachment and
// signaling of managed child processes. Its Token and Verify shape
// intentionally matches the API requested in gastownhall/beads#4513 for
// internal/doltserver, so that package can adopt it later. On macOS, where
// this package has no pidfd equivalent, signaling uses verify, signal, verify
// and retains a residual PID-reuse race.
package procid

// Token is an opaque, versioned process-birth identity.
type Token string
