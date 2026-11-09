package dolt

import "testing"

// TestTryAutoResolveMergeConflicts_ChildCountersBothChangedResolvesToMax
// reproduces bd-28sb25 (GH#4796): two clones each mint children under the
// same parent offline, so the child_counters row changes on both sides
// (base 70, ours 73, theirs 72). The counter is a high-water mark, so the
// conflict is safe to settle at the max of both sides; today it is left for
// the operator and the pull aborts.
func TestTryAutoResolveMergeConflicts_ChildCountersBothChangedResolvesToMax(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	const parent = "bd-ccmax"
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

	setCounter := func(n int, msg string) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			"UPDATE child_counters SET last_child = ? WHERE parent_id = ?", n, parent); err != nil {
			t.Fatalf("update counter to %d: %v", n, err)
		}
		if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", msg); err != nil {
			t.Fatalf("commit %q: %v", msg, err)
		}
	}

	setCounter(73, "ours: three children")

	peerBranch := currentBranch + "_ccpeer"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	setCounter(72, "theirs: two children")
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
		"SELECT COUNT(*) FROM dolt_conflicts WHERE `table` = 'child_counters'").Scan(&conflicts); err != nil {
		t.Fatalf("read dolt_conflicts: %v", err)
	}
	if conflicts != 1 {
		t.Fatalf("fixture: want one child_counters conflict, got %d", conflicts)
	}

	resolved, err := store.tryAutoResolveMergeConflicts(ctx, tx)
	if err != nil {
		t.Fatalf("resolver error: %v", err)
	}
	if !resolved {
		t.Fatal("both-changed child_counters conflict (base 70, ours 73, theirs 72) was left for the operator; " +
			"want it auto-resolved to the high-water mark")
	}
	var last int
	if err := tx.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?", parent).Scan(&last); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if last != 73 {
		t.Errorf("last_child after resolve = %d, want max(73, 72) = 73", last)
	}
}

// TestSettleMergeCounterOnlyPullCompletes walks the same both-changed counter
// conflict through the whole settle path (SettleMerge, the embedded pull's
// choke point) rather than only the resolver: a counter-only divergence must
// conclude the merge — committed, no conflicts left, and the working set
// clean — where today the pull aborts for the operator.
func TestSettleMergeCounterOnlyPullCompletes(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	const parent = "bd-ccpull"
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

	setCounter := func(branch string, n int, msg string) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			"UPDATE child_counters SET last_child = ? WHERE parent_id = ?", n, parent); err != nil {
			t.Fatalf("update counter to %d: %v", n, err)
		}
		if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", msg); err != nil {
			t.Fatalf("commit %q: %v", msg, err)
		}
	}

	peerBranch := currentBranch + "_ccpeer2"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})
	setCounter(currentBranch, 73, "ours: three children")
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	setCounter(peerBranch, 72, "theirs: two children")
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current: %v", err)
	}

	// settleForcedMerge drives settleMergeInTx — the settle choke point every
	// SQL pull route funnels through — over the conflicted merge.
	if err := settleForcedMerge(ctx, t, store, peerBranch); err != nil {
		t.Fatalf("settle counter-only merge: %v", err)
	}

	var last int
	if err := db.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters AS OF 'HEAD' WHERE parent_id = ?", parent).Scan(&last); err != nil {
		t.Fatalf("read committed counter: %v", err)
	}
	if last != 73 {
		t.Errorf("committed last_child = %d, want max(73, 72) = 73", last)
	}
	var dirty int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d table(s) left uncommitted after settlement", dirty)
	}
}
