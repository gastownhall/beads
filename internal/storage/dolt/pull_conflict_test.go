package dolt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestPullAutoResolveMetadataConflicts verifies that merge conflicts limited to
// the metadata table are automatically resolved with "theirs" strategy (GH#2466).
// This simulates the scenario where two machines each write different
// dolt_auto_push_* values to the metadata table, causing recurring conflicts on pull.
func TestPullAutoResolveMetadataConflicts(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	// Record the current branch (our test branch).
	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}

	// Insert a metadata row on the current branch and commit.
	if _, err := db.ExecContext(ctx, "INSERT INTO metadata (`key`, value) VALUES ('dolt_auto_push_commit', 'aaa')"); err != nil {
		t.Fatalf("failed to insert metadata on current branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local metadata')"); err != nil {
		t.Fatalf("failed to commit on current branch: %v", err)
	}

	// Create a divergent branch to simulate the remote.
	remoteBranch := currentBranch + "_remote"
	// Branch from current branch's parent (HEAD~1).
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", remoteBranch); err != nil {
		t.Fatalf("failed to create remote branch: %v", err)
	}
	defer func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", remoteBranch)
	}()

	// Switch to remote branch and insert conflicting metadata.
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", remoteBranch); err != nil {
		t.Fatalf("failed to checkout remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO metadata (`key`, value) VALUES ('dolt_auto_push_commit', 'bbb')"); err != nil {
		t.Fatalf("failed to insert metadata on remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'remote metadata')"); err != nil {
		t.Fatalf("failed to commit on remote branch: %v", err)
	}

	// Switch back to current branch.
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("failed to checkout current branch: %v", err)
	}

	// Merge the remote branch in a transaction with dolt_allow_commit_conflicts.
	// This simulates what pullWithAutoResolve does internally.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("failed to set dolt_allow_commit_conflicts: %v", err)
	}

	_, mergeErr := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", remoteBranch)
	// mergeErr may or may not be nil depending on Dolt version.

	// Try auto-resolve.
	resolved, resolveErr := store.tryAutoResolveMergeConflicts(ctx, tx)
	if resolveErr != nil {
		_ = tx.Rollback()
		t.Fatalf("tryAutoResolveMergeConflicts error: %v (mergeErr: %v)", resolveErr, mergeErr)
	}
	if !resolved {
		_ = tx.Rollback()
		if mergeErr != nil {
			t.Fatalf("merge failed and metadata conflicts were not auto-resolved: %v", mergeErr)
		}
		// Clean merge, no conflicts to resolve — verify the value.
		t.Log("merge succeeded without conflicts (auto-merge)")
		return
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("failed to commit after auto-resolve: %v", err)
	}

	// Verify the metadata value is "theirs" (bbb from remote).
	var value string
	if err := db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = 'dolt_auto_push_commit'").Scan(&value); err != nil {
		t.Fatalf("failed to read resolved metadata: %v", err)
	}
	if value != "bbb" {
		t.Errorf("expected metadata value 'bbb' (theirs), got %q", value)
	}
}

// TestPullAutoResolveSkipsNonMetadataConflicts verifies that conflicts on
// tables other than metadata are NOT auto-resolved. The issues-table conflict
// here is add/add (both branches insert the same PK), which the LWW safety
// check rejects (GH#4698); modify/modify conflicts with different timestamps
// ARE auto-resolved by LWW, covered by issues_conflict_test.go.
func TestPullAutoResolveSkipsNonMetadataConflicts(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}

	// Create an issue on the current branch.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('conflict-test', 'Local Title', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("failed to insert issue on current branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local issue')"); err != nil {
		t.Fatalf("failed to commit on current branch: %v", err)
	}

	// Create divergent branch from parent.
	remoteBranch := currentBranch + "_remote2"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", remoteBranch); err != nil {
		t.Fatalf("failed to create remote branch: %v", err)
	}
	defer func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", remoteBranch)
	}()

	// Insert conflicting issue on remote branch (same PK, different title).
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", remoteBranch); err != nil {
		t.Fatalf("failed to checkout remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('conflict-test', 'Remote Title', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("failed to insert issue on remote branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'remote issue')"); err != nil {
		t.Fatalf("failed to commit on remote branch: %v", err)
	}

	// Switch back and merge.
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("failed to checkout current branch: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("failed to set dolt_allow_commit_conflicts: %v", err)
	}

	_, mergeErr := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", remoteBranch)

	// Issues table conflict should NOT be auto-resolved.
	resolved, resolveErr := store.tryAutoResolveMergeConflicts(ctx, tx)
	_ = tx.Rollback()

	if mergeErr == nil && resolveErr == nil && !resolved {
		// Clean merge — Dolt auto-merged the issue changes.
		t.Skip("merge succeeded without conflicts — cannot test non-metadata conflict path")
		return
	}

	if resolveErr != nil {
		// Error checking conflicts is acceptable for some Dolt versions.
		t.Logf("tryAutoResolveMergeConflicts returned error: %v", resolveErr)
		return
	}

	if resolved {
		t.Error("expected non-metadata conflicts NOT to be auto-resolved")
	}
}

