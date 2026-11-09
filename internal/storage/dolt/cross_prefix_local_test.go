package dolt

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
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

// Transaction addition must not fall back to "foreign prefix means external":
// the cross-prefix grandparent already exists here, and blocking a descendant
// on its own ancestor must be refused by the transaction write path.
func TestCrossPrefixTransactionRejectsHierarchy(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	for _, id := range []string{"a-tx-child", "a-tx-parent", "b-tx-grand"} {
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, pair := range [][2]string{{"a-tx-child", "a-tx-parent"}, {"a-tx-parent", "b-tx-grand"}} {
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: pair[0], DependsOnID: pair[1], Type: types.DepParentChild,
		}, "tester"); err != nil {
			t.Fatalf("seed ancestor %s -> %s: %v", pair[0], pair[1], err)
		}
	}
	err := store.RunInTransaction(ctx, "cross-prefix transaction hierarchy check", func(tx storage.Transaction) error {
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID: "a-tx-child", DependsOnID: "b-tx-grand", Type: types.DepBlocks,
		}, "tester")
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be blocked by its ancestor") {
		t.Fatalf("transaction cross-prefix ancestor blocker = %v, want hierarchy conflict", err)
	}
	var n int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND (depends_on_issue_id = ? OR depends_on_external = ?)",
		"a-tx-child", "b-tx-grand", "b-tx-grand").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("refused transaction left %d ancestor blocking edges", n)
	}
}

// A row stored by the old prefix-only classifier must not be a permanent
// no-op on re-add. The original row id survives, but its target column and
// derived blocked state must now match the locally-present target.
func TestCrossPrefixLegacyExternalReAddMigrates(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	for _, id := range []string{"a-legacy-dependent", "b-legacy-blocker"} {
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO dependencies (id, issue_id, depends_on_external, type, created_at, created_by, metadata) VALUES (?, ?, ?, 'blocks', UTC_TIMESTAMP(), 'tester', '{}')",
		"legacy-edge-8300", "a-legacy-dependent", "b-legacy-blocker"); err != nil {
		t.Fatalf("seed legacy external edge: %v", err)
	}
	dep := &types.Dependency{IssueID: "a-legacy-dependent", DependsOnID: "b-legacy-blocker", Type: types.DepBlocks}
	for i := 0; i < 2; i++ {
		if err := store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("re-add legacy edge %d: %v", i+1, err)
		}
	}
	var id string
	var local, external sql.NullString
	if err := store.db.QueryRowContext(ctx,
		"SELECT id, depends_on_issue_id, depends_on_external FROM dependencies WHERE issue_id = ?",
		dep.IssueID).Scan(&id, &local, &external); err != nil {
		t.Fatal(err)
	}
	if id != "legacy-edge-8300" || !local.Valid || local.String != dep.DependsOnID || external.Valid {
		t.Fatalf("legacy edge migration: id=%s local=%v external=%v", id, local, external)
	}
	var blocked bool
	if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", dep.IssueID).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatal("legacy external-column re-add failed to recompute blocked state")
	}
}

// A cross-prefix wisp is local even when its ID doesn't look like a wisp,
// and an explicit external: reference remains external despite any prefix.
func TestCrossPrefixWispAndExplicitExternalTargets(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	for _, issue := range []*types.Issue{
		{ID: "a-wisp-dependent", Title: "Dependent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "b-wisp-target", Title: "Wisp target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "a-wisp-dependent", DependsOnID: "b-wisp-target", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("add cross-prefix local wisp: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "a-wisp-dependent", DependsOnID: "external:GH-8300", Type: types.DepRelated,
	}, "tester"); err != nil {
		t.Fatalf("add explicit external: %v", err)
	}
	var wisp, external sql.NullString
	if err := store.db.QueryRowContext(ctx,
		"SELECT depends_on_wisp_id, depends_on_external FROM dependencies WHERE issue_id = ? AND type = 'blocks'",
		"a-wisp-dependent").Scan(&wisp, &external); err != nil {
		t.Fatal(err)
	}
	if !wisp.Valid || wisp.String != "b-wisp-target" || external.Valid {
		t.Fatalf("cross-prefix wisp routing: wisp=%v external=%v", wisp, external)
	}
	var n int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_external = ? AND type = 'related'",
		"a-wisp-dependent", "external:GH-8300").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("explicit external target stored as external count=%d, want 1", n)
	}
}
