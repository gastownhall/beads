package dolt

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestReopenIssueWithOversizedReasonKeepsTheCommentVerbatim pins the reopen
// half of GH#3925: ReopenIssue records the reason into events.new_value
// (LONGTEXT since 0048/0057) and then again into events.comment, which is
// still TEXT. A reason over TEXT's 65,535-byte cap therefore fails the second
// write and rolls back the whole reopen, so a closed issue cannot be reopened
// with a large reason at all. Migration 0075 widens events.comment to
// LONGTEXT; after it, the reopen succeeds and the commented event carries the
// reason byte-for-byte.
func TestReopenIssueWithOversizedReasonKeepsTheCommentVerbatim(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "reopen-big-reason",
		Title:     "Issue reopened with a large reason",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("close issue: %v", err)
	}

	reason := strings.Repeat("r", 70000)
	if err := store.ReopenIssue(ctx, issue.ID, reason, "tester"); err != nil {
		t.Fatalf("ReopenIssue with %d-byte reason failed: %v", len(reason), err)
	}

	got, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("get issue after reopen: %v", err)
	}
	if got.Status != types.StatusOpen {
		t.Fatalf("issue status after reopen = %q, want open", got.Status)
	}

	events, err := store.GetEvents(ctx, issue.ID, 50)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	var commented int
	for _, ev := range events {
		if ev.EventType != types.EventCommented {
			continue
		}
		commented++
		if ev.Comment == nil {
			t.Fatalf("commented event %s has nil comment", ev.ID)
		}
		if *ev.Comment != reason {
			t.Fatalf("commented event comment: %d bytes, want the full %d-byte reason byte-for-byte", len(*ev.Comment), len(reason))
		}
	}
	if commented == 0 {
		t.Fatalf("no commented event found among %d events after reopen", len(events))
	}
}
