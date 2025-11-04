package main

import (
	"testing"
	"time"
)

// waitFor repeatedly evaluates pred until it returns true or timeout expires.
// Use this instead of time.Sleep for event-driven testing.
func waitFor(t *testing.T, timeout, poll time.Duration, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(poll)
	}
	t.Fatalf("condition not met within %v", timeout)
}