// TestAutoResolveConflictsAfterCLIPull_DependencyAuditOnly verifies the post-CLI-pull
// resolver (used for git-protocol / credentialed remotes, #4259 finding 1) resolves an
// audit-only dependency conflict left by an out-of-transaction merge. CLI `dolt pull`
// performs the merge in a subprocess and leaves conflicts in the working set, so the
// resolver never runs in the transaction that produced the merge — unlike the SQL
// DOLT_PULL path. Here the conflict is persisted into the working set (what a CLI pull
// leaves behind) and then resolved via finishCLIPull's helper on the store connection,
// which stays on the pull's branch (a brand-new connection would default to the base
// branch and miss the conflict).
func TestAutoResolveConflictsAfterCLIPull_DependencyAuditOnly(t *testing.T) {
	store, peerBranch := setupDependencyMergeConflict(t, "blocks", "alice", "blocks", "bob")

	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	// Persist the merge conflict into the working set (mirroring a CLI pull), then
	// resolve it from a brand-new connection rather than this transaction.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("allow commit conflicts: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		t.Logf("merge returned: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("persist conflicted working set: %v", err)
	}

	// A fresh connection must observe the persisted conflict, else this test would
	// not be exercising the cross-connection (post-CLI-pull) resolver path.
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts_dependencies").Scan(&n); err != nil {
		t.Fatalf("count conflicts on fresh conn: %v", err)
	}
	if n == 0 {
		t.Skip("merge auto-resolved without persisted conflicts — cannot exercise post-CLI-pull resolver")
	}

	resolved, err := store.autoResolveConflictsAfterCLIPull(ctx)
	if err != nil {
		t.Fatalf("autoResolveConflictsAfterCLIPull: %v", err)
	}
	if !resolved {
		t.Fatal("expected audit-only dependency conflict to auto-resolve after a CLI pull")
	}

	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = 'depc-x' AND depends_on_issue_id = 'depc-y'").Scan(&count); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 dependency row after resolve, got %d", count)
	}
}

// TestSettleMergeInTxKeepsCleanMergedTables (bd-cdo628, GH#7811) is the
// server-mode counterpart of the embedded auto-resolve regression: a forced
// merge whose only conflict is an auto-resolvable metadata modify/modify
// rides together with the peer's cleanly merged label swap, and
// settleMergeInTx must conclude the merge with BOTH — CommitResolvedConflicts
// stages the merge's clean output before committing, or the settlement
// commit reverts the peer's label change while the working set keeps it as
// an unstaged divergence. Deliberately no trailing DOLT_COMMIT('-Am') sweep,
// which would mask the drop by committing the working set afterwards.
func TestSettleMergeInTxKeepsCleanMergedTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}

	peerBranch := currentBranch + "_labelspeer"
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-Df', ?)", peerBranch)
	})

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Base: the annotated issue and a metadata row both branches will modify.
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('sltx-1', 'X', '', '', '', '', 'open', 3, 'bug')")
	exec("INSERT INTO labels (issue_id, label) VALUES ('sltx-1', 'needs-triage')")
	exec("INSERT INTO metadata (`key`, value) VALUES ('dolt_auto_push_commit', 'base')")
	exec("CALL DOLT_COMMIT('-Am', 'seed issue + label + metadata')")
	exec("CALL DOLT_BRANCH(?, 'HEAD')", peerBranch)
	// Main modifies the metadata key; peer modifies it differently and swaps
	// the label — a metadata modify/modify (auto-resolved theirs) riding
	// with a cleanly merged labels change.
	exec("UPDATE metadata SET value = 'aaa' WHERE `key` = 'dolt_auto_push_commit'")
	exec("CALL DOLT_COMMIT('-Am', 'main metadata aaa')")
	exec("CALL DOLT_CHECKOUT(?)", peerBranch)
	exec("UPDATE metadata SET value = 'bbb' WHERE `key` = 'dolt_auto_push_commit'")
	exec("DELETE FROM labels WHERE issue_id = 'sltx-1' AND label = 'needs-triage'")
	exec("INSERT INTO labels (issue_id, label) VALUES ('sltx-1', 'ready-for-agent')")
	exec("CALL DOLT_COMMIT('-Am', 'peer metadata bbb + relabel')")
	exec("CALL DOLT_CHECKOUT(?)", currentBranch)

	if err := settleForcedMerge(ctx, t, store, peerBranch); err != nil {
		t.Fatalf("settling merge with clean label ride-along: %v", err)
	}

	labelsAt := func(q string) []string {
		t.Helper()
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var l string
			if err := rows.Scan(&l); err != nil {
				t.Fatalf("scan label: %v", err)
			}
			got = append(got, l)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate labels: %v", err)
		}
		return got
	}
	committed := labelsAt("SELECT label FROM labels AS OF 'HEAD' WHERE issue_id = 'sltx-1' ORDER BY label")
	if len(committed) != 1 || committed[0] != "ready-for-agent" {
		t.Errorf("HEAD labels = %v, want [ready-for-agent]: the settlement commit did not carry the peer's label swap", committed)
	}
	var value string
	if err := db.QueryRowContext(ctx,
		"SELECT value FROM metadata AS OF 'HEAD' WHERE `key` = 'dolt_auto_push_commit'").Scan(&value); err != nil {
		t.Fatalf("read resolved metadata: %v", err)
	}
	if value != "bbb" {
		t.Errorf("metadata value = %q, want %q (theirs)", value, "bbb")
	}
	var dirty int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d table(s) left uncommitted after the settlement", dirty)
	}
}

