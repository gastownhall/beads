package protocol

import (
	"strings"
	"testing"
)

// TestProtocol_UpdateRejectsGarbageStatus verifies that bd update --status
// with an invalid status value exits non-zero.
func TestProtocol_UpdateRejectsGarbageStatus(t *testing.T) {
	w := newWorkspace(t)
	id := w.create("Test issue")

	out, code := w.runExpectError("update", id, "--status", "bogus")
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(out, "invalid status") {
		t.Errorf("expected 'invalid status' in output, got: %s", out)
	}
}

// TestProtocol_UpdateAcceptsBuiltinStatuses verifies that built-in status
// values are accepted without error.
func TestProtocol_UpdateAcceptsBuiltinStatuses(t *testing.T) {
	w := newWorkspace(t)
	id := w.create("Test issue")

	w.run("update", id, "--status", "in_progress")
	w.run("update", id, "--status", "open")
}

// TestProtocol_UpdateAcceptsCustomStatus verifies that custom statuses
// configured via bd config are accepted by update.
func TestProtocol_UpdateAcceptsCustomStatus(t *testing.T) {
	w := newWorkspace(t)
	id := w.create("Test issue")

	w.run("config", "set", "status.custom", "awaiting_review,testing")
	w.run("update", id, "--status", "awaiting_review")
}
