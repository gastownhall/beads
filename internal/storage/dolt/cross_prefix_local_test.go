package dolt

import (
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestCrossPrefixLocalBlockerReadiness(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, id := range []string{"b-local-blocker", "a-local-dependent"} {
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	dep := &types.Dependency{IssueID: "a-local-dependent", DependsOnID: "b-local-blocker", Type: types.DepBlocks}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("add cross-prefix blocker: %v", err)
	}

	var local, external sql.NullString
	if err := store.db.QueryRowContext(ctx,
		"SELECT depends_on_issue_id, depends_on_external FROM dependencies WHERE issue_id = ?",
		dep.IssueID).Scan(&local, &external); err != nil {
		t.Fatal(err)
	}
	if !local.Valid || local.String != dep.DependsOnID || external.Valid {
		t.Fatalf("target columns: local=%v external=%v, want local issue FK", local, external)
	}

	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range ready {
		if issue.ID == dep.IssueID {
			t.Fatal("open local cross-prefix blocker allowed dependent into ready")
		}
	}
	blocked, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	explained := false
	for _, issue := range blocked {
		if issue.ID == dep.IssueID {
			for _, id := range issue.BlockedBy {
				if id == dep.DependsOnID {
					explained = true
				}
			}
		}
	}
	if !explained {
		t.Fatal("blocked query did not explain the local cross-prefix blocker")
	}

	if err := store.CloseIssue(ctx, dep.DependsOnID, "done", "tester", ""); err != nil {
		t.Fatalf("close blocker: %v", err)
	}
	ready, err = store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range ready {
		if issue.ID == dep.IssueID {
			return
		}
	}
	t.Fatal("closing cross-prefix blocker did not unblock its dependent")
}

func TestCrossPrefixBatchCreateRoutesLocalAndForeignTargets(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	if err := store.SetConfig(ctx, "allowed_prefixes", "a,b"); err != nil {
		t.Fatal(err)
	}

	blocker := &types.Issue{ID: "b-batch-blocker", Title: "Batch blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	dependent := &types.Issue{
		ID: "a-batch-dependent", Title: "Batch dependent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{
			{IssueID: "a-batch-dependent", DependsOnID: "b-batch-blocker", Type: types.DepBlocks},
			{IssueID: "a-batch-dependent", DependsOnID: "foreign-nonexistent", Type: types.DepRelated},
		},
	}
	if err := store.CreateIssues(ctx, []*types.Issue{blocker, dependent}, "tester"); err != nil {
		t.Fatalf("batch create: %v", err)
	}
	var local, foreign int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND type = 'blocks'",
		dependent.ID, blocker.ID).Scan(&local); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_external = ? AND type = 'related'",
		dependent.ID, "foreign-nonexistent").Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if local != 1 || foreign != 1 {
		t.Fatalf("batch routing: local=%d foreign=%d, want 1 each", local, foreign)
	}
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range ready {
		if issue.ID == dependent.ID {
			t.Fatal("batch cross-prefix blocked issue is ready")
		}
	}
	if err := store.CloseIssue(ctx, blocker.ID, "done", "tester", ""); err != nil {
		t.Fatal(err)
	}
	ready, err = store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range ready {
		if issue.ID == dependent.ID {
			return
		}
	}
	t.Fatal("batch dependent should be ready after local blocker closes")
}
