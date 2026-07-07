package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

// These tests cover the bd-6dnrw.4 hazard: migrations 0041/0042 put FOREIGN
// KEYs with ON DELETE/UPDATE CASCADE from the synced child tables
// (dependencies, labels, comments, events, snapshots) onto issues(id). Dolt
// merges row-wise and never re-executes cascades, so "clone A deletes an
// issue" merged with "clone B inserts a child row referencing it" produces a
// dangling child row — a foreign-key constraint violation that rolls the whole
// merge transaction back, with nothing in dolt_conflicts to resolve and no
// convergence on retry. tryRepairFKCascadeViolations applies the cascade
// semantics by hand (delete the dangling rows) so the pull converges.

// fkCascadeCase describes one child table's delete-vs-insert scenario. insert
// is run on the peer branch and must reference issue fkc-x-<name>; orphanQuery
// counts the dangling rows the merge would leave (1 before repair, 0 after).
type fkCascadeCase struct {
	name        string
	insert      string
	orphanQuery string
}

var fkCascadeCases = []fkCascadeCase{
	{
		// The dangling reference is on depends_on_issue_id (the edge's target),
		// NOT issue_id — the repair must cover both FK columns.
		name: "dependencies",
		insert: "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) " +
			"VALUES (UUID(), 'fkc-w-%[1]s', 'fkc-x-%[1]s', 'blocks', NOW(), 'peer')",
		orphanQuery: "SELECT COUNT(*) FROM dependencies WHERE depends_on_issue_id = 'fkc-x-%[1]s'",
	},
	{
		name:        "labels",
		insert:      "INSERT INTO labels (issue_id, label) VALUES ('fkc-x-%[1]s', 'late-label')",
		orphanQuery: "SELECT COUNT(*) FROM labels WHERE issue_id = 'fkc-x-%[1]s'",
	},
	{
		name: "comments",
		// comments.id lost its DB-side default in 0051 (bd-2rd37); inserts
		// must supply an explicit id like the app does (bd-6dnrw.18).
		insert: "INSERT INTO comments (id, issue_id, author, text, created_at) " +
			"VALUES (UUID(), 'fkc-x-%[1]s', 'peer', 'late comment', NOW())",
		orphanQuery: "SELECT COUNT(*) FROM comments WHERE issue_id = 'fkc-x-%[1]s'",
	},
}

// seedFKCascadeDivergence seeds issues fkc-x-<name> (to be deleted) and
// fkc-w-<name>, commits, then diverges: the current branch deletes X (the FK
// cascade removes any local children) while the returned peer branch runs the
// case's insert referencing X. Leaves the current branch checked out.
func seedFKCascadeDivergence(ctx context.Context, t *testing.T, db *sql.DB, tc fkCascadeCase) (peerBranch string) {
	t.Helper()

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	x, w := "fkc-x-"+tc.name, "fkc-w-"+tc.name
	for _, id := range []string{x, w} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, '', '', '', '', 'open', 2, 'task')",
			id, id); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issues')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	peerBranch = currentBranch + "_fkpeer"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-Df', ?)", peerBranch)
	})

	// Current branch (clone A): delete issue X — FK cascade removes children.
	if _, err := db.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", x); err != nil {
		t.Fatalf("delete issue on current: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'delete issue x')"); err != nil {
		t.Fatalf("commit delete: %v", err)
	}

	// Peer branch (clone B): insert a new child row referencing X.
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(tc.insert, tc.name)); err != nil {
		t.Fatalf("peer insert into %s: %v", tc.name, err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'peer adds child row')"); err != nil {
		t.Fatalf("commit peer insert: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current: %v", err)
	}
	return peerBranch
}

// TestFKCascadeDeleteVsInsertMergeFails documents the raw hazard: without the
// repair, the delete-vs-insert merge fails outright with a constraint
// violation ("transaction rolled back"), records NO conflicts to resolve, and
// retrying can never converge.
func TestFKCascadeDeleteVsInsertMergeFails(t *testing.T) {
	for _, tc := range fkCascadeCases {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			db := store.db

			peerBranch := seedFKCascadeDivergence(ctx, t, db, tc)

			_, mergeErr := db.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch)
			if mergeErr == nil {
				t.Fatal("expected the delete-vs-insert merge to fail with a constraint violation; it merged cleanly")
			}
			var nConf int
			if err := db.QueryRowContext(ctx, "SELECT COALESCE(SUM(num_conflicts),0) FROM dolt_conflicts").Scan(&nConf); err == nil && nConf != 0 {
				t.Errorf("expected no row conflicts (the violation is not a conflict), got %d", nConf)
			}
		})
	}
}

