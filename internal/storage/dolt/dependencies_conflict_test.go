package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
)

// setupDependencyMergeConflict seeds issues x and y on a shared ancestor, then
// creates the SAME edge (same deterministic id) on the current branch and on a
// peer branch with the given per-branch (type, created_by), and merges the peer
// back with conflicts allowed. It returns the open transaction (mid-merge) and a
// cleanup that restores the branch. The caller drives the resolver and commits.
func setupDependencyMergeConflict(t *testing.T, ourType, ourBy, theirType, theirBy string) (*DoltStore, string) {
	t.Helper()
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)

	ctx, cancel := testContext(t)
	t.Cleanup(cancel)

	db := store.db
	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	for _, id := range []string{"depc-x", "depc-y"} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, '', '', '', '', 'open', 2, 'task')",
			id, id); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issues')"); err != nil {
		t.Fatalf("commit seed issues: %v", err)
	}

	edgeID := depid.New("depc-x", "depc-y")
	insert := func(depType, by string) {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (?, 'depc-x', 'depc-y', ?, NOW(), ?)",
			edgeID, depType, by); err != nil {
			t.Fatalf("insert edge (%s, %s): %v", depType, by, err)
		}
	}

	// Current branch creates its version of the edge.
	insert(ourType, ourBy)
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'edge on current')"); err != nil {
		t.Fatalf("commit edge on current: %v", err)
	}

	// Peer branch forks from the shared ancestor (HEAD~1, before the edge) and
	// creates its own version of the same edge.
	peerBranch := currentBranch + "_peer"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD~1')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer branch: %v", err)
	}
	insert(theirType, theirBy)
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'edge on peer')"); err != nil {
		t.Fatalf("commit edge on peer: %v", err)
	}

	// Back on current, merge the peer (allowing conflicts so the resolver can run).
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current branch: %v", err)
	}
	return store, peerBranch
}

// TestTryAutoResolveMergeConflicts_DependencyAuditOnly verifies that a same-edge
// dependency conflict that differs only in audit columns is auto-resolved (#4259
// Hazard B), converging to a single row.
func TestTryAutoResolveMergeConflicts_DependencyAuditOnly(t *testing.T) {
	store, peerBranch := setupDependencyMergeConflict(t, "blocks", "alice", "blocks", "bob")
	ctx, cancel := testContext(t)
	defer cancel()
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
		// Some Dolt versions report the conflict as a merge error; the resolver
		// inspects dolt_conflicts regardless.
		t.Logf("merge returned: %v", err)
	}

	resolved, err := store.tryAutoResolveMergeConflicts(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("resolver error: %v", err)
	}
	if !resolved {
		_ = tx.Rollback()
		t.Fatal("expected audit-only dependency conflict to be auto-resolved")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit after resolve: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = 'depc-x' AND depends_on_issue_id = 'depc-y'").Scan(&count); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 dependency row after auto-resolve, got %d", count)
	}
}

// TestTryAutoResolveMergeConflicts_DependencyTypeConflictLeftAlone verifies that
// a same-edge dependency conflict where the type differs is NOT auto-resolved —
// it is a real semantic conflict left for the operator.
func TestTryAutoResolveMergeConflicts_DependencyTypeConflictLeftAlone(t *testing.T) {
	store, peerBranch := setupDependencyMergeConflict(t, "blocks", "alice", "related", "alice")
	ctx, cancel := testContext(t)
	defer cancel()
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

	resolved, err := store.tryAutoResolveMergeConflicts(ctx, tx)
	_ = tx.Rollback()
	if err != nil {
		t.Fatalf("resolver error: %v", err)
	}
	if resolved {
		t.Error("expected a differing-type dependency conflict to be left unresolved")
	}
}
