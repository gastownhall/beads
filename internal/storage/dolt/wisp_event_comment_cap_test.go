package dolt

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPromoteFromEphemeralPreservesOversizedEventComment pins the wisp half
// of GH#3925: wisp_events.comment is still TEXT while its events counterpart
// is widened, so a comment over the 65,535-byte cap fails AddComment on an
// active wisp outright, and once the two planes hold different widths the
// promote/demote copies (INSERT IGNORE ... SELECT comment) become silent
// truncation points in whichever direction is narrow. Migration 0075 widens
// wisp_events.comment to LONGTEXT (ignored/0031 carries it to fresh clones);
// after it the comment must survive the promotion into events byte-for-byte,
// and the demotion copy back into wisp_events as well.
func TestPromoteFromEphemeralPreservesOversizedEventComment(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const wispID = "wisp-big-comment"
	createWisp(t, ctx, store, wispID)

	comment := strings.Repeat("w", 70000)
	if err := store.AddComment(ctx, wispID, "user1", comment); err != nil {
		t.Fatalf("AddComment with %d-byte comment on wisp failed: %v", len(comment), err)
	}

	if err := store.PromoteFromEphemeral(ctx, wispID, "tester"); err != nil {
		t.Fatalf("PromoteFromEphemeral: %v", err)
	}

	// The durable events row must carry the complete comment byte-for-byte,
	// not merely exist: assert the stored bytes, not a row count.
	promoted := commentedEventComment(t, store, ctx, wispID)
	if promoted != comment {
		t.Fatalf("promoted durable event comment: %d bytes, want the full %d-byte comment byte-for-byte", len(promoted), len(comment))
	}

	// Reverse copy direction: demote back to the wisp plane, which copies
	// events.comment into wisp_events.comment the same way.
	if err := store.UpdateIssue(ctx, wispID, map[string]interface{}{"wisp": true}, "tester"); err != nil {
		t.Fatalf("demote back to wisp: %v", err)
	}
	demoted := commentedEventComment(t, store, ctx, wispID)
	if demoted != comment {
		t.Fatalf("demoted wisp event comment: %d bytes, want the full %d-byte comment byte-for-byte", len(demoted), len(comment))
	}
}

// commentedEventComment returns the comment payload of the issue's single
// commented event, failing the test if there is not exactly one.
func commentedEventComment(t *testing.T, store *DoltStore, ctx context.Context, issueID string) string {
	t.Helper()
	events, err := store.GetEvents(ctx, issueID, 50)
	if err != nil {
		t.Fatalf("get events for %s: %v", issueID, err)
	}
	var payload string
	var seen int
	for _, ev := range events {
		if ev.EventType != types.EventCommented {
			continue
		}
		seen++
		if ev.Comment == nil {
			t.Fatalf("commented event %s has nil comment", ev.ID)
		}
		payload = *ev.Comment
	}
	if seen != 1 {
		t.Fatalf("commented events for %s: %d, want exactly 1", issueID, seen)
	}
	return payload
}
