//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// These tests run the bd-6dnrw.4 / GH#2466 merge-settlement coverage against
// the EMBEDDED engine (bd-6dnrw.40): the embedded pull path
// (versioncontrolops.Pull → MergeAndSettle) historically had no conflict
// auto-resolution and no FK cascade repair, so a delete-vs-insert merge
// failed with a raw "constraint violations" error and could never converge.
// The merge ref here is a local peer branch — production pulls merge the
// remote tracking ref, which exercises the identical merge/settle code; only
// the DOLT_FETCH step is skipped.

// openSettleConn opens a raw SQL connection to the test env's database and
// pins a single *sql.Conn, mirroring withPinnedDBConn: MergeAndSettle's
// session flags must be visible to every subsequent statement.
func openSettleConn(t *testing.T, ctx context.Context, te *testEnv) *sql.Conn {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// embeddedFKCascadeCase mirrors the server-mode fkCascadeCase: insert runs on
// the peer branch and must reference issue fkc-x-<name>; orphanQuery counts
// the dangling rows the merge would leave (1 before repair, 0 after).
type embeddedFKCascadeCase struct {
	name        string
	insert      string
	orphanQuery string
}

var embeddedFKCascadeCases = []embeddedFKCascadeCase{
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
		// comments.id lost its DB-side default in 0051 (bd-2rd37); inserts
		// must supply an explicit id like the app does (bd-6dnrw.18).
		name: "comments",
		insert: "INSERT INTO comments (id, issue_id, author, text, created_at) " +
			"VALUES (UUID(), 'fkc-x-%[1]s', 'peer', 'late comment', NOW())",
		orphanQuery: "SELECT COUNT(*) FROM comments WHERE issue_id = 'fkc-x-%[1]s'",
	},
}

// seedEmbeddedFKCascadeDivergence seeds issues fkc-x-<name> (to be deleted)
// and fkc-w-<name>, commits, then diverges: main deletes X (the FK cascade
// removes any local children) while the returned peer branch runs the case's
// insert referencing X. Leaves main checked out on conn's session.
func seedEmbeddedFKCascadeDivergence(t *testing.T, ctx context.Context, conn *sql.Conn, tc embeddedFKCascadeCase) (peerBranch string) {
	t.Helper()

	x, w := "fkc-x-"+tc.name, "fkc-w-"+tc.name
	for _, id := range []string{x, w} {
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, '', '', '', '', 'open', 2, 'task')",
			id, id); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issues')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	peerBranch = "fkpeer_" + tc.name
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}

	// main (clone A): delete issue X — FK cascade removes children.
	if _, err := conn.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", x); err != nil {
		t.Fatalf("delete issue on main: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'delete issue x')"); err != nil {
		t.Fatalf("commit delete: %v", err)
	}

	// Peer branch (clone B): insert a new child row referencing X.
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(tc.insert, tc.name)); err != nil {
		t.Fatalf("peer insert into %s: %v", tc.name, err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'peer adds child row')"); err != nil {
		t.Fatalf("commit peer insert: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	return peerBranch
}

// TestEmbeddedMergeAndSettleFKCascadeRepair is the embedded-engine
// counterpart of TestFKCascadeViolationRepairOnSQLPull: the delete-vs-insert
// merge converges through MergeAndSettle — the dangling child row is removed
// (cascade semantics), the violations are cleared, and the result is
// committable.
func TestEmbeddedMergeAndSettleFKCascadeRepair(t *testing.T) {
	for _, tc := range embeddedFKCascadeCases {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t, "fksettle")
			ctx := t.Context()
			conn := openSettleConn(t, ctx, te)

			peerBranch := seedEmbeddedFKCascadeDivergence(t, ctx, conn, tc)

			if err := versioncontrolops.MergeAndSettle(ctx, conn, peerBranch); err != nil {
				t.Fatalf("settling FK-violating merge: %v", err)
			}

			var orphans int
			if err := conn.QueryRowContext(ctx, fmt.Sprintf(tc.orphanQuery, tc.name)).Scan(&orphans); err != nil {
				t.Fatalf("count dangling rows: %v", err)
			}
			if orphans != 0 {
				t.Errorf("repair left %d dangling %s row(s) referencing the deleted issue", orphans, tc.name)
			}
			var violations int
			if err := conn.QueryRowContext(ctx,
				"SELECT COALESCE(SUM(num_violations),0) FROM dolt_constraint_violations").Scan(&violations); err != nil {
				t.Fatalf("read dolt_constraint_violations: %v", err)
			}
			if violations != 0 {
				t.Errorf("%d constraint violations survive the repair", violations)
			}
			// The merged, repaired state must be committable — the unrepaired
			// state was not. An already-finalized merge ("nothing to commit")
			// is equally converged.
			if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'merge with cascade repair')"); err != nil &&
				!isNothingToCommit(err) {
				t.Errorf("post-repair DOLT_COMMIT: %v", err)
			}
		})
	}
}