// persistConflictedCLIMerge persists a conflicted merge into the shared
// working set exactly the way a CLI `dolt pull` subprocess leaves it: the
// merge ran outside any transaction bd can inspect, its conflicts are live
// in dolt_conflicts, and dolt_merge_status.is_merging is 1.
func persistConflictedCLIMerge(ctx context.Context, t *testing.T, store *DoltStore, peerBranch string) {
	t.Helper()
	db := store.db
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("allow commit conflicts: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		t.Logf("merge returned: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("persist conflicted working set: %v", err)
	}
	var merging bool
	if err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		t.Fatalf("read dolt_merge_status: %v", err)
	}
	if !merging {
		t.Fatal("precondition: expected an open merge in the working set after the simulated CLI pull")
	}
}

// TestFinishCLIPull_DeclinedConflictAbortsMerge pins the CLI-pull route against
// GH#7839: when the resolver declines a conflict (here a same-edge dependency whose
// type differs), finishCLIPull must abort the merge the CLI `dolt pull` left in the
// shared working set, as the SQL pull route does. Leaving it open (is_merging=1)
// blocks every later write and pull until an operator runs DOLT_MERGE('--abort').
func TestFinishCLIPull_DeclinedConflictAbortsMerge(t *testing.T) {
	store, peerBranch := setupDependencyMergeConflict(t, "blocks", "alice", "related", "alice")

	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	preHead, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("read pre-pull head: %v", err)
	}

	persistConflictedCLIMerge(ctx, t, store, peerBranch)

	pullErr := errors.New("dolt pull failed: Automatic merge failed; fix conflicts and then commit the result")
	err = store.finishCLIPull(ctx, pullErr)

	var mce *versioncontrolops.MergeConflictsError
	if !errors.As(err, &mce) {
		t.Fatalf("finishCLIPull error = %v, want *MergeConflictsError carrying the declined conflicts", err)
	}
	if len(mce.Conflicts) != 1 || mce.Conflicts[0].Field != "dependencies" {
		t.Errorf("MergeConflictsError.Conflicts = %v, want the declined dependencies conflict", mce.Conflicts)
	}
	if !errors.Is(err, pullErr) {
		t.Error("the conflicts error does not wrap the pull's own error")
	}

	var merging bool
	if err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		t.Fatalf("read dolt_merge_status after finishCLIPull: %v", err)
	}
	if merging {
		t.Fatal("declined CLI-pull conflict left the merge open (dolt_merge_status.is_merging=1); want it aborted")
	}
	var conflicts int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("count dolt_conflicts: %v", err)
	}
	if conflicts != 0 {
		t.Errorf("%d conflicted table(s) remain after the declined CLI pull; want the merge aborted", conflicts)
	}

	// The abort restores the pre-pull state: same head, our side of the
	// declined edge intact.
	if got, err := store.GetCurrentCommit(ctx); err != nil {
		t.Fatalf("read head after finishCLIPull: %v", err)
	} else if got != preHead {
		t.Errorf("head = %s after the aborted pull, want the pre-pull head %s", got, preHead)
	}
	var depType string
	if err := db.QueryRowContext(ctx,
		"SELECT type FROM dependencies WHERE issue_id = 'depc-x' AND depends_on_issue_id = 'depc-y'").Scan(&depType); err != nil {
		t.Fatalf("read local edge after the aborted pull: %v", err)
	}
	if depType != "blocks" {
		t.Errorf("local edge type = %q after the abort, want our pre-pull value %q", depType, "blocks")
	}
}

