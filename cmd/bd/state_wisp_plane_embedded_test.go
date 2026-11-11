//go:build cgo

package main

import (
	"os"
	"testing"
)

// stateDurableRows counts every durable row a set-state could write (issues,
// dependencies, labels) and the history commits, so a wisp-plane state change
// can be shown to leave all of them alone.
type stateDurableRows struct {
	issues, edges, labels, commits int
}

func readStateDurableRows(t *testing.T, beadsDir string) stateDurableRows {
	t.Helper()
	return stateDurableRows{
		issues:  stateAtomicityCount(t, beadsDir, "SELECT COUNT(*) FROM issues"),
		edges:   stateAtomicityCount(t, beadsDir, "SELECT COUNT(*) FROM dependencies"),
		labels:  stateAtomicityCount(t, beadsDir, "SELECT COUNT(*) FROM labels"),
		commits: stateAtomicityCount(t, beadsDir, "SELECT COUNT(*) FROM dolt_log"),
	}
}

// TestEmbeddedSetStateOnAWispPlaneSubjectStaysInTheWispPlane pins set-state on
// an ephemeral or NoHistory subject: the event takes the subject's mode, the
// edge and the label swap land in the wisp tables, and no durable issue, edge
// or label row, nor a history commit, is added.
func TestEmbeddedSetStateOnAWispPlaneSubjectStaysInTheWispPlane(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sw")

	for _, c := range []struct {
		name, flag           string
		ephemeral, noHistory int
	}{
		{"ephemeral", "--ephemeral", 1, 0},
		{"no_history", "--no-history", 0, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			subject := bdCreate(t, bd, dir, "Wisp-plane state subject "+c.name, "--type", "task", c.flag)
			bdSetState(t, bd, dir, subject.ID, "patrol=active")
			before := readStateDurableRows(t, beadsDir)

			res := stateSetStateJSON(t, bd, dir, subject.ID, "patrol=muted")
			eventID, _ := res["event_id"].(string)
			if eventID == "" {
				t.Fatalf("set-state --json returned no event_id: %v", res)
			}

			if got := stateAtomicityCount(t, beadsDir,
				"SELECT COUNT(*) FROM wisps WHERE id = ? AND issue_type = 'event' AND ephemeral = ? AND no_history = ?",
				eventID, c.ephemeral, c.noHistory); got != 1 {
				t.Errorf("event %s is not a wisp-plane event with ephemeral=%d no_history=%d", eventID, c.ephemeral, c.noHistory)
			}
			if got := stateAtomicityCount(t, beadsDir,
				"SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND type = 'parent-child' AND COALESCE(depends_on_wisp_id, depends_on_issue_id) = ?",
				eventID, subject.ID); got != 1 {
				t.Errorf("wisp_dependencies holds %d parent-child edges %s -> %s, want 1", got, eventID, subject.ID)
			}
			if got := stateAtomicityCount(t, beadsDir,
				"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ? AND label = 'patrol:muted'", subject.ID); got != 1 {
				t.Errorf("wisp_labels holds patrol:muted on %s %d times, want 1", subject.ID, got)
			}
			if got := stateAtomicityCount(t, beadsDir,
				"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ? AND label = 'patrol:active'", subject.ID); got != 0 {
				t.Errorf("wisp_labels still holds patrol:active on %s %d times, want 0", subject.ID, got)
			}
			if after := readStateDurableRows(t, beadsDir); after != before {
				t.Errorf("durable rows and history %+v -> %+v across a wisp-plane set-state, want unchanged", before, after)
			}
		})
	}
}
