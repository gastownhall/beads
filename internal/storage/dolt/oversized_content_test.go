package dolt

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// oversizedText returns n bytes of non-repeating ASCII ending in a marker, so
// a truncated or shifted round-trip cannot compare equal by accident. 70,000
// bytes is past TEXT's 65,535-byte cap (GH#3925).
func oversizedText(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%07d ", i)
	}
	s := b.String()[:n-len("<END>")]
	return s + "<END>"
}

// TestReopenIssueWithOversizedReasonKeepsTheCommentVerbatim pins GH#3925 on
// the reopen path: ReopenIssueInTx records the reason in events.new_value
// (LONGTEXT) and then again as a commented event in events.comment, which
// stayed TEXT until migration 0075. A reason over the cap failed that second
// write and rolled the reopen back.
func TestReopenIssueWithOversizedReasonKeepsTheCommentVerbatim(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, tc := range []struct {
		name   string
		id     string
		reason string
	}{
		{"70000 bytes", "reopen-big-reason", oversizedText(70000)},
		{"65536 bytes", "reopen-cap-plus-one", oversizedText(65536)},
		// 21,846 three-byte euro signs: 65,538 bytes in 21,846 characters, so
		// a character-counted cap would admit it and a byte cap would not.
		{"multibyte past cap", "reopen-euro-reason", strings.Repeat("€", 21846)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := &types.Issue{
				ID:        tc.id,
				Title:     "Issue reopened with a large reason",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("create issue: %v", err)
			}
			if err := store.CloseIssue(ctx, tc.id, "done", "tester", "s1"); err != nil {
				t.Fatalf("close issue: %v", err)
			}

			if err := store.ReopenIssue(ctx, tc.id, tc.reason, "tester"); err != nil {
				t.Fatalf("ReopenIssue with a %d-byte reason: %.300v", len(tc.reason), err)
			}

			got, err := store.GetIssue(ctx, tc.id)
			if err != nil {
				t.Fatalf("get reopened issue: %v", err)
			}
			if got.Status != types.StatusOpen {
				t.Fatalf("status after reopen = %q, want %q", got.Status, types.StatusOpen)
			}

			events, err := store.GetEvents(ctx, tc.id, 50)
			if err != nil {
				t.Fatalf("get events: %v", err)
			}
			for _, ev := range events {
				if ev.EventType != types.EventCommented || ev.Comment == nil {
					continue
				}
				if *ev.Comment != tc.reason {
					t.Fatalf("commented event holds %d bytes, want the %d-byte reason verbatim", len(*ev.Comment), len(tc.reason))
				}
				return
			}
			t.Fatalf("no commented event among %d events after reopen", len(events))
		})
	}
}

// TestPromoteFromEphemeralPreservesOversizedEventComment pins GH#3925 on the
// wisp plane: AddComment on an active wisp writes wisp_events.comment, TEXT
// until migration 0075, and PromoteFromEphemeral copies that row into
// events.comment with INSERT IGNORE, which truncates instead of failing when
// the destination is narrower than the source. Both planes must hold the
// comment byte for byte, in both copy directions.
func TestPromoteFromEphemeralPreservesOversizedEventComment(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	commentIn := func(t *testing.T, table, id string) string {
		t.Helper()
		var got string
		if err := store.db.QueryRowContext(ctx,
			"SELECT comment FROM "+table+" WHERE issue_id = ? AND event_type = ?",
			id, string(types.EventCommented)).Scan(&got); err != nil {
			t.Fatalf("read commented event from %s: %v", table, err)
		}
		return got
	}

	t.Run("promote", func(t *testing.T) {
		const id = "promote-big-comment"
		big := oversizedText(70000)
		createWisp(t, ctx, store, id)
		if err := store.AddComment(ctx, id, "tester", big); err != nil {
			t.Fatalf("AddComment on a wisp with a %d-byte comment: %.300v", len(big), err)
		}
		if got := commentIn(t, "wisp_events", id); got != big {
			t.Fatalf("wisp_events.comment holds %d bytes, want the %d-byte comment verbatim", len(got), len(big))
		}

		if err := store.PromoteFromEphemeral(ctx, id, "tester"); err != nil {
			t.Fatalf("PromoteFromEphemeral: %.300v", err)
		}
		assertNoRowsForIssue(t, store, "wisp_events", id)
		if got := commentIn(t, "events", id); got != big {
			t.Fatalf("promoted events.comment holds %d bytes, want the %d-byte comment verbatim", len(got), len(big))
		}
	})

	t.Run("demote", func(t *testing.T) {
		const id = "demote-big-comment"
		big := strings.Repeat("€", 21846)
		createPerm(t, ctx, store, id)
		if err := store.AddComment(ctx, id, "tester", big); err != nil {
			t.Fatalf("AddComment on a durable issue with a %d-byte comment: %.300v", len(big), err)
		}

		if err := store.UpdateIssue(ctx, id, map[string]interface{}{"no_history": true}, "tester"); err != nil {
			t.Fatalf("demote to no-history wisp: %.300v", err)
		}
		assertNoRowsForIssue(t, store, "events", id)
		if got := commentIn(t, "wisp_events", id); got != big {
			t.Fatalf("demoted wisp_events.comment holds %d bytes, want the %d-byte comment verbatim", len(got), len(big))
		}
	})
}
