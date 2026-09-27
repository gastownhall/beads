package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateOnlyDuplicateKeyErrorDoesNotPoisonTx answers, against a real Dolt
// backend, whether a duplicate-key error from insertIssueCreateOnly (reached
// through InsertIssueIfNew's CreateOnly branch) poisons the enclosing
// transaction. A caller that gets storage.ErrAlreadyExists back from a
// CreateOnly insert inside its own transaction needs to know whether it can
// handle the error and keep writing on that transaction.
//
// This forces the SQL-level collision directly and deterministically: same
// explicit ID, same transaction, no goroutines, no hash-space odds. It is
// deliberately narrower than a race reproduction —
// TestCrossProject_ConcurrentWrites already exercises real concurrent writers
// against a real backend, but its writers each use their own transaction, so
// it never asks whether the real driver/engine leaves a transaction usable
// after an error mid-transaction. This test isolates exactly that one
// question.
//
// Answer, verified empirically below rather than assumed: no. MySQL-protocol
// semantics — unlike PostgreSQL's "current transaction is aborted" behavior —
// never mark an in-flight transaction as unusable after an ordinary statement
// error; only the failing statement's own effect is discarded. Dolt's
// go-mysql-server follows that same contract.
//
// Note on EnsureIssueIDAvailableInTx specifically: its own ErrAlreadyExists
// return (create_only_guard.go) never involves a failing SQL statement at
// all — REPLACE INTO local_metadata cannot itself error on a duplicate key
// (REPLACE is delete-then-insert by definition), and the collision is
// reported by a plain SELECT COUNT(*) read that the Go code then rejects.
// There is no SQL-level error there to poison anything. This test's
// collision is therefore forced at the stricter, higher-risk site — a genuine
// INSERT constraint violation from insertIssueCreateOnly — which also covers
// the weaker EnsureIssueIDAvailableInTx case by implication.
func TestCreateOnlyDuplicateKeyErrorDoesNotPoisonTx(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	bc, err := issueops.NewBatchContext(ctx, tx, storage.BatchCreateOptions{})
	if err != nil {
		t.Fatalf("NewBatchContext: %v", err)
	}

	newIssue := func(id, title string) *types.Issue {
		issue := &types.Issue{
			ID:        id,
			Title:     title,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := issueops.PrepareIssueForInsert(issue, bc.CustomStatuses, bc.CustomTypes); err != nil {
			t.Fatalf("PrepareIssueForInsert(%s): %v", id, err)
		}
		return issue
	}

	const dupID = "duptx-collide"

	first := newIssue(dupID, "first writer")
	isNew, _, err := issueops.InsertIssueIfNew(ctx, tx, "issues", first, storage.BatchCreateOptions{CreateOnly: true})
	if err != nil {
		t.Fatalf("first InsertIssueIfNew: %v", err)
	}
	if !isNew {
		t.Fatal("first InsertIssueIfNew: isNew = false, want true")
	}

	// Force the real SQL-level duplicate-key error from insertIssueCreateOnly
	// — same ID, same transaction, CreateOnly still set so the ON DUPLICATE
	// KEY UPDATE upsert path can't paper over it.
	second := newIssue(dupID, "second writer, same ID")
	_, _, err = issueops.InsertIssueIfNew(ctx, tx, "issues", second, storage.BatchCreateOptions{CreateOnly: true})
	if !errors.Is(err, storage.ErrAlreadyExists) {
		t.Fatalf("second InsertIssueIfNew: want storage.ErrAlreadyExists, got %v", err)
	}

	// The actual question: is the transaction still usable after that error?
	// Keep writing on the SAME tx, never a fresh one.
	third := newIssue("duptx-after-collision", "post-collision write")
	isNew, _, err = issueops.InsertIssueIfNew(ctx, tx, "issues", third, storage.BatchCreateOptions{CreateOnly: true})
	if err != nil {
		t.Fatalf("post-collision InsertIssueIfNew on the same tx: %v (transaction appears poisoned by the prior duplicate-key error)", err)
	}
	if !isNew {
		t.Fatal("post-collision InsertIssueIfNew: isNew = false, want true")
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit after recovering from a duplicate-key error on the same tx: %v", err)
	}
	committed = true

	// Durability check: read the post-collision row back, proving the commit
	// actually landed rather than silently no-op'ing.
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", third.ID).Scan(&count); err != nil {
		t.Fatalf("post-commit read-back: %v", err)
	}
	if count != 1 {
		t.Fatalf("post-commit read-back: count = %d, want 1", count)
	}
}
