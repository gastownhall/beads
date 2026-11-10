package dolt

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// mergeAsCLIPull runs the CLI routes' merge of ref (mergeFetchedInTx) on the
// test store's own branch, in a transaction with the session flags
// beginPullSettleTx sets. Production opens that transaction on a fresh
// connection, which would sit on the database's default branch rather than
// the test's.
func mergeAsCLIPull(ctx context.Context, t *testing.T, store *DoltStore, ref string) (pullReport, error) {
	t.Helper()
	return store.mergeFetchedInTx(ctx, beginForcedMergeTx(ctx, t, store.db), ref)
}

// persistPeerMerge merges peerBranch into the current branch and commits the
// result with its conflicts (and, with force, its constraint violations): a
// merge another client left open in the working set, as a hand-run `dolt
// pull` or an older bd's CLI pull does.
func persistPeerMerge(ctx context.Context, t *testing.T, db *sql.DB, peerBranch string, force bool) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin merge tx: %v", err)
	}
	settings := []string{"SET @@dolt_allow_commit_conflicts = 1"}
	if force {
		settings = append(settings, "SET @@dolt_force_transaction_commit = 1")
	}
	for _, q := range settings {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			_ = tx.Rollback()
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		t.Logf("merge returned: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("persist merged working set: %v", err)
	}
	if !isMerging(ctx, t, db) {
		t.Fatal("fixture merge is not open in the working set")
	}
}

// assertMergeAborted fails unless the working set holds no merge, conflict or
// constraint violation and HEAD is still preHead: the declined merge rolled
// back and nothing was committed on top of the pre-pull history.
func assertMergeAborted(ctx context.Context, t *testing.T, db *sql.DB, preHead string) {
	t.Helper()
	if isMerging(ctx, t, db) {
		t.Fatal("declined merge is still open (dolt_merge_status.is_merging=1); want it rolled back")
	}
	var conflicts, violations int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("count dolt_conflicts: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_constraint_violations").Scan(&violations); err != nil {
		t.Fatalf("count dolt_constraint_violations: %v", err)
	}
	if conflicts != 0 || violations != 0 {
		t.Fatalf("settlement left %d conflicted and %d violated table(s)", conflicts, violations)
	}
	if head := readHead(ctx, t, db); head != preHead {
		t.Fatalf("HEAD moved from %s to %s; the declined merge must not be committed", preHead, head)
	}
}

// assertOtherClientsMergeLeftAlone fails unless the merge another client left
// open is still open with its conflicts, nothing was committed over it, and
// err refuses the pull without claiming a rollback of that merge.
func assertOtherClientsMergeLeftAlone(ctx context.Context, t *testing.T, db *sql.DB, preHead string, err error, peerBranch string) {
	t.Helper()
	var mce *versioncontrolops.MergeConflictsError
	if err == nil || errors.As(err, &mce) {
		t.Fatalf("merge = %v; another client's open merge must be refused, not settled", err)
	}
	var branch string
	if qErr := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch); qErr != nil {
		t.Fatalf("read active branch: %v", qErr)
	}
	want := "a merge of " + peerBranch + " into refs/heads/" + branch + " is already open"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("merge error %q does not mention %q", err, want)
	}
	if !isMerging(ctx, t, db) {
		t.Fatal("the other client's merge is closed; want it still open")
	}
	var conflicts int
	if qErr := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); qErr != nil {
		t.Fatalf("count dolt_conflicts: %v", qErr)
	}
	if conflicts == 0 {
		t.Fatal("the other client's conflicts are gone; want them left for that client")
	}
	if head := readHead(ctx, t, db); head != preHead {
		t.Fatalf("HEAD moved from %s to %s; nothing may be committed over another client's merge", preHead, head)
	}
}

func isMerging(ctx context.Context, t *testing.T, db *sql.DB) bool {
	t.Helper()
	var merging bool
	if err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		t.Fatalf("read dolt_merge_status: %v", err)
	}
	return merging
}

func readHead(ctx context.Context, t *testing.T, db *sql.DB) string {
	t.Helper()
	var head string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&head); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	return head
}

func declinedTables(conflicts []storage.Conflict) []string {
	tables := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		tables = append(tables, c.Field)
	}
	return tables
}

