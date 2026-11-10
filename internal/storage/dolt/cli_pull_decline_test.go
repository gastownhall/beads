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

// persistPeerMerge merges peerBranch into the current branch and commits the
// result with its conflicts (and, with force, its constraint violations), so
// the working set is what a CLI `dolt pull` leaves behind for settleCLIPull.
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
		t.Fatal("fixture merge is not open in the working set; nothing for the CLI settle pass to decline")
	}
}

// assertMergeAborted fails unless the working set holds no merge, conflict or
// constraint violation and HEAD is still preHead: the declined merge was
// aborted and nothing was committed on top of the pre-pull history.
func assertMergeAborted(ctx context.Context, t *testing.T, db *sql.DB, preHead string) {
	t.Helper()
	if isMerging(ctx, t, db) {
		t.Fatal("declined merge is still open (dolt_merge_status.is_merging=1); want it aborted")
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

// TestCLIPullDeclinedSettlement pins GH#7839 on persisted CLI-pull merges: a
// merge the resolver declines is aborted back to the pre-pull HEAD, with row
// conflicts reported as *MergeConflictsError (AC-1) and unrepairable FK
// violations as a non-conflict error naming their table (AC-4).
func TestCLIPullDeclinedSettlement(t *testing.T) {
	t.Run("declined dependency conflict", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			pullErr error
		}{
			{"pull reported failure", errors.New("dolt pull: merge conflicts")},
			{"pull reported success", nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				store, peerBranch := setupDependencyMergeConflict(t, "blocks", "alice", "related", "alice")
				ctx, cancel := testContext(t)
				defer cancel()
				db := store.db
				preHead := readHead(ctx, t, db)
				persistPeerMerge(ctx, t, db, peerBranch, false)

				err := store.finishCLIPull(ctx, tc.pullErr)
				var mce *versioncontrolops.MergeConflictsError
				if !errors.As(err, &mce) {
					t.Fatalf("finishCLIPull = %v, want *MergeConflictsError", err)
				}
				if !slices.Contains(declinedTables(mce.Conflicts), "dependencies") {
					t.Fatalf("MergeConflictsError names %v, want dependencies", declinedTables(mce.Conflicts))
				}
				if mce.MergeErr != tc.pullErr {
					t.Fatalf("MergeConflictsError.MergeErr = %v, want the pull's error %v", mce.MergeErr, tc.pullErr)
				}
				assertMergeAborted(ctx, t, db, preHead)
				var depType string
				if err := db.QueryRowContext(ctx, "SELECT type FROM dependencies WHERE id = ?",
					depid.New("depc-x", "depc-y")).Scan(&depType); err != nil {
					t.Fatalf("read local edge: %v", err)
				}
				if depType != "blocks" {
					t.Fatalf("local edge type = %q after the abort, want the pre-pull %q", depType, "blocks")
				}
			})
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
		persistPeerMerge(ctx, t, db, peerBranch, true)

		err := store.finishCLIPull(ctx, nil)
		if err == nil {
			t.Fatal("finishCLIPull = nil for an unrepairable FK violation")
		}
		var mce *versioncontrolops.MergeConflictsError
		if errors.As(err, &mce) {
			t.Fatalf("finishCLIPull = %v; FK violations without row conflicts are not a MergeConflictsError", err)
		}
		for _, want := range []string{"constraint violations", "cpd_child", "merge aborted"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("finishCLIPull error %q does not mention %q", err, want)
			}
		}
		assertMergeAborted(ctx, t, db, preHead)
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
		persistPeerMerge(ctx, t, db, peerBranch, true)

		err := store.finishCLIPull(ctx, nil)
		var mce *versioncontrolops.MergeConflictsError
		if !errors.As(err, &mce) {
			t.Fatalf("finishCLIPull = %v, want *MergeConflictsError for the declined edge", err)
		}
		if !slices.Contains(declinedTables(mce.Conflicts), "dependencies") {
			t.Fatalf("MergeConflictsError names %v, want dependencies", declinedTables(mce.Conflicts))
		}
		// The FK repair must not settle the merge around the declined conflict.
		assertMergeAborted(ctx, t, db, preHead)
	})
}