func isNothingToCommit(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "nothing to commit") ||
		strings.Contains(err.Error(), "no changes added to commit"))
}

// TestEmbeddedMergeAndSettleRefusesUnknownTable proves the repair's allowlist
// on the embedded engine: a foreign-key violation on a table bd does not own
// fails the merge, and the abort restores the pre-merge working set so a
// retry is possible (the autocommit stand-in for server mode's tx rollback).
func TestEmbeddedMergeAndSettleRefusesUnknownTable(t *testing.T) {
	te := newTestEnv(t, "fkrefuse")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	if _, err := conn.ExecContext(ctx, `CREATE TABLE custom_notes (
		id INT PRIMARY KEY,
		issue_id VARCHAR(255) NOT NULL,
		CONSTRAINT fk_custom_notes_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
	)`); err != nil {
		t.Fatalf("create custom table: %v", err)
	}

	tc := embeddedFKCascadeCase{
		name:        "custom",
		insert:      "INSERT INTO custom_notes (id, issue_id) VALUES (1, 'fkc-x-%[1]s')",
		orphanQuery: "SELECT COUNT(*) FROM custom_notes WHERE issue_id = 'fkc-x-%[1]s'",
	}
	peerBranch := seedEmbeddedFKCascadeDivergence(t, ctx, conn, tc)

	if err := versioncontrolops.MergeAndSettle(ctx, conn, peerBranch); err == nil {
		t.Fatal("expected the merge to fail on a violation bd cannot repair, but it succeeded")
	}

	// The abort left nothing behind: the table bd does not own keeps its row
	// on the peer branch and main never absorbed the merge.
	var rows int
	if err := conn.QueryRowContext(ctx, fmt.Sprintf(tc.orphanQuery, tc.name)).Scan(&rows); err != nil {
		t.Fatalf("count custom rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("refused merge still landed %d row(s) in the working set", rows)
	}
	var violations int
	if err := conn.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(num_violations),0) FROM dolt_constraint_violations").Scan(&violations); err != nil {
		t.Fatalf("read dolt_constraint_violations: %v", err)
	}
	if violations != 0 {
		t.Errorf("refused merge left %d constraint violations in the working set", violations)
	}
}

