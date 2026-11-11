//go:build cgo

package main

import (
	"os"
	"testing"
)

// TestEmbeddedSetStateLandsAsOneHistoryCommit pins set-state's history on a
// settled, isolated durable database with auto-commit on: one Dolt commit
// titled `bd: set-state <id>` holds the event row, its parent-child edge and
// the label swap together. The command used to commit the event and the label
// patch separately, so history showed an event without the label change it
// recorded.
func TestEmbeddedSetStateLandsAsOneHistoryCommit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sh")
	subject := bdCreate(t, bd, dir, "State history subject", "--type", "task")
	bdSetState(t, bd, dir, subject.ID, "patrol=active")
	if got := stateAtomicityCount(t, beadsDir, "SELECT COUNT(*) FROM dolt_status"); got != 0 {
		t.Fatalf("working set has %d uncommitted tables before the state change, want a settled database", got)
	}
	message := "bd: set-state " + subject.ID
	commitsBefore := stateAtomicityCount(t, beadsDir, "SELECT COUNT(*) FROM dolt_log")
	titledBefore := stateAtomicityCount(t, beadsDir, "SELECT COUNT(*) FROM dolt_log WHERE message = ?", message)

	res := stateSetStateJSON(t, bd, dir, subject.ID, "patrol=muted")
	eventID, _ := res["event_id"].(string)
	if eventID == "" {
		t.Fatalf("set-state --json returned no event_id: %v", res)
	}

	if got := stateAtomicityCount(t, beadsDir, "SELECT COUNT(*) FROM dolt_log"); got != commitsBefore+1 {
		t.Errorf("set-state added %d history commits, want 1", got-commitsBefore)
	}
	if got := stateAtomicityCount(t, beadsDir, "SELECT COUNT(*) FROM dolt_log WHERE message = ?", message); got != titledBefore+1 {
		t.Fatalf("set-state added %d commits titled %q, want 1", got-titledBefore, message)
	}
	if got := stateAtomicityCount(t, beadsDir,
		"SELECT COUNT(*) FROM dolt_log WHERE message = ? AND commit_hash = HASHOF('HEAD')", message); got != 1 {
		t.Fatalf("the %q commit is not HEAD", message)
	}

	// The one commit's own diff carries all three writes.
	for _, c := range []struct {
		what, query string
		args        []any
	}{
		{"the event row",
			"SELECT COUNT(*) FROM DOLT_DIFF('HEAD~1', 'HEAD', 'issues') WHERE diff_type = 'added' AND to_id = ? AND to_issue_type = 'event'",
			[]any{eventID}},
		{"the parent-child edge",
			"SELECT COUNT(*) FROM DOLT_DIFF('HEAD~1', 'HEAD', 'dependencies') WHERE diff_type = 'added' AND to_issue_id = ? AND to_depends_on_issue_id = ? AND to_type = 'parent-child'",
			[]any{eventID, subject.ID}},
		{"the new label",
			"SELECT COUNT(*) FROM DOLT_DIFF('HEAD~1', 'HEAD', 'labels') WHERE diff_type = 'added' AND to_issue_id = ? AND to_label = 'patrol:muted'",
			[]any{subject.ID}},
		{"the old label's removal",
			"SELECT COUNT(*) FROM DOLT_DIFF('HEAD~1', 'HEAD', 'labels') WHERE diff_type = 'removed' AND from_issue_id = ? AND from_label = 'patrol:active'",
			[]any{subject.ID}},
	} {
		if got := stateAtomicityCount(t, beadsDir, c.query, c.args...); got != 1 {
			t.Errorf("the %q commit carries %s %d times, want 1", message, c.what, got)
		}
	}
	if got := stateAtomicityCount(t, beadsDir, "SELECT COUNT(*) FROM dolt_status"); got != 0 {
		t.Errorf("set-state left %d tables uncommitted, want none", got)
	}
}
