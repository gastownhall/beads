package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCreateReplayPreservesExplicitIdentifiers pins the caller-supplied half
// of the GH#7289 fix: the per-attempt reset clears only the fields the
// caller left empty, so explicit issue IDs, dependency source IDs and
// comment IDs survive a replay unchanged, and explicit-ID creates keep
// their existing upsert behavior — a replayed explicit create still updates
// the row the concurrent winner committed, exactly as it did before the fix.
func TestCreateReplayPreservesExplicitIdentifiers(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	// A durable dependency target, committed before any forced interleaving.
	target := replayCollidingIssue("replay explicit target", "target-notes", false)
	if err := store.CreateIssue(ctx, target, "same-actor"); err != nil {
		t.Fatalf("create dep target: %v", err)
	}

	explicitIssue := func(id, title, notes string) *types.Issue {
		issue := replayCollidingIssue(title, notes, false)
		issue.ID = id
		return issue
	}

	t.Run("explicit single create keeps its upsert under replay", func(t *testing.T) {
		const id = "test-explicit-upsert"
		loser := explicitIssue(id, "replay explicit single", "loser-notes")
		var winner *types.Issue
		attempts, err := forceCreateReplay(t, store, ctx,
			func(lctx context.Context) error { return store.CreateIssue(lctx, loser, "same-actor") },
			func(wctx context.Context) error {
				winner = explicitIssue(id, "replay explicit single", "winner-notes")
				return store.CreateIssue(wctx, winner, "same-actor")
			})
		if err != nil {
			t.Fatalf("replayed create failed: %v", err)
		}
		if attempts < 2 {
			t.Fatalf("loser committed without a replay (%d attempts)", attempts)
		}
		if loser.ID != id || winner.ID != id {
			t.Fatalf("explicit ids changed: loser=%q winner=%q, want %q", loser.ID, winner.ID, id)
		}
		// Existing upsert semantics: one row, and the replayed loser — the
		// last writer — owns its content.
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", id); n != 1 {
			t.Fatalf("rows for %s = %d, want 1", id, n)
		}
		if got := replayQueryText(t, store, ctx, "SELECT notes FROM issues WHERE id = ?", id); got != "loser-notes" {
			t.Fatalf("row notes = %q, want loser-notes (the explicit upsert must still win)", got)
		}
		// Exactly one created event: the winner's. The replayed upsert is an
		// update and never duplicates it.
		assertOneCreatedEvent(t, store, ctx, "events", id)
	})

	t.Run("explicit batch keeps its upsert under replay", func(t *testing.T) {
		const id = "test-explicit-batch-upsert"
		loser := explicitIssue(id, "replay explicit batch", "loser-notes")
		var winner *types.Issue
		attempts, err := forceCreateReplay(t, store, ctx,
			func(lctx context.Context) error {
				return store.CreateIssues(lctx, []*types.Issue{loser}, "same-actor")
			},
			func(wctx context.Context) error {
				winner = explicitIssue(id, "replay explicit batch", "winner-notes")
				return store.CreateIssue(wctx, winner, "same-actor")
			})
		if err != nil {
			t.Fatalf("replayed batch failed: %v", err)
		}
		if attempts < 2 {
			t.Fatalf("loser committed without a replay (%d attempts)", attempts)
		}
		if loser.ID != id {
			t.Fatalf("explicit id changed: %q, want %q", loser.ID, id)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", id); n != 1 {
			t.Fatalf("rows for %s = %d, want 1", id, n)
		}
		if got := replayQueryText(t, store, ctx, "SELECT notes FROM issues WHERE id = ?", id); got != "loser-notes" {
			t.Fatalf("row notes = %q, want loser-notes", got)
		}
		assertOneCreatedEvent(t, store, ctx, "events", id)
	})

	t.Run("mixed batch keeps caller ids and remints generated ones", func(t *testing.T) {
		const id = "test-mixed-keep"
		const commentID = "c-mixed-keep"
		const title = "replay explicit mixed"
		explicit := explicitIssue(id, title+" explicit", "explicit-notes")
		explicit.Dependencies = []*types.Dependency{
			{IssueID: id, DependsOnID: target.ID, Type: types.DepBlocks},
		}
		explicit.Comments = []*types.Comment{
			{ID: commentID, Author: "same-actor", Text: "caller comment", CreatedAt: fixedReplayClock},
		}
		gen := replayCollidingIssue(title, "loser-notes", false)
		attempts, winner, err := forceBatchReplay(t, store, ctx, gen, func(lctx context.Context) error {
			return store.CreateIssues(lctx, []*types.Issue{explicit, gen}, "same-actor")
		})
		if err != nil {
			t.Fatalf("replayed batch failed: %v", err)
		}
		if attempts < 2 {
			t.Fatalf("loser committed without a replay (%d attempts)", attempts)
		}
		if gen.ID == winner.ID {
			t.Fatalf("generated issue reused the rolled-back ID %q the winner committed", gen.ID)
		}
		// Every caller-supplied identifier is unchanged by the replay.
		if explicit.ID != id {
			t.Fatalf("explicit issue ID changed: %q, want %q", explicit.ID, id)
		}
		if got := explicit.Dependencies[0].IssueID; got != id {
			t.Fatalf("caller-supplied dependency source changed: %q, want %q", got, id)
		}
		if got := explicit.Comments[0].ID; got != commentID {
			t.Fatalf("caller-supplied comment ID changed: %q, want %q", got, commentID)
		}
		// The edge and the comment are stored under the caller's IDs.
		if n := replayQueryCount(t, store, ctx,
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
			id, target.ID); n != 1 {
			t.Fatalf("dependency edges %s -> %s = %d, want 1", id, target.ID, n)
		}
		if n := replayQueryCount(t, store, ctx,
			"SELECT COUNT(*) FROM comments WHERE id = ? AND issue_id = ?", commentID, id); n != 1 {
			t.Fatalf("comments %s on %s = %d, want 1", commentID, id, n)
		}
		// The reminted issue keeps its own row; the winner keeps its own.
		if got := replayQueryText(t, store, ctx, "SELECT notes FROM issues WHERE id = ?", gen.ID); got != "loser-notes" {
			t.Fatalf("generated row notes = %q, want loser-notes", got)
		}
		if got := replayQueryText(t, store, ctx, "SELECT notes FROM issues WHERE id = ?", winner.ID); got != "winner-notes" {
			t.Fatalf("winner row notes = %q, want winner-notes", got)
		}
	})
}
