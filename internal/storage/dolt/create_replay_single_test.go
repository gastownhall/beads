package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCreateIssueReplayRemintsGeneratedIdentifiers pins GH#7289 on the single
// create path: a retried DoltStore.CreateIssue must not mistake the ID its
// rolled-back attempt minted for a caller-supplied one. Without the reset at
// the top of the replayed closure, the replay skips generation and its
// collision probe, takes the concurrent winner's committed row for an
// upsert, drops the created event (isNew=false) and returns nil — both
// callers "succeed" on one surviving row (GH#7289). Each subtest forces the
// interleaving deterministically via the pre-commit hook (see
// forceCreateReplay): the winner commits while the loser's transaction is
// complete but open, and the loser's own commit conflict drives the retry.
func TestCreateIssueReplayRemintsGeneratedIdentifiers(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	t.Run("hash mode keeps both writers' rows", func(t *testing.T) {
		const title = "replay remint single hash"
		loser := replayCollidingIssue(title, "loser-notes", false)
		var winner *types.Issue
		attempts, err := forceCreateReplay(t, store, ctx,
			func(lctx context.Context) error { return store.CreateIssue(lctx, loser, "same-actor") },
			func(wctx context.Context) error {
				winner = replayCollidingIssue(title, "winner-notes", false)
				if werr := store.CreateIssue(wctx, winner, "same-actor"); werr != nil {
					return werr
				}
				if winner.ID != loser.ID {
					t.Fatalf("winner minted %q, want the loser's open-transaction candidate %q: the forced interleaving did not collide candidates", winner.ID, loser.ID)
				}
				return nil
			})
		if err != nil {
			t.Fatalf("replayed create failed: %v", err)
		}
		if attempts < 2 {
			t.Fatalf("loser committed without a replay (%d attempts): the winner did not force a commit conflict", attempts)
		}
		if loser.ID == winner.ID {
			t.Fatalf("loser reused the rolled-back ID %q the winner committed", loser.ID)
		}
		if got := replayQueryText(t, store, ctx, "SELECT notes FROM issues WHERE id = ?", loser.ID); got != "loser-notes" {
			t.Fatalf("loser row notes = %q, want loser-notes", got)
		}
		if got := replayQueryText(t, store, ctx, "SELECT notes FROM issues WHERE id = ?", winner.ID); got != "winner-notes" {
			t.Fatalf("winner row notes = %q, want winner-notes (the replay overwrote it)", got)
		}
		assertOneCreatedEvent(t, store, ctx, "events", loser.ID)
		assertOneCreatedEvent(t, store, ctx, "events", winner.ID)
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM issues WHERE title = ?", title); n != 2 {
			t.Fatalf("rows titled %q = %d, want 2", title, n)
		}
	})

	t.Run("generated comment follows its reminted issue", func(t *testing.T) {
		const title = "replay remint single comment"
		loser := replayCollidingIssue(title, "loser-notes", false)
		loser.Comments = []*types.Comment{
			{Author: "same-actor", Text: "shared comment body", CreatedAt: fixedReplayClock},
		}
		var winner *types.Issue
		attempts, err := forceCreateReplay(t, store, ctx,
			func(lctx context.Context) error { return store.CreateIssue(lctx, loser, "same-actor") },
			func(wctx context.Context) error {
				// The winner carries the same comment content, so an
				// issue-ID-only reset would leave the loser replay holding a
				// comment ID derived for the winner's issue.
				winner = replayCollidingIssue(title, "winner-notes", false)
				winner.Comments = []*types.Comment{
					{Author: "same-actor", Text: "shared comment body", CreatedAt: fixedReplayClock},
				}
				return store.CreateIssue(wctx, winner, "same-actor")
			})
		if err != nil {
			t.Fatalf("replayed create failed: %v", err)
		}
		if attempts < 2 {
			t.Fatalf("loser committed without a replay (%d attempts)", attempts)
		}
		if loser.ID == winner.ID {
			t.Fatalf("loser reused the rolled-back ID %q the winner committed", loser.ID)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", loser.ID); n != 1 {
			t.Fatalf("loser comments = %d, want 1", n)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", winner.ID); n != 1 {
			t.Fatalf("winner comments = %d, want 1", n)
		}
		loserComment := replayQueryText(t, store, ctx, "SELECT id FROM comments WHERE issue_id = ?", loser.ID)
		winnerComment := replayQueryText(t, store, ctx, "SELECT id FROM comments WHERE issue_id = ?", winner.ID)
		if loserComment == winnerComment {
			t.Fatalf("loser comment %q collides with the winner's: the comment ID was not reminted with its issue", loserComment)
		}
	})

	t.Run("serial equal-input control", func(t *testing.T) {
		const title = "replay remint serial control"
		first := replayCollidingIssue(title, "first-notes", false)
		second := replayCollidingIssue(title, "second-notes", false)
		if err := store.CreateIssue(ctx, first, "same-actor"); err != nil {
			t.Fatalf("first create: %v", err)
		}
		if err := store.CreateIssue(ctx, second, "same-actor"); err != nil {
			t.Fatalf("second create: %v", err)
		}
		if first.ID == second.ID {
			t.Fatalf("serial creates shared ID %q", first.ID)
		}
		assertOneCreatedEvent(t, store, ctx, "events", first.ID)
		assertOneCreatedEvent(t, store, ctx, "events", second.ID)
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM issues WHERE title = ?", title); n != 2 {
			t.Fatalf("rows titled %q = %d, want 2", title, n)
		}
	})

	t.Run("counter mode remints past the winner", func(t *testing.T) {
		// Counter mode runs last: it flips issue_id_mode for the store.
		if err := setConfigWithOwnBudget(t, store, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("set counter mode: %v", err)
		}
		// Seed the counter so both transactions deterministically draw
		// test-1: the loser's bump lives in its still-open transaction and
		// is invisible to the winner, exactly the overlapping-counter
		// snapshot of the bug.
		if _, err := store.db.ExecContext(ctx, "INSERT INTO issue_counter (prefix, last_id) VALUES ('test', 0)"); err != nil {
			t.Fatalf("seed counter: %v", err)
		}
		const title = "replay remint single counter"
		loser := replayCollidingIssue(title, "loser-notes", false)
		var winner *types.Issue
		attempts, err := forceCreateReplay(t, store, ctx,
			func(lctx context.Context) error { return store.CreateIssue(lctx, loser, "same-actor") },
			func(wctx context.Context) error {
				winner = replayCollidingIssue(title, "winner-notes", false)
				return store.CreateIssue(wctx, winner, "same-actor")
			})
		if err != nil {
			t.Fatalf("replayed create failed: %v", err)
		}
		if attempts < 2 {
			t.Fatalf("loser committed without a replay (%d attempts)", attempts)
		}
		if winner.ID != "test-1" || loser.ID != "test-2" {
			t.Fatalf("winner/loser ids = %q/%q, want test-1/test-2", winner.ID, loser.ID)
		}
		// The winner's counter bump survived the conflict: the next create
		// draws test-3, not a value either writer already holds.
		next := replayCollidingIssue(title+" next", "next-notes", false)
		if err := store.CreateIssue(ctx, next, "same-actor"); err != nil {
			t.Fatalf("create after the replay: %v", err)
		}
		if next.ID != "test-3" {
			t.Fatalf("next create id = %q, want test-3 (the winner advanced the counter)", next.ID)
		}
	})
}