// TestEmbeddedMergeAndSettleMetadataConflict is the embedded-engine
// counterpart of TestPullAutoResolveMetadataConflicts (GH#2466): a
// metadata-only merge conflict is auto-resolved with "theirs".
func TestEmbeddedMergeAndSettleMetadataConflict(t *testing.T) {
	te := newTestEnv(t, "metasettle")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	if _, err := conn.ExecContext(ctx,
		"INSERT INTO metadata (`key`, value) VALUES ('dolt_auto_push_commit', 'aaa')"); err != nil {
		t.Fatalf("insert metadata on main: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local metadata')"); err != nil {
		t.Fatalf("commit on main: %v", err)
	}

	// Divergent peer branch from before the local write, with a conflicting value.
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH('metapeer', 'HEAD~1')"); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('metapeer')"); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO metadata (`key`, value) VALUES ('dolt_auto_push_commit', 'bbb')"); err != nil {
		t.Fatalf("insert metadata on peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'peer metadata')"); err != nil {
		t.Fatalf("commit on peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	if err := versioncontrolops.MergeAndSettle(ctx, conn, "metapeer"); err != nil {
		t.Fatalf("settling metadata-conflicted merge: %v", err)
	}

	var value string
	if err := conn.QueryRowContext(ctx,
		"SELECT value FROM metadata WHERE `key` = 'dolt_auto_push_commit'").Scan(&value); err != nil {
		t.Fatalf("read resolved metadata: %v", err)
	}
	if value != "bbb" {
		t.Errorf("expected metadata value 'bbb' (theirs), got %q", value)
	}
}

// TestEmbeddedMergeAndSettleDirtyWorkingSetSurvivesRefusedMerge pins the
// bd-578h9.2 fix: when DOLT_MERGE refuses to start because the working set
// is dirty, no merge state exists, so DOLT_MERGE('--abort') fails too — and
// the old abortMerge fell back to DOLT_RESET('--hard'), silently destroying
// the uncommitted rows the merge never touched. The hard reset is now gated
// on the working set having been clean before the merge ran.
// TestEmbeddedMergeAndSettleReportsOperatorConflicts covers bd-578h9.15: a
// semantic conflict the resolver declines (here: both sides retitle the same
// issue) must surface as MergeConflictsError with the conflicted tables
// captured BEFORE the abort. The settle machinery aborts such merges, so a
// post-hoc GetConflicts sees an empty set — which had turned PullFrom's
// conflict-reporting contract into dead code.
func TestEmbeddedMergeAndSettleReportsOperatorConflicts(t *testing.T) {
	te := newTestEnv(t, "opconflict")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('opc-1', 'base', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issue')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH('opcpeer', 'HEAD')"); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "UPDATE issues SET title = 'ours' WHERE id = 'opc-1'"); err != nil {
		t.Fatalf("retitle on main: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'retitle ours')"); err != nil {
		t.Fatalf("commit main retitle: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('opcpeer')"); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "UPDATE issues SET title = 'theirs' WHERE id = 'opc-1'"); err != nil {
		t.Fatalf("retitle on peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'retitle theirs')"); err != nil {
		t.Fatalf("commit peer retitle: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}

	err := versioncontrolops.MergeAndSettle(ctx, conn, "opcpeer")
	var mce *versioncontrolops.MergeConflictsError
	if !errors.As(err, &mce) {
		t.Fatalf("want MergeConflictsError, got: %v", err)
	}
	foundIssues := false
	for _, c := range mce.Conflicts {
		if c.Field == "issues" {
			foundIssues = true
		}
	}
	if !foundIssues {
		t.Errorf("captured conflicts %+v do not name the issues table", mce.Conflicts)
	}

	// The merge must have been aborted: no live conflicts, local value intact.
	var liveConflicts int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_conflicts").Scan(&liveConflicts); err != nil {
		t.Fatalf("count live conflicts: %v", err)
	}
	if liveConflicts != 0 {
		t.Errorf("%d conflicted table(s) remain after the abort", liveConflicts)
	}
	var title string
	if err := conn.QueryRowContext(ctx,
		"SELECT title FROM issues WHERE id = 'opc-1'").Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "ours" {
		t.Errorf("local title = %q after aborted merge, want %q", title, "ours")
	}
}

func TestEmbeddedMergeAndSettleDirtyWorkingSetSurvivesRefusedMerge(t *testing.T) {
	te := newTestEnv(t, "dirtysettle")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	// Seed and commit a baseline, then diverge a peer branch so the merge
	// has real work to do (a fast-forward can succeed on a dirty set).
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('dirty-base', 'base', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed base issue: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed base')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH('dirtypeer', 'HEAD')"); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('dirtypeer')"); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('dirty-peer', 'peer', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed peer issue: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'peer work')"); err != nil {
		t.Fatalf("commit peer: %v", err)
	}
	// Diverge main too so the merge is a true three-way merge.
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('dirty-main', 'main', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed main issue: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'main work')"); err != nil {
		t.Fatalf("commit main: %v", err)
	}

	// The user's uncommitted work: present in the working set, never committed.
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('dirty-uncommitted', 'precious', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("insert uncommitted issue: %v", err)
	}

	mergeErr := versioncontrolops.MergeAndSettle(ctx, conn, "dirtypeer")
	if mergeErr == nil {
		t.Logf("note: this Dolt version merged over a dirty working set without refusing")
	} else {
		t.Logf("merge refused as expected: %v", mergeErr)
	}

	var count int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id = 'dirty-uncommitted'").Scan(&count); err != nil {
		t.Fatalf("count uncommitted issue: %v", err)
	}
	if count != 1 {
		t.Fatalf("uncommitted working-set row was destroyed by the merge abort path (count=%d)", count)
	}
}