// settleForcedMerge mirrors pullWithAutoResolve's transaction shape on the
// store's own connection (the test harness isolates each test on a branch via
// session-level DOLT_CHECKOUT, so pullWithAutoResolve's fresh long-timeout
// connection would land on the shared default branch): set the session flags,
// run the merge, and hand the result to settleMergeInTx — resolve, repair,
// gate, commit/rollback.
func settleForcedMerge(ctx context.Context, t *testing.T, store *DoltStore, peerBranch string) error {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin merge tx: %v", err)
	}
	for _, q := range []string{"SET @@dolt_allow_commit_conflicts = 1", "SET @@dolt_force_transaction_commit = 1"} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			_ = tx.Rollback()
			t.Fatalf("%s: %v", q, err)
		}
	}
	_, mergeErr := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch)
	return store.settleMergeInTx(ctx, tx, mergeErr)
}

// TestFKCascadeViolationRepairOnSQLPull drives the production settle path
// every SQL pull route funnels through (forced transaction +
// tryRepairFKCascadeViolations + pre-commit violation gate) over the
// delete-vs-insert merge and asserts it converges: the dangling child row is
// removed (cascade semantics), the violations are cleared, and the result can
// be committed.
func TestFKCascadeViolationRepairOnSQLPull(t *testing.T) {
	for _, tc := range fkCascadeCases {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			db := store.db

			peerBranch := seedFKCascadeDivergence(ctx, t, db, tc)

			if err := settleForcedMerge(ctx, t, store, peerBranch); err != nil {
				t.Fatalf("settling FK-violating merge: %v", err)
			}

			var orphans int
			if err := db.QueryRowContext(ctx, fmt.Sprintf(tc.orphanQuery, tc.name)).Scan(&orphans); err != nil {
				t.Fatalf("count dangling rows: %v", err)
			}
			if orphans != 0 {
				t.Errorf("repair left %d dangling %s row(s) referencing the deleted issue", orphans, tc.name)
			}
			var violations int
			if err := db.QueryRowContext(ctx,
				"SELECT COALESCE(SUM(num_violations),0) FROM dolt_constraint_violations").Scan(&violations); err != nil {
				t.Fatalf("read dolt_constraint_violations: %v", err)
			}
			if violations != 0 {
				t.Errorf("%d constraint violations survive the repair", violations)
			}
			// The merged, repaired state must be committable — the unrepaired
			// state was not. An already-finalized merge ("nothing to commit")
			// is equally converged.
			if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'merge with cascade repair')"); err != nil && !isDoltNothingToCommit(err) {
				t.Errorf("post-repair DOLT_COMMIT: %v", err)
			}
		})
	}
}

// TestFKCascadeViolationRepairOnCLIPull covers the CLI pull route: a CLI
// `dolt pull` leaves the violated merge in the shared working set with no
// transaction to inspect, and autoResolveConflictsAfterCLIPull repairs it
// after the fact. The violated working set is staged here by committing a
// forced merge transaction without settling it — exactly the state a CLI pull
// leaves behind.
func TestFKCascadeViolationRepairOnCLIPull(t *testing.T) {
	tc := fkCascadeCases[1] // labels — any synced child table works
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	peerBranch := seedFKCascadeDivergence(ctx, t, db, tc)

	// Simulate the CLI pull: forced merge committed with its violations.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin merge tx: %v", err)
	}
	for _, q := range []string{"SET @@dolt_allow_commit_conflicts = 1", "SET @@dolt_force_transaction_commit = 1"} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		t.Fatalf("forced merge: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit violated working set: %v", err)
	}

	resolved, err := store.autoResolveConflictsAfterCLIPull(ctx)
	if err != nil {
		t.Fatalf("autoResolveConflictsAfterCLIPull: %v", err)
	}
	if !resolved {
		t.Fatal("expected the CLI-pull resolver to repair the FK cascade violations")
	}

	var orphans int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(tc.orphanQuery, tc.name)).Scan(&orphans); err != nil {
		t.Fatalf("count dangling rows: %v", err)
	}
	if orphans != 0 {
		t.Errorf("repair left %d dangling %s row(s)", orphans, tc.name)
	}
	var violations int
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(num_violations),0) FROM dolt_constraint_violations").Scan(&violations); err != nil {
		t.Fatalf("read dolt_constraint_violations: %v", err)
	}
	if violations != 0 {
		t.Errorf("%d constraint violations survive the repair", violations)
	}
}

