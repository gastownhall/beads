package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/types"
)

// TestAddDependencyUsesDeterministicID proves the production insert path derives
// the dependency primary key deterministically from the natural edge key
// (issue_id, target) rather than a random UUID — the heart of the #4259 fix.
func TestAddDependencyUsesDeterministicID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db
	for _, id := range []string{"det-x", "det-y"} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, '', '', '', '', 'open', 2, 'task')",
			id, id); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}

	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "det-x", DependsOnID: "det-y", Type: types.DepBlocks}, "alice"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	var gotID string
	if err := db.QueryRowContext(ctx,
		"SELECT id FROM dependencies WHERE issue_id = 'det-x' AND depends_on_issue_id = 'det-y'").Scan(&gotID); err != nil {
		t.Fatalf("read edge id: %v", err)
	}
	if want := depid.New("det-x", "det-y"); gotID != want {
		t.Errorf("AddDependency wrote id %q, want deterministic %q (a random UUID here is the #4259 bug)", gotID, want)
	}
}

// TestDependencyDeterministicIDMergesAcrossBranches is the cross-clone
// merge-safety regression test for #4259. It merges two divergent branches that
// each create the same logical edge (issue_id, target), demonstrating both halves
// of why a deterministic id matters:
//
//   - deterministic ids: both branches use the same primary key, so the same edge
//     is one row. With identical edge content the merge is clean and yields
//     exactly one dependency (no duplicate, no uk_dep_* collision).
//   - random ids (negative control): the same edge under two different primary
//     keys brings in two rows that violate uk_dep_issue_target, and the merge
//     does not cleanly converge — exactly the unrecoverable `bd dolt pull` break
//     this fix removes.
//
// The deterministic-id value used here is depid.New(...), the same value the
// production insert paths compute (see TestAddDependencyUsesDeterministicID).
func TestDependencyDeterministicIDMergesAcrossBranches(t *testing.T) {
	t.Run("deterministic ids converge to one row", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		x, y := "conv-x", "conv-y"
		id := depid.New(x, y)
		// Identical edge content on both branches (same id, same audit columns):
		// the byte-identical case, e.g. an edge synced before two clones migrated
		// independently. It must merge cleanly to a single row.
		insert := "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) " +
			"VALUES ('" + id + "', '" + x + "', '" + y + "', 'blocks', '2020-01-01 00:00:00', 'sync')"
		mergeErr, rows := runTwoBranchEdgeMerge(t, ctx, store, x, y, insert, insert)
		if mergeErr != nil {
			t.Fatalf("merging identical deterministic-id edges should be clean, got: %v", mergeErr)
		}
		if rows != 1 {
			t.Errorf("expected exactly 1 dependency row after merge, got %d", rows)
		}
	})

	t.Run("random ids collide (negative control)", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		x, y := "rand-x", "rand-y"
		// Same logical edge, two different (random) primary keys — the pre-fix
		// behaviour. The merge must NOT cleanly converge: the two rows collide on
		// uk_dep_issue_target.
		insA := "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) " +
			"VALUES (UUID(), '" + x + "', '" + y + "', 'blocks', NOW(), 'a')"
		insB := "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) " +
			"VALUES (UUID(), '" + x + "', '" + y + "', 'blocks', NOW(), 'b')"
		mergeErr, rows := runTwoBranchEdgeMerge(t, ctx, store, x, y, insA, insB)
		if mergeErr == nil && rows == 1 {
			t.Error("expected divergent random ids to break the merge (uk_dep_issue_target collision), but it converged cleanly")
		}
	})
}

