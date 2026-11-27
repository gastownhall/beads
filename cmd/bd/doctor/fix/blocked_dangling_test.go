//go:build cgo

package fix

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bd-muhnf0, the `bd doctor --fix` entry point: on a clone whose
// dependencies name a wisp this database does not have, the blocked-state
// repair must leave the dependent's is_blocked exactly as found — nothing a
// recompute here could do would be right — report nothing to fix, and land
// no repair commit over a database it did not correct.
func TestRepairBlockedState_DanglingWispEdgeLeavesFlagAsFound(t *testing.T) {
	dir := t.TempDir()
	store := newFixTestStore(t, dir, "bd")
	ctx := context.Background()

	wisp := &types.Issue{ID: "dd-w", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	issue := &types.Issue{ID: "dd-a", Title: "issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "dd-a", DependsOnID: "dd-w", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	// Open the same database the fix itself opens, and simulate the clone.
	db, _, err := openDoltDB(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatalf("openDoltDB: %v", err)
	}
	defer db.Close()

	var blocked bool
	if err := db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = 'dd-a'").Scan(&blocked); err != nil {
		t.Fatalf("read pre-state: %v", err)
	}
	if !blocked {
		t.Fatal("fixture: dd-a should be blocked while the wisp is present")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM wisps WHERE id = 'dd-w'"); err != nil {
		t.Fatalf("drop wisp row: %v", err)
	}
	// The repair guards on a clean issues/dependencies working set.
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed dangling edge')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	var commitsBefore int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&commitsBefore); err != nil {
		t.Fatalf("count commits before: %v", err)
	}

	if err := repairBlockedState(ctx, db); err != nil {
		t.Fatalf("repairBlockedState: %v", err)
	}

	if err := db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = 'dd-a'").Scan(&blocked); err != nil {
		t.Fatalf("read post-state: %v", err)
	}
	if !blocked {
		t.Error("doctor fix cleared is_blocked on a row whose blocker wisp is absent from this database; want left as found")
	}
	var commitsAfter int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&commitsAfter); err != nil {
		t.Fatalf("count commits after: %v", err)
	}
	if commitsAfter != commitsBefore {
		t.Errorf("doctor fix landed %d commit(s) over a database whose only defect is a dangling wisp edge", commitsAfter-commitsBefore)
	}
}
