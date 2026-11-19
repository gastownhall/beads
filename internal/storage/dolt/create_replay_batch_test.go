package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateBatchReplayRemintsGeneratedIdentifiers pins GH#7289 on the
// regular (non-wisp) batch create path: the retry closure in
// createIssuesWithFullOptions receives the caller's issue structs with the
// IDs a rolled-back attempt minted still on them, so the replay upserts over
// the concurrent winner that took the candidate, attaches the batch's
// defaulted dependency source and derived comment ID to the winner's row,
// and reports success. Each subtest forces the interleaving deterministically
// (see forceBatchReplay).
func TestCreateBatchReplayRemintsGeneratedIdentifiers(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	// A durable dependency target, committed before any forced interleaving.
	target := replayCollidingIssue("replay remint batch target", "target-notes", false)
	if err := store.CreateIssue(ctx, target, "same-actor"); err != nil {
		t.Fatalf("create dep target: %v", err)
	}

	// assertReplayedBatch checks the post-replay invariants every losing
	// batch shares: both writers' rows survive with their own content and
	// exactly one created event each.
	assertReplayedBatch := func(t *testing.T, loser *types.Issue, winner *types.Issue, attempts int, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("replayed batch failed: %v", err)
		}
		if attempts < 2 {
			t.Fatalf("loser committed without a replay (%d attempts)", attempts)
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
	}

	t.Run("CreateIssues keeps the edge and comment with the fresh issue", func(t *testing.T) {
		const title = "replay remint batch createissues"
		loser := replayCollidingIssue(title, "loser-notes", false)
		loser.Dependencies = []*types.Dependency{
			{DependsOnID: target.ID, Type: types.DepBlocks},
		}
		loser.Comments = []*types.Comment{
			{Author: "same-actor", Text: "batch replay comment", CreatedAt: fixedReplayClock},
		}
		attempts, winner, err := forceBatchReplay(t, store, ctx, loser, func(lctx context.Context) error {
			return store.CreateIssues(lctx, []*types.Issue{loser}, "same-actor")
		})
		assertReplayedBatch(t, loser, winner, attempts, err)
		// The defaulted dependency source and the derived comment ID were
		// reminted with the issue: both records belong to the fresh ID.
		if n := replayQueryCount(t, store, ctx,
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
			loser.ID, target.ID); n != 1 {
			t.Fatalf("dependency edges %s -> %s = %d, want 1", loser.ID, target.ID, n)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", loser.ID); n != 1 {
			t.Fatalf("loser comments = %d, want 1", n)
		}
		// The winner keeps only its own records.
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", winner.ID); n != 0 {
			t.Fatalf("winner dependency edges = %d, want 0 (the replay attached the loser's edge)", n)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", winner.ID); n != 0 {
			t.Fatalf("winner comments = %d, want 0 (the replay attached the loser's comment)", n)
		}
	})

	t.Run("CreateIssuesWithFullOptions keeps the edge and comment with the fresh issue", func(t *testing.T) {
		const title = "replay remint batch fulloptions"
		loser := replayCollidingIssue(title, "loser-notes", false)
		loser.Dependencies = []*types.Dependency{
			{DependsOnID: target.ID, Type: types.DepBlocks},
		}
		loser.Comments = []*types.Comment{
			{Author: "same-actor", Text: "batch replay comment", CreatedAt: fixedReplayClock},
		}
		attempts, winner, err := forceBatchReplay(t, store, ctx, loser, func(lctx context.Context) error {
			return store.CreateIssuesWithFullOptions(lctx, []*types.Issue{loser}, "same-actor",
				storage.BatchCreateOptions{SkipPrefixValidation: true})
		})
		assertReplayedBatch(t, loser, winner, attempts, err)
		if n := replayQueryCount(t, store, ctx,
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
			loser.ID, target.ID); n != 1 {
			t.Fatalf("dependency edges %s -> %s = %d, want 1", loser.ID, target.ID, n)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", loser.ID); n != 1 {
			t.Fatalf("loser comments = %d, want 1", n)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", winner.ID); n != 0 {
			t.Fatalf("winner dependency edges = %d, want 0", n)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", winner.ID); n != 0 {
			t.Fatalf("winner comments = %d, want 0", n)
		}
	})

	t.Run("CreateOnly remints instead of refusing the winner's id", func(t *testing.T) {
		const title = "replay remint batch createonly"
		loser := replayCollidingIssue(title, "loser-notes", false)
		attempts, winner, err := forceBatchReplay(t, store, ctx, loser, func(lctx context.Context) error {
			return store.CreateIssuesWithFullOptions(lctx, []*types.Issue{loser}, "same-actor",
				storage.BatchCreateOptions{CreateOnly: true, SkipPrefixValidation: true})
		})
		// Without the reset the replay treats the rolled-back ID as an
		// explicit one and the create-only insert refuses the winner's row.
		assertReplayedBatch(t, loser, winner, attempts, err)
	})

	t.Run("ConflictSkip remints instead of dropping the issue", func(t *testing.T) {
		const title = "replay remint batch conflictskip"
		loser := replayCollidingIssue(title, "loser-notes", false)
		attempts, winner, err := forceBatchReplay(t, store, ctx, loser, func(lctx context.Context) error {
			return store.CreateIssuesWithFullOptions(lctx, []*types.Issue{loser}, "same-actor",
				storage.BatchCreateOptions{ConflictSkip: true, SkipPrefixValidation: true})
		})
		// Without the reset the replay sees the winner's row under the stale
		// ID and silently skips the whole issue.
		assertReplayedBatch(t, loser, winner, attempts, err)
	})

	t.Run("mixed explicit and generated ids", func(t *testing.T) {
		const title = "replay remint batch mixed"
		const explicitID = "test-mixed-batch-explicit"
		explicit := replayCollidingIssue(title+" explicit", "explicit-notes", false)
		explicit.ID = explicitID
		gen := replayCollidingIssue(title, "loser-notes", false)
		override := replayCollidingIssue(title+" override", "override-notes", false)
		override.PrefixOverride = "ovr"
		attempts, winner, err := forceBatchReplay(t, store, ctx, gen, func(lctx context.Context) error {
			return store.CreateIssues(lctx, []*types.Issue{explicit, gen, override}, "same-actor")
		})
		assertReplayedBatch(t, gen, winner, attempts, err)
		// Caller-supplied identifiers survive the replay untouched.
		if explicit.ID != explicitID {
			t.Fatalf("explicit issue ID changed: %q, want %q", explicit.ID, explicitID)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", explicitID); n != 1 {
			t.Fatalf("explicit rows = %d, want 1", n)
		}
		// The prefix override still routes the mint on the replay.
		if want := "ovr-"; len(override.ID) < len(want) || override.ID[:len(want)] != want {
			t.Fatalf("override issue ID = %q, want an %q prefix", override.ID, want)
		}
		if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", override.ID); n != 1 {
			t.Fatalf("override rows = %d, want 1", n)
		}
	})

	t.Run("cache-sized batch remints every generated id", func(t *testing.T) {
		// A batch of createBatchCacheMinIssues+ issues builds the batch
		// cache; the reset must hold on that path too.
		const title = "replay remint batch cache"
		collide := replayCollidingIssue(title, "loser-notes", false)
		first := replayCollidingIssue(title+" first", "first-notes", false)
		second := replayCollidingIssue(title+" second", "second-notes", false)
		attempts, winner, err := forceBatchReplay(t, store, ctx, collide, func(lctx context.Context) error {
			return store.CreateIssues(lctx, []*types.Issue{collide, first, second}, "same-actor")
		})
		assertReplayedBatch(t, collide, winner, attempts, err)
		for _, issue := range []*types.Issue{first, second} {
			if n := replayQueryCount(t, store, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", issue.ID); n != 1 {
				t.Fatalf("rows for %s = %d, want 1", issue.ID, n)
			}
			if issue.ID == winner.ID || issue.ID == collide.ID {
				t.Fatalf("batch issue %s collides with another row", issue.ID)
			}
		}
	})
}
