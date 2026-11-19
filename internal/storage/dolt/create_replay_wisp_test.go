package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateWispBatchReplayRemintsGeneratedIdentifiers pins GH#7289 on the
// all-wisps fast path, which has its own retry closure in
// createIssuesWithFullOptions: a replayed ephemeral batch must remint the
// IDs its rolled-back attempt minted instead of upserting over the wisp the
// concurrent winner committed, and its dependency edge and comment must move
// with the fresh ID. The no-history routing subtest covers the other half
// of AllWisps.
func TestCreateWispBatchReplayRemintsGeneratedIdentifiers(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	t.Run("ephemeral batch", func(t *testing.T) {
		const title = "replay remint wisp batch"
		target := replayCollidingIssue(title+" target", "target-notes", true)
		if err := store.CreateIssue(ctx, target, "same-actor"); err != nil {
			t.Fatalf("create wisp dep target: %v", err)
		}
		collide := replayCollidingIssue(title, "loser-notes", true)
		collide.Dependencies = []*types.Dependency{
			{DependsOnID: target.ID, Type: types.DepBlocks},
		}
		collide.Comments = []*types.Comment{
			{Author: "same-actor", Text: "wisp replay comment", CreatedAt: fixedReplayClock},
		}
		sibling := replayCollidingIssue(title+" sibling", "sibling-notes", true)
		attempts, winner, err := forceBatchReplay(t, store, ctx, collide, func(lctx context.Context) error {
			return store.CreateIssuesWithFullOptions(lctx, []*types.Issue{collide, sibling}, "same-actor",
				storage.BatchCreateOptions{SkipPrefixValidation: true})
		})
		if err != nil {
			t.Fatalf("replayed wisp batch failed: %v", err)
		}
		if attempts < 2 {
			t.Fatalf("loser committed without a replay (%d attempts)", attempts)
		}
		if collide.ID == winner.ID {
			t.Fatalf("loser reused the rolled-back ID %q the winner committed", collide.ID)
		}
		// Both issues and their related records live in the routed wisp
		// tables under their own IDs.
		if got := replayQueryText(t, store, ctx, "SELECT notes FROM wisps WHERE id = ?", collide.ID); got != "loser-notes" {
			t.Fatalf("loser wisp notes = %q, want loser-notes", got)
		}
		if got := replayQueryText(t, store, ctx, "SELECT notes FROM wisps WHERE id = ?", winner.ID); got != "winner-notes" {
			t.Fatalf("winner wisp notes = %q, want winner-notes (the replay overwrote it)", got)
		}
		if got := replayQueryText(t, store, ctx, "SELECT notes FROM wisps WHERE id = ?", sibling.ID); got != "sibling-notes" {
			t.Fatalf("sibling wisp notes = %q, want sibling-notes", got)
		}
		assertOneCreatedEvent(t, store, ctx, "wisp_events", collide.ID)
		assertOneCreatedEvent(t, store, ctx, "wisp_events", winner.ID)
		if n := replayQueryCount(t, store, ctx,
			"SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND depends_on_wisp_id = ?",
			collide.ID, target.ID); n != 1 {
			t.Fatalf("wisp dependency edges %s -> %s = %d, want 1", collide.ID, target.ID, n)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM wisp_comments WHERE issue_id = ?", collide.ID); n != 1 {
			t.Fatalf("loser wisp comments = %d, want 1", n)
		}
		// The winner keeps only its own records.
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?", winner.ID); n != 0 {
			t.Fatalf("winner wisp dependency edges = %d, want 0", n)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM wisp_comments WHERE issue_id = ?", winner.ID); n != 0 {
			t.Fatalf("winner wisp comments = %d, want 0", n)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM wisps WHERE title = ?", title); n != 2 {
			t.Fatalf("wisps titled %q = %d, want 2", title, n)
		}
	})

	t.Run("no-history batch", func(t *testing.T) {
		const title = "replay remint nohistory batch"
		noHistory := func(notes string) *types.Issue {
			issue := replayCollidingIssue(title, notes, false)
			issue.NoHistory = true
			return issue
		}
		target := noHistory("target-notes")
		target.Title = title + " target"
		if err := store.CreateIssue(ctx, target, "same-actor"); err != nil {
			t.Fatalf("create no-history dep target: %v", err)
		}
		collide := noHistory("loser-notes")
		collide.Dependencies = []*types.Dependency{
			{DependsOnID: target.ID, Type: types.DepBlocks},
		}
		attempts, winner, err := forceBatchReplay(t, store, ctx, collide, func(lctx context.Context) error {
			return store.CreateIssuesWithFullOptions(lctx, []*types.Issue{collide}, "same-actor",
				storage.BatchCreateOptions{SkipPrefixValidation: true})
		})
		if err != nil {
			t.Fatalf("replayed no-history batch failed: %v", err)
		}
		if attempts < 2 {
			t.Fatalf("loser committed without a replay (%d attempts)", attempts)
		}
		if collide.ID == winner.ID {
			t.Fatalf("loser reused the rolled-back ID %q the winner committed", collide.ID)
		}
		if got := replayQueryText(t, store, ctx, "SELECT notes FROM wisps WHERE id = ?", collide.ID); got != "loser-notes" {
			t.Fatalf("loser wisp notes = %q, want loser-notes", got)
		}
		if got := replayQueryText(t, store, ctx, "SELECT notes FROM wisps WHERE id = ?", winner.ID); got != "winner-notes" {
			t.Fatalf("winner wisp notes = %q, want winner-notes", got)
		}
		assertOneCreatedEvent(t, store, ctx, "wisp_events", collide.ID)
		assertOneCreatedEvent(t, store, ctx, "wisp_events", winner.ID)
		if n := replayQueryCount(t, store, ctx,
			"SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND depends_on_wisp_id = ?",
			collide.ID, target.ID); n != 1 {
			t.Fatalf("wisp dependency edges %s -> %s = %d, want 1", collide.ID, target.ID, n)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?", winner.ID); n != 0 {
			t.Fatalf("winner wisp dependency edges = %d, want 0", n)
		}
	})
}
