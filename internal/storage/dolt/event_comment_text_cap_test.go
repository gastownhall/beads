package dolt

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDoltStoreEventCommentOverTextCap pins GH#3925: an audit comment larger
// than MySQL TEXT's 65,535-byte cap (the reopen reason path, AddComment) must
// round-trip through events.comment intact. events.comment is still TEXT while
// old_value/new_value were widened to LONGTEXT.
func TestDoltStoreEventCommentOverTextCap(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "test-big-comment",
		Title:     "Issue with a large audit comment",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	big := strings.Repeat("x", 70000)
	if err := store.AddComment(ctx, issue.ID, "user1", big); err != nil {
		t.Fatalf("AddComment with %d-byte comment failed: %v", len(big), err)
	}

	events, err := store.GetEvents(ctx, issue.ID, 50)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	for _, ev := range events {
		if ev.EventType != types.EventCommented || ev.Comment == nil {
			continue
		}
		if got := len(*ev.Comment); got != len(big) {
			t.Fatalf("events.comment stored %d bytes, want %d (silently truncated)", got, len(big))
		}
		return
	}
	t.Fatalf("no commented event found among %d events", len(events))
}