// TestCLIPullDeclinedSettlement pins GH#7839 on the CLI routes' merge
// (mergeFetchedInTx), which runs in bd's own transaction: a merge the
// resolver declines rolls back to the pre-pull HEAD and leaves nothing open,
// with row conflicts reported as *MergeConflictsError (AC-1) and unrepairable
// FK violations as a non-conflict error (AC-4). A merge another client left
// open in the working set is not this pull's: it is neither merged over,
// settled nor aborted.
func TestCLIPullDeclinedSettlement(t *testing.T) {
	t.Run("declined dependency conflict", func(t *testing.T) {
		store, peerBranch := setupDependencyMergeConflict(t, "blocks", "alice", "related", "alice")
		ctx, cancel := testContext(t)
		defer cancel()
		db := store.db
		preHead := readHead(ctx, t, db)

		report, err := mergeAsCLIPull(ctx, t, store, peerBranch)
		var mce *versioncontrolops.MergeConflictsError
		if !errors.As(err, &mce) {
			t.Fatalf("merge = %v, want *MergeConflictsError", err)
		}
		if !slices.Contains(declinedTables(mce.Conflicts), "dependencies") {
			t.Fatalf("MergeConflictsError names %v, want dependencies", declinedTables(mce.Conflicts))
		}
		if !report.Conflicted {
			t.Fatalf("merge report = %+v; DOLT_MERGE reported no conflict", report)
		}
		assertMergeAborted(ctx, t, db, preHead)
		var depType string
		if err := db.QueryRowContext(ctx, "SELECT type FROM dependencies WHERE id = ?",
			depid.New("depc-x", "depc-y")).Scan(&depType); err != nil {
			t.Fatalf("read local edge: %v", err)
		}
		if depType != "blocks" {
			t.Fatalf("local edge type = %q after the rollback, want the pre-pull %q", depType, "blocks")
		}
	})

	t.Run("unrepairable FK violation", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()
		db := store.db

		// A test-local FK outside the cascade repair's allowlist: the current
		// branch deletes the parent the peer branch gives a new child.
		var currentBranch string
		if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
			t.Fatalf("get current branch: %v", err)
		}
		peerBranch := currentBranch + "_fkdecline"
		for _, step := range []struct {
			query string
			args  []any
		}{
			{"CREATE TABLE cpd_parent (id VARCHAR(32) PRIMARY KEY)", nil},
			{"CREATE TABLE cpd_child (id VARCHAR(32) PRIMARY KEY, parent_id VARCHAR(32), " +
				"CONSTRAINT fk_cpd_child_parent FOREIGN KEY (parent_id) REFERENCES cpd_parent (id))", nil},
			{"INSERT INTO cpd_parent (id) VALUES ('p1')", nil},
			{"CALL DOLT_COMMIT('-Am', 'seed cpd tables')", nil},
			{"CALL DOLT_BRANCH(?, 'HEAD')", []any{peerBranch}},
			{"DELETE FROM cpd_parent WHERE id = 'p1'", nil},
			{"CALL DOLT_COMMIT('-Am', 'delete parent')", nil},
			{"CALL DOLT_CHECKOUT(?)", []any{peerBranch}},
			{"INSERT INTO cpd_child (id, parent_id) VALUES ('c1', 'p1')", nil},
			{"CALL DOLT_COMMIT('-Am', 'peer adds child')", nil},
			{"CALL DOLT_CHECKOUT(?)", []any{currentBranch}},
		} {
			if _, err := db.ExecContext(ctx, step.query, step.args...); err != nil {
				t.Fatalf("%s: %v", step.query, err)
			}
		}
		t.Cleanup(func() {
			db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
			db.ExecContext(ctx, "CALL DOLT_BRANCH('-Df', ?)", peerBranch)
		})
		preHead := readHead(ctx, t, db)

		_, err := mergeAsCLIPull(ctx, t, store, peerBranch)
		if err == nil {
			t.Fatal("merge = nil for an unrepairable FK violation")
		}
		var mce *versioncontrolops.MergeConflictsError
		if errors.As(err, &mce) {
			t.Fatalf("merge = %v; FK violations without row conflicts are not a MergeConflictsError", err)
		}
		if !strings.Contains(err.Error(), "constraint violations") {
			t.Fatalf("merge error %q does not mention the constraint violations", err)
		}
		assertMergeAborted(ctx, t, db, preHead)
		var childRows int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM cpd_child").Scan(&childRows); err != nil {
			t.Fatalf("count child rows after the rollback: %v", err)
		}
		if childRows != 0 {
			t.Fatalf("%d cpd_child row(s) survive the rollback; want the pre-pull working set", childRows)
		}
	})

	t.Run("declined conflict beside a repairable FK violation", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()
		db := store.db

		var currentBranch string
		if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
			t.Fatalf("get current branch: %v", err)
		}
		peerBranch := currentBranch + "_mixdecline"
		edgeID := depid.New("cpm-a", "cpm-b")
		insertEdge := "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) " +
			"VALUES (?, 'cpm-a', 'cpm-b', ?, NOW(), 'alice')"
		for _, step := range []struct {
			query string
			args  []any
		}{
			{"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) " +
				"VALUES ('cpm-a', 'a', '', '', '', '', 'open', 2, 'task'), ('cpm-b', 'b', '', '', '', '', 'open', 2, 'task'), " +
				"('cpm-z', 'z', '', '', '', '', 'open', 2, 'task')", nil},
			{"CALL DOLT_COMMIT('-Am', 'seed issues')", nil},
			{"CALL DOLT_BRANCH(?, 'HEAD')", []any{peerBranch}},
			// Current: one edge type, and issue Z deleted.
			{insertEdge, []any{edgeID, "blocks"}},
			{"DELETE FROM issues WHERE id = 'cpm-z'", nil},
			{"CALL DOLT_COMMIT('-Am', 'current edge and delete z')", nil},
			// Peer: a different type on the same edge, and a label on Z — a
			// labels FK violation the cascade repair would fix on its own.
			{"CALL DOLT_CHECKOUT(?)", []any{peerBranch}},
			{insertEdge, []any{edgeID, "related"}},
			{"INSERT INTO labels (issue_id, label) VALUES ('cpm-z', 'late-label')", nil},
			{"CALL DOLT_COMMIT('-Am', 'peer edge and label on z')", nil},
			{"CALL DOLT_CHECKOUT(?)", []any{currentBranch}},
		} {
			if _, err := db.ExecContext(ctx, step.query, step.args...); err != nil {
				t.Fatalf("%s: %v", step.query, err)
			}
		}
		t.Cleanup(func() {
			db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
			db.ExecContext(ctx, "CALL DOLT_BRANCH('-Df', ?)", peerBranch)
		})
		preHead := readHead(ctx, t, db)

		_, err := mergeAsCLIPull(ctx, t, store, peerBranch)
		var mce *versioncontrolops.MergeConflictsError
		if !errors.As(err, &mce) {
			t.Fatalf("merge = %v, want *MergeConflictsError for the declined edge", err)
		}
		if !slices.Contains(declinedTables(mce.Conflicts), "dependencies") {
			t.Fatalf("MergeConflictsError names %v, want dependencies", declinedTables(mce.Conflicts))
		}
		// The FK repair must not settle the merge around the declined conflict.
		assertMergeAborted(ctx, t, db, preHead)
	})

	t.Run("declined merge another client opened is left alone", func(t *testing.T) {
		// The merge open in the working set is the fixture's merge of a local
		// branch, another client's work: the pull, which merges origin's main,
		// must refuse before it merges anything. Its conflicts must survive,
		// unaborted.
		store, peerBranch := setupDependencyMergeConflict(t, "blocks", "alice", "related", "alice")
		ctx, cancel := testContext(t)
		defer cancel()
		db := store.db
		preHead := readHead(ctx, t, db)
		persistPeerMerge(ctx, t, db, peerBranch, false)

		_, err := mergeAsCLIPull(ctx, t, store, "origin/main")
		assertOtherClientsMergeLeftAlone(ctx, t, db, preHead, err, peerBranch)
		if _, err := db.ExecContext(ctx, "CALL DOLT_MERGE('--abort')"); err != nil {
			t.Fatalf("clean up the fixture merge: %v", err)
		}
	})

	t.Run("auto-resolvable merge another client opened is not settled", func(t *testing.T) {
		// An audit-only edge conflict the resolver settles on its own when the
		// pull's own merge raises it (TestCLIPullMergeAutoResolvesAuditOnlyDependency):
		// in another client's open merge it is that client's to settle, so the
		// pull must not resolve and commit it either.
		store, peerBranch := setupDependencyMergeConflict(t, "blocks", "alice", "blocks", "bob")
		ctx, cancel := testContext(t)
		defer cancel()
		db := store.db
		preHead := readHead(ctx, t, db)
		persistPeerMerge(ctx, t, db, peerBranch, false)
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts_dependencies").Scan(&n); err != nil {
			t.Fatalf("count dependency conflicts: %v", err)
		}
		if n == 0 {
			t.Skip("merge auto-resolved without persisted conflicts — nothing for the pull to leave alone")
		}

		_, err := mergeAsCLIPull(ctx, t, store, "origin/main")
		assertOtherClientsMergeLeftAlone(ctx, t, db, preHead, err, peerBranch)
		if _, err := db.ExecContext(ctx, "CALL DOLT_MERGE('--abort')"); err != nil {
			t.Fatalf("clean up the fixture merge: %v", err)
		}
	})
}
