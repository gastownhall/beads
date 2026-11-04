package protocol

import (
	"strings"
	"testing"
)

// TestProtocol_CloseBlockedExitsNonZero verifies that closing an issue blocked
// by open dependencies returns exit code 1.
func TestProtocol_CloseBlockedExitsNonZero(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	blocker := w.create("Blocker issue")
	blocked := w.create("Blocked issue")
	w.run("dep", "add", blocked, blocker, "--type=blocks")

	_, code := w.runExpectError("close", blocked)
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
}

// TestProtocol_CloseUnblockedExitsZero verifies that closing an unblocked
// issue returns exit code 0 (no regression).
func TestProtocol_CloseUnblockedExitsZero(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("Simple issue")
	w.run("close", id)
}

// TestProtocol_UpdateNonexistentExitsNonZero verifies that updating a
// nonexistent issue returns exit code 1.
func TestProtocol_UpdateNonexistentExitsNonZero(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	_, code := w.runExpectError("update", "nonexistent-xyz", "--status", "in_progress")
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
}

// TestProtocol_ClosePartialFailureExitsNonZero verifies that when closing
// multiple issues where some succeed and some fail (e.g., blocked), the
// command exits non-zero and still closes the closeable ones.
func TestProtocol_ClosePartialFailureExitsNonZero(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	closeable := w.create("Closeable issue")
	blocker := w.create("Blocker issue")
	blocked := w.create("Blocked issue")
	w.run("dep", "add", blocked, blocker, "--type=blocks")

	// Close both: closeable should succeed, blocked should fail, and the
	// refusal makes the batch exit 1.
	if _, code := w.runExpectError("close", closeable, blocked); code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}

	// Verify the closeable one was actually closed despite partial failure
	out := w.run("show", closeable, "--json")
	issues := parseJSONOutput(t, out)
	if len(issues) == 0 {
		t.Fatal("show returned no issues")
	}
	status, _ := issues[0]["status"].(string)
	if status != "closed" {
		t.Errorf("closeable issue should be closed despite partial failure, got status=%q", status)
	}

	// Verify the blocked one is still open
	out2 := w.run("show", blocked, "--json")
	issues2 := parseJSONOutput(t, out2)
	if len(issues2) > 0 {
		status2, _ := issues2[0]["status"].(string)
		if status2 == "closed" {
			t.Error("blocked issue should NOT be closed")
		}
	}
}

// TestProtocol_CloseNonexistentExitsNonZero verifies that closing a
// nonexistent issue returns a non-zero exit code.
func TestProtocol_CloseNonexistentExitsNonZero(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	out, _ := w.runExpectError("close", "nonexistent-xyz")
	if !strings.Contains(strings.ToLower(out), "not found") {
		t.Logf("output: %s", out)
	}
}