// TestFKCascadeRepairWithAutoResolvableConflicts covers bd-578h9.14: ONE merge
// carrying BOTH settle classes — an auto-resolvable metadata conflict (GH#2466)
// and an FK cascade violation (bd-6dnrw.4). The resolver used to conclude with
// DOLT_COMMIT while the violations were still outstanding; DOLT_COMMIT refuses
// a violated working set, so the settle aborted even though each class alone
// settles fine. Realistic single merge: clone A deletes issue X while clone B
// adds a child row to X, and both clones touched the same metadata row.
func TestFKCascadeRepairWithAutoResolvableConflicts(t *testing.T) {
	tc := fkCascadeCases[1] // labels
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	// Seed the metadata row both branches will rewrite. It lands in
	// seedFKCascadeDivergence's "seed issues" commit, the branches' common base.
	if _, err := db.ExecContext(ctx,
		"REPLACE INTO metadata (`key`, value) VALUES ('bd578h914-key', 'base')"); err != nil {
		t.Fatalf("seed metadata row: %v", err)
	}
	peerBranch := seedFKCascadeDivergence(ctx, t, db, tc)

	// Diverge the metadata row on both branches on top of the FK divergence.
	if _, err := db.ExecContext(ctx,
		"UPDATE metadata SET value = 'ours' WHERE `key` = 'bd578h914-key'"); err != nil {
		t.Fatalf("update metadata on current: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'metadata ours')"); err != nil {
		t.Fatalf("commit metadata on current: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE metadata SET value = 'theirs' WHERE `key` = 'bd578h914-key'"); err != nil {
		t.Fatalf("update metadata on peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'metadata theirs')"); err != nil {
		t.Fatalf("commit metadata on peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current: %v", err)
	}

	if err := settleForcedMerge(ctx, t, store, peerBranch); err != nil {
		t.Fatalf("settling combined conflict+violation merge: %v", err)
	}

	var metaVal string
	if err := db.QueryRowContext(ctx,
		"SELECT value FROM metadata WHERE `key` = 'bd578h914-key'").Scan(&metaVal); err != nil {
		t.Fatalf("read metadata row: %v", err)
	}
	if metaVal != "theirs" {
		t.Errorf("metadata conflict resolved to %q, want %q (--theirs)", metaVal, "theirs")
	}
	var orphans int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(tc.orphanQuery, tc.name)).Scan(&orphans); err != nil {
		t.Fatalf("count dangling rows: %v", err)
	}
	if orphans != 0 {
		t.Errorf("repair left %d dangling %s row(s)", orphans, tc.name)
	}
	var violations int
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(num_violations),0) FROM dolt_constraint_violations").Scan(&violations); err != nil {
		t.Fatalf("read dolt_constraint_violations: %v", err)
	}
	if violations != 0 {
		t.Errorf("%d constraint violations survive the settle", violations)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'combined settle')"); err != nil && !isDoltNothingToCommit(err) {
		t.Errorf("post-settle DOLT_COMMIT: %v", err)
	}
}

// TestFKCascadeRepairRefusesUnknownTable proves the repair's allowlist: a
// foreign-key violation on a table bd does not own is left untouched for the
// operator and the pull fails instead of guessing.
func TestFKCascadeRepairRefusesUnknownTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	if _, err := db.ExecContext(ctx, `CREATE TABLE custom_notes (
		id INT PRIMARY KEY,
		issue_id VARCHAR(255) NOT NULL,
		CONSTRAINT fk_custom_notes_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
	)`); err != nil {
		t.Fatalf("create custom table: %v", err)
	}

	tc := fkCascadeCase{
		name:        "custom",
		insert:      "INSERT INTO custom_notes (id, issue_id) VALUES (1, 'fkc-x-%[1]s')",
		orphanQuery: "SELECT COUNT(*) FROM custom_notes WHERE issue_id = 'fkc-x-%[1]s'",
	}
	peerBranch := seedFKCascadeDivergence(ctx, t, db, tc)

	if err := settleForcedMerge(ctx, t, store, peerBranch); err == nil {
		t.Fatal("expected the pull to fail on a violation bd cannot repair, but it succeeded")
	}

	// Nothing was committed: the table bd does not own keeps its row on the
	// peer branch and the current branch never absorbed the merge.
	var rows int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(tc.orphanQuery, tc.name)).Scan(&rows); err != nil {
		t.Fatalf("count custom rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("refused merge still landed %d row(s) in the working set", rows)
	}
}