// TestFinishCLIPull_UnrepairedViolationsAbortsMerge pins the FK-violations half
// of the declined CLI-pull settlement (GH#7839): a merge whose FK cascade
// violations the repair cannot fix used to be rolled back and left in the
// shared working set with the merge OPEN — the same is_merging=1 wedge as a
// declined conflict. finishCLIPull must abort it and name the unrepaired
// tables, and must NOT report *MergeConflictsError: the abort wipes
// dolt_constraint_violations with the rest of the merge state, so there are no
// conflict rows for the SQL route's contract to carry.
func TestFinishCLIPull_UnrepairedViolationsAbortsMerge(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	// A child table with an FK to issues that is deliberately NOT on the
	// repair allowlist (fkCascadeRepairDeletes): the repair declines it
	// exactly as it would a child table a future migration adds.
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE cli_decline_children (id INT PRIMARY KEY, issue_id VARCHAR(255), "+
			"CONSTRAINT fk_cli_decline_issue FOREIGN KEY (issue_id) REFERENCES issues(id))"); err != nil {
		t.Fatalf("create child table: %v", err)
	}

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('decl-x', 'X', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed decl-x')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	peerBranch := currentBranch + "_declinepeer"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-Df', ?)", peerBranch)
	})

	// The delete-vs-insert hazard (bd-6dnrw.4): the current branch deletes the
	// issue while the peer branch inserts a child row referencing it.
	if _, err := db.ExecContext(ctx, "DELETE FROM issues WHERE id = 'decl-x'"); err != nil {
		t.Fatalf("delete issue on current: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'delete decl-x')"); err != nil {
		t.Fatalf("commit delete: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO cli_decline_children (id, issue_id) VALUES (1, 'decl-x')"); err != nil {
		t.Fatalf("insert child row on peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'add child row')"); err != nil {
		t.Fatalf("commit child row: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current: %v", err)
	}

	preHead, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("read pre-pull head: %v", err)
	}

	// Persist the violated merge into the shared working set exactly the way
	// a CLI `dolt pull` leaves it (bd-6dnrw.4's TestFKCascadeViolationRepairOnCLIPull
	// stage): forced merge committed with its violations, no transaction to inspect.
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
	if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		t.Logf("merge returned: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("persist violated working set: %v", err)
	}
	var violating string
	if err := db.QueryRowContext(ctx,
		"SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0").Scan(&violating); err != nil {
		t.Fatalf("precondition: read violating table: %v", err)
	}
	if violating != "cli_decline_children" {
		t.Fatalf("precondition: violating table = %q, want cli_decline_children", violating)
	}

	pullErr := errors.New("dolt pull failed: exit status 1")
	err = store.finishCLIPull(ctx, pullErr)

	var mce *versioncontrolops.MergeConflictsError
	if errors.As(err, &mce) {
		t.Fatalf("finishCLIPull error = %v, want a constraint-violations error, not *MergeConflictsError", err)
	}
	if !errors.Is(err, pullErr) {
		t.Errorf("the violations error does not wrap the pull's own error: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "cli_decline_children") {
		t.Errorf("error %q does not name the unrepaired table", msg)
	}

	var merging bool
	if err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		t.Fatalf("read dolt_merge_status after finishCLIPull: %v", err)
	}
	if merging {
		t.Fatal("unrepaired-violation CLI pull left the merge open (dolt_merge_status.is_merging=1); want it aborted")
	}
	var violations int
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(num_violations),0) FROM dolt_constraint_violations").Scan(&violations); err != nil {
		t.Fatalf("count surviving violations: %v", err)
	}
	if violations != 0 {
		t.Errorf("%d violation(s) remain in the working set; want the merge aborted", violations)
	}
	if got, err := store.GetCurrentCommit(ctx); err != nil {
		t.Fatalf("read head after the aborted pull: %v", err)
	} else if got != preHead {
		t.Errorf("head = %s after the aborted pull, want the pre-pull head %s", got, preHead)
	}
	var childRows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM cli_decline_children").Scan(&childRows); err != nil {
		t.Fatalf("count child rows after the abort: %v", err)
	} else if childRows != 0 {
		t.Errorf("%d child row(s) survive the abort; want the pre-pull working set restored", childRows)
	}
}