// TestInsertBackfillIDPathsAgree pins the invariant that the production insert
// path (AddDependency) and the backfill path (rekeyDependencyIDs / migration
// 0050) compute the same dependency primary key from the same edge.
//
// Insert path:  depid.New(dep.IssueID, dep.DependsOnID); dep.DependsOnID is
//
//	stored in depends_on_issue_id.
//
// Backfill path: SELECT issue_id, COALESCE(depends_on_issue_id,
//
//	depends_on_wisp_id, depends_on_external) FROM dependencies;
//	then depid.New(issue_id, coalesced_target).
//
// The two paths agree because AddDependency writes DependsOnID to
// depends_on_issue_id and the backfill reads that column first via COALESCE.
// If AddDependency ever begins encoding the target differently, or the
// backfill's COALESCE column order changes, the stored id diverges from what
// the backfill would derive — and two independently-upgraded clones would fork
// again (#4259). This test creates a real edge via AddDependency, reads back
// the exact column values the backfill would use, recomputes depid.New over
// them, and asserts they match.
func TestInsertBackfillIDPathsAgree(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	db := store.db
	for _, id := range []string{"ibp-src", "ibp-dst"} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, '', '', '', '', 'open', 2, 'task')",
			id, id); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "ibp-src", DependsOnID: "ibp-dst", Type: types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// What the insert path stored.
	var insertID string
	if err := db.QueryRowContext(ctx,
		"SELECT id FROM dependencies WHERE issue_id = 'ibp-src' AND depends_on_issue_id = 'ibp-dst'",
	).Scan(&insertID); err != nil {
		t.Fatalf("read insert id: %v", err)
	}

	// What the backfill would compute from the stored column values:
	// rekeyDependencyTable selects (issue_id, COALESCE(depends_on_issue_id,
	// depends_on_wisp_id, depends_on_external)) then calls depid.New.
	var issueID, coalesceTarget string
	if err := db.QueryRowContext(ctx,
		"SELECT issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) "+
			"FROM dependencies WHERE issue_id = 'ibp-src'",
	).Scan(&issueID, &coalesceTarget); err != nil {
		t.Fatalf("read backfill inputs: %v", err)
	}
	backfillID := depid.New(issueID, coalesceTarget)

	if insertID != backfillID {
		t.Errorf("insert path stored id %q but backfill would derive %q from "+
			"(issue_id=%q, coalesce_target=%q): the two paths have diverged — "+
			"independently-upgraded clones would produce different ids and bd dolt pull would fork (#4259)",
			insertID, backfillID, issueID, coalesceTarget)
	}
}

// runTwoBranchEdgeMerge seeds issues x and y on the shared ancestor, forks a peer
// branch from it, runs insA on the current branch and insB on the peer branch
// (each committed), then merges the peer back into the current branch. It returns
// the merge error (if any) and the resulting count of dependency rows for the
// edge x -> y on the current branch.
func runTwoBranchEdgeMerge(t *testing.T, ctx context.Context, store *DoltStore, x, y, insA, insB string) (error, int) {
	t.Helper()
	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	for _, id := range []string{x, y} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, ?, '', '', '', '', 'open', 2, 'task')",
			id, id); err != nil {
			t.Fatalf("seed issue %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issues')"); err != nil {
		t.Fatalf("commit seed issues: %v", err)
	}

	peerBranch := currentBranch + "_peer"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})

	// Current branch creates its edge.
	if _, err := db.ExecContext(ctx, insA); err != nil {
		t.Fatalf("insert edge on %s: %v", currentBranch, err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'edge on current')"); err != nil {
		t.Fatalf("commit edge on %s: %v", currentBranch, err)
	}

	// Peer branch independently creates the same logical edge.
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer branch: %v", err)
	}
	if _, err := db.ExecContext(ctx, insB); err != nil {
		t.Fatalf("insert edge on peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'edge on peer')"); err != nil {
		t.Fatalf("commit edge on peer: %v", err)
	}

	// Merge peer back into the current branch.
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current branch: %v", err)
	}
	_, mergeErr := db.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch)

	var rows int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?", x, y).Scan(&rows); err != nil {
		// A failed/aborted merge can leave the session mid-transaction; the row
		// count is only meaningful for the clean path, so tolerate a read error
		// here and let the caller judge on mergeErr.
		return mergeErr, -1
	}
	return mergeErr, rows
}
