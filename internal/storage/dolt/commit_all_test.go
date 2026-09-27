package dolt

import (
	"testing"
)

// TestCommitAllSweepsOutOfBandConfig is the regression test for the explicit
// commit commands' false success: a table modified out of band (the chronic
// config touch above all) never enters any transaction's dirty-table tracking,
// and plain Commit() excludes config (GH#2455) — so `bd vc commit` staged
// nothing, created no commit, and printed "Created commit <old HEAD>" while
// dolt_status stayed dirty. That also made the doctor's dirty-working-set
// warning unclearable by its own recommended remedy. CommitAll must sweep the
// whole working set, create a real commit, and report honestly that it did.
func TestCommitAllSweepsOutOfBandConfig(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	// Flush anything the harness left uncommitted so the assertions below are
	// about this test's own out-of-band write only.
	if _, err := store.CommitAll(ctx, "test: flush baseline"); err != nil {
		t.Fatalf("baseline CommitAll: %v", err)
	}

	headBefore, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}

	// Out-of-band write: config modified with no bd transaction tracking it.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('test.oob-commit-all', 'v1')"); err != nil {
		t.Fatalf("insert out-of-band config row: %v", err)
	}

	// Precondition: plain Commit leaves config dirty (GH#2455). If this stops
	// reproducing, the false-success scenario no longer exists and this test
	// should be rethought.
	if err := store.Commit(ctx, "commit excluding config"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !configDirty(t, ctx, db) {
		t.Fatalf("Commit() unexpectedly committed config; the false-success precondition no longer reproduces (did GH#2455's config exclusion change?)")
	}

	committed, err := store.CommitAll(ctx, "test: sweep out-of-band config")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if !committed {
		t.Fatalf("CommitAll reported nothing to commit while config was dirty")
	}
	if configDirty(t, ctx, db) {
		t.Fatalf("CommitAll left config dirty; the doctor's dirty-working-set warning would persist")
	}

	headAfter, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if headAfter == headBefore {
		t.Fatalf("CommitAll reported committed=true but HEAD did not advance from %s", headBefore)
	}
}

// TestCommitAllNothingToCommit pins the honest-no-op half of the fix: on a
// working set with nothing committable, CommitAll must return (false, nil)
// and leave HEAD alone, so `bd vc commit` prints "Nothing to commit" instead
// of fabricating "Created commit <existing HEAD>".
func TestCommitAllNothingToCommit(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := store.CommitAll(ctx, "test: flush baseline"); err != nil {
		t.Fatalf("baseline CommitAll: %v", err)
	}

	headBefore, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}

	committed, err := store.CommitAll(ctx, "test: nothing to commit")
	if err != nil {
		t.Fatalf("CommitAll on clean working set: %v", err)
	}
	if committed {
		t.Fatalf("CommitAll reported a commit on a clean working set")
	}

	headAfter, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("CommitAll reported committed=false but HEAD advanced: %s -> %s", headBefore, headAfter)
	}
}
