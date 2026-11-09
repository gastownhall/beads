package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestSettleMergeDeclinesDeletedChildCountersRow (bd-28sb25 AC-9): a
// child_counters conflict row absent on one side is a delete racing a
// counter advance, not a both-present high-water divergence — settlement
// must decline it with a MergeConflictsError naming child_counters, apply
// no counter resolution, and roll the transaction back (the server CLI's
// existing recovery contract: nothing of the merge survives).
func TestSettleMergeDeclinesDeletedChildCountersRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	const parent = "bd-ccdel"
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, 'parent epic', '', '', '', '', 'open', 2, 'epic')", parent); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO child_counters (parent_id, last_child) VALUES (?, 70)", parent); err != nil {
		t.Fatalf("insert counter: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed ancestor counter')"); err != nil {
		t.Fatalf("commit ancestor: %v", err)
	}

	peerBranch := currentBranch + "_ccdelpeer"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})

	// Ours advances the counter; theirs deletes the row.
	if _, err := db.ExecContext(ctx,
		"UPDATE child_counters SET last_child = 73 WHERE parent_id = ?", parent); err != nil {
		t.Fatalf("advance counter: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'ours: counter 73')"); err != nil {
		t.Fatalf("commit ours: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM child_counters WHERE parent_id = ?", parent); err != nil {
		t.Fatalf("delete counter: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'theirs: delete counter')"); err != nil {
		t.Fatalf("commit theirs: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	for _, q := range []string{"SET @@dolt_allow_commit_conflicts = 1", "SET @@dolt_force_transaction_commit = 1"} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			_ = tx.Rollback()
			t.Fatalf("%s: %v", q, err)
		}
	}
	_, mergeErr := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch)

	settleErr := store.settleMergeInTx(ctx, tx, mergeErr)
	if settleErr == nil {
		t.Fatal("settlement accepted a child_counters conflict row absent on one side; want a decline")
	}
	var mce *versioncontrolops.MergeConflictsError
	if !errors.As(settleErr, &mce) {
		t.Fatalf("settlement error = %v, want *versioncontrolops.MergeConflictsError", settleErr)
	}
	named := false
	for _, c := range mce.Conflicts {
		if c.Field == "child_counters" {
			named = true
		}
	}
	if !named {
		t.Errorf("MergeConflictsError does not name child_counters: %v", settleErr)
	}

	// The transaction rolled back: our counter is untouched and the working
	// set carries nothing from the refused merge.
	var last int
	if err := db.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?", parent).Scan(&last); err != nil {
		t.Fatalf("read counter after decline: %v", err)
	}
	if last != 73 {
		t.Errorf("our counter after decline = %d, want 73 (no resolution applied)", last)
	}
	var merging int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_merge_status WHERE is_merging").Scan(&merging); err != nil {
		t.Logf("read merge status: %v", err)
	}
	if merging != 0 {
		t.Error("a merge is still open after the declined settlement")
	}
}

// TestTryAutoResolveMergeConflicts_ChildCountersDeclinesNegative (bd-28sb25
// AC-9): a negative last_child is not a counter bd's write paths can produce;
// the INT schema cannot hold a non-numeric one, so the malformed-value case
// is exercised through a synthetic negative value in the conflict row. The
// resolver must decline the whole table — no partial resolution of the
// healthy rows riding with it.
func TestTryAutoResolveMergeConflicts_ChildCountersDeclinesNegative(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	seedCounter := func(parent string) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
				"VALUES (?, 'parent epic', '', '', '', '', 'open', 2, 'epic')", parent); err != nil {
			t.Fatalf("insert parent: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO child_counters (parent_id, last_child) VALUES (?, 70)", parent); err != nil {
			t.Fatalf("insert counter: %v", err)
		}
	}
	seedCounter("bd-ccneg")
	seedCounter("bd-ccok")
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed ancestor counters')"); err != nil {
		t.Fatalf("commit ancestor: %v", err)
	}

	peerBranch := currentBranch + "_ccnegpeer"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})

	// Ours advances both counters; theirs advances them differently — two
	// both-changed conflicts.
	for _, parent := range []string{"bd-ccneg", "bd-ccok"} {
		if _, err := db.ExecContext(ctx,
			"UPDATE child_counters SET last_child = 73 WHERE parent_id = ?", parent); err != nil {
			t.Fatalf("advance counter %s: %v", parent, err)
		}
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'ours: counters 73')"); err != nil {
		t.Fatalf("commit ours: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	for _, parent := range []string{"bd-ccneg", "bd-ccok"} {
		if _, err := db.ExecContext(ctx,
			"UPDATE child_counters SET last_child = 72 WHERE parent_id = ?", parent); err != nil {
			t.Fatalf("advance counter %s: %v", parent, err)
		}
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'theirs: counters 72')"); err != nil {
		t.Fatalf("commit theirs: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		t.Fatalf("allow commit conflicts: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		t.Logf("merge returned: %v", err)
	}

	var conflicts int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_conflicts_child_counters").Scan(&conflicts); err != nil {
		t.Fatalf("read child_counters conflict rows: %v", err)
	}
	if conflicts != 2 {
		t.Fatalf("fixture: want two child_counters conflict rows, got %d", conflicts)
	}

	// Corrupt one side's value to a negative high-water mark: the malformed
	// value the INT schema cannot persist normally (AC-9).
	if _, err := tx.ExecContext(ctx,
		"UPDATE dolt_conflicts_child_counters SET our_last_child = -5 WHERE our_parent_id = ?", "bd-ccneg"); err != nil {
		t.Fatalf("corrupt conflict value: %v", err)
	}

	resolved, err := store.tryAutoResolveMergeConflicts(ctx, tx)
	if err != nil {
		t.Fatalf("resolver error: %v", err)
	}
	if resolved {
		t.Fatal("resolver settled a child_counters conflict carrying a negative last_child; want a decline")
	}

	// The decline must be all-or-nothing: the healthy sibling conflict row is
	// still live and neither counter was touched.
	var live int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_conflicts_child_counters").Scan(&live); err != nil {
		t.Fatalf("count live conflicts: %v", err)
	}
	if live != 2 {
		t.Errorf("live child_counters conflicts after decline = %d, want 2 (no partial resolution)", live)
	}
	var last int
	if err := tx.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?", "bd-ccok").Scan(&last); err != nil {
		t.Fatalf("read healthy counter: %v", err)
	}
	if last != 73 {
		t.Errorf("healthy counter after decline = %d, want 73 (no resolution applied)", last)
	}
}
