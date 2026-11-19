package dolt

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// fixedReplayClock is the CreatedAt every forced-collision writer shares.
// Hash IDs derive from title, description, actor and CreatedAt
// (idgen.GenerateHashID), so two creates equal on those four inputs mint the
// same first candidate and stay free to differ everywhere else (Notes) — the
// colliding-candidate shape of GH#7289 without a barrier race.
var fixedReplayClock = time.Date(2026, 11, 19, 9, 30, 0, 0, time.UTC)

// replayCollidingIssue returns a create input whose generated-ID candidate
// collides with every other replayCollidingIssue of the same title (and, for
// wisps, the same plane).
func replayCollidingIssue(title, notes string, ephemeral bool) *types.Issue {
	issue := &types.Issue{
		Title:       title,
		Description: "same hash inputs",
		Notes:       notes,
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   fixedReplayClock,
	}
	if ephemeral {
		issue.Ephemeral = true
	}
	return issue
}

// forceCreateReplay deterministically reproduces the GH#7289 interleaving: it
// runs loser (a store write) under a pre-commit hook that, on the loser's
// FIRST transaction attempt only — body complete, transaction still open —
// synchronously runs createWinner on a hook-free context. The winner commits
// the row the loser's open transaction also wrote, so the loser's own commit
// fails with a serialization conflict and withRetryTx replays the closure: no
// synthetic error is injected; the real committed winner drives the retry.
// Later attempts invoke the hook as an attempt counter only.
//
// It returns the number of hook invocations (one per loser transaction
// attempt) and the loser's error. The winner closure runs on the test
// goroutine and may assert whatever it needs about the loser's open attempt
// (the minted candidate is already on the loser's struct).
func forceCreateReplay(t *testing.T, store *DoltStore, ctx context.Context,
	loser func(ctx context.Context) error,
	createWinner func(ctx context.Context) error,
) (attempts int, err error) {
	t.Helper()
	hookCtx := withPreCommitHook(ctx, func(tx *sql.Tx) error {
		attempts++
		if attempts == 1 {
			if werr := createWinner(ctx); werr != nil {
				t.Errorf("winner create failed: %v", werr)
			}
		}
		return nil
	})
	return attempts, loser(hookCtx)
}

// forceBatchReplay is forceCreateReplay for a losing batch: the winner is a
// single create of the same title, description, actor, CreatedAt and plane
// as colliding, so it takes colliding's open-transaction candidate. It
// returns the attempt count and the winner issue.
func forceBatchReplay(t *testing.T, store *DoltStore, ctx context.Context,
	colliding *types.Issue, call func(ctx context.Context) error) (attempts int, winner *types.Issue, err error) {
	t.Helper()
	var w *types.Issue
	attempts, err = forceCreateReplay(t, store, ctx, call, func(wctx context.Context) error {
		w = replayCollidingIssue(colliding.Title, "winner-notes", colliding.Ephemeral)
		// Route the winner to the colliding issue's plane (a no-history
		// loser collides with a no-history winner, not an ephemeral one).
		w.NoHistory = colliding.NoHistory
		if werr := store.CreateIssue(wctx, w, "same-actor"); werr != nil {
			return werr
		}
		if w.ID != colliding.ID {
			t.Fatalf("winner minted %q, want the loser's open-transaction candidate %q: the forced interleaving did not collide candidates", w.ID, colliding.ID)
		}
		return nil
	})
	return attempts, w, err
}

// replayQueryCount runs a COUNT-style query and returns its integer result.
func replayQueryCount(t *testing.T, store *DoltStore, ctx context.Context, query string, args ...any) int {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// replayQueryText runs a single-column text query and returns its string
// result.
func replayQueryText(t *testing.T, store *DoltStore, ctx context.Context, query string, args ...any) string {
	t.Helper()
	var s string
	if err := store.db.QueryRowContext(ctx, query, args...).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return s
}

// assertOneCreatedEvent fails unless exactly one created event exists for id
// in the given events table.
func assertOneCreatedEvent(t *testing.T, store *DoltStore, ctx context.Context, eventTable, id string) {
	t.Helper()
	//nolint:gosec // G202: eventTable is one of the two hardcoded event tables.
	if n := replayQueryCount(t, store, ctx,
		"SELECT COUNT(*) FROM "+eventTable+" WHERE issue_id = ? AND event_type = 'created'", id); n != 1 {
		t.Fatalf("%s: created events for %s = %d, want exactly 1", eventTable, id, n)
	}
}
