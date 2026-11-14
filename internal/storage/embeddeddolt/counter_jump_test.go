//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCounterMintJumpsPastHeldIDs (bd-t4h0n7, PR #6754): an explicit create
// need not advance the issue counter, so the counter can lag behind ids the
// id space already holds — in either plane. Every counter mint probes its
// candidate like the hash mint does, and the first held candidate jumps the
// counter to the highest numeric suffix held for the prefix — one scan — so
// the mint lands one past it instead of on it, and the jump persists, so the
// mint after that is the next sequential value. Legacy and guarded, single
// and batch creates all mint through the same path.
func TestCounterMintJumpsPastHeldIDs(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cl")
	ctx := t.Context()
	if err := te.store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("SetConfig(issue_id_mode=counter): %v", err)
	}
	if err := te.store.Commit(ctx, "enable counter mode"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	auto := func(title string) *types.Issue {
		return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	explicit := func(id, title string, ephemeral bool) *types.Issue {
		return &types.Issue{ID: id, Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
	}

	// cl-1 seeds the counter row at 1 while the id space holds nothing above
	// it, so only the explicit creates below lag the counter.
	first := auto("auto one")
	if err := te.store.CreateIssue(ctx, first, "tester"); err != nil {
		t.Fatalf("CreateIssue(first): %v", err)
	}
	if first.ID != "cl-1" {
		t.Fatalf("first.ID = %q, want cl-1", first.ID)
	}

	// Explicit cl-2 in the issues plane and explicit cl-100 in the wisps
	// plane: neither advances the counter.
	if err := te.store.CreateIssue(ctx, explicit("cl-2", "explicit two", false), "tester"); err != nil {
		t.Fatalf("CreateIssue(explicit cl-2): %v", err)
	}
	if err := te.store.CreateIssue(ctx, explicit("cl-100", "explicit wisp hundred", true), "tester"); err != nil {
		t.Fatalf("CreateIssue(explicit cl-100 wisp): %v", err)
	}

	// The legacy single API: candidate cl-2 is held, so the mint jumps past
	// the highest held suffix and returns cl-101.
	legacy := auto("auto over the lag")
	if err := te.store.CreateIssue(ctx, legacy, "tester"); err != nil {
		t.Fatalf("CreateIssue(legacy auto-mint over the lag): %v", err)
	}
	if legacy.ID != "cl-101" {
		t.Fatalf("legacy.ID = %q, want cl-101, one past the highest held suffix", legacy.ID)
	}

	// The occupants survived the mint untouched, in both planes.
	got, err := te.store.GetIssue(ctx, "cl-2")
	if err != nil {
		t.Fatalf("GetIssue(cl-2): %v", err)
	}
	if got.Title != "explicit two" {
		t.Errorf("issues cl-2 title = %q, want %q", got.Title, "explicit two")
	}
	var wispTitle string
	te.queryScalar(t, ctx, "SELECT title FROM wisps WHERE id = ?", []any{"cl-100"}, &wispTitle)
	if wispTitle != "explicit wisp hundred" {
		t.Errorf("wisps cl-100 title = %q, want %q", wispTitle, "explicit wisp hundred")
	}

	// The jump persisted: the next mint is the next sequential value.
	next := auto("auto sequential")
	if err := te.store.CreateIssue(ctx, next, "tester"); err != nil {
		t.Fatalf("CreateIssue(next): %v", err)
	}
	if next.ID != "cl-102" {
		t.Fatalf("next.ID = %q, want cl-102: the jump must persist in the counter", next.ID)
	}

	// Lag again behind an explicit cl-103; the guarded single — the
	// transaction create — jumps to cl-104.
	if err := te.store.CreateIssue(ctx, explicit("cl-103", "explicit three", false), "tester"); err != nil {
		t.Fatalf("CreateIssue(explicit cl-103): %v", err)
	}
	guarded := auto("auto guarded single")
	err = te.store.RunInTransaction(ctx, "test: guarded single over the lag", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, guarded, "tester")
	})
	if err != nil {
		t.Fatalf("transaction CreateIssue(guarded single over the lag): %v", err)
	}
	if guarded.ID != "cl-104" {
		t.Fatalf("guarded.ID = %q, want cl-104, one past the explicit cl-103", guarded.ID)
	}

	// Lag again behind an explicit cl-105; the guarded batch jumps to cl-106.
	if err := te.store.CreateIssue(ctx, explicit("cl-105", "explicit five", false), "tester"); err != nil {
		t.Fatalf("CreateIssue(explicit cl-105): %v", err)
	}
	guardedBatch := auto("auto guarded batch")
	if err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{guardedBatch}, "tester", storage.BatchCreateOptions{CreateOnly: true}); err != nil {
		t.Fatalf("CreateIssuesWithFullOptions(guarded batch over the lag): %v", err)
	}
	if guardedBatch.ID != "cl-106" {
		t.Fatalf("guardedBatch.ID = %q, want cl-106, one past the explicit cl-105", guardedBatch.ID)
	}

	// The counter holds the last jump.
	var lastID int
	te.queryScalar(t, ctx, "SELECT last_id FROM issue_counter WHERE prefix = ?", []any{"cl"}, &lastID)
	if lastID != 106 {
		t.Errorf("issue_counter last_id = %d, want 106", lastID)
	}
}
