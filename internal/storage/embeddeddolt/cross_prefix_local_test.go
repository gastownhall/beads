//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestEmbeddedCrossPrefixLocalBlocksReadiness(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "b")
	ctx := t.Context()
	blocker := &types.Issue{ID: "b-open-blocker", Title: "Local blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	dependent := &types.Issue{ID: "a-queued-task", Title: "Dependent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	for _, issue := range []*types.Issue{blocker, dependent} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{
		IssueID: dependent.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("add cross-prefix local blocker: %v", err)
	}
	var local, external sql.NullString
	te.queryScalar(t, ctx, "SELECT depends_on_issue_id, depends_on_external FROM dependencies WHERE issue_id = ?", []any{dependent.ID}, &local, &external)
	if !local.Valid || local.String != blocker.ID || external.Valid {
		t.Fatalf("local target routed incorrectly: issue=%v external=%v", local, external)
	}
	ready, err := te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range ready {
		if item.ID == dependent.ID {
			t.Fatal("dependent is ready while cross-prefix blocker open")
		}
	}
	blocked, err := te.store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	explained := false
	for _, item := range blocked {
		if item.ID != dependent.ID {
			continue
		}
		for _, id := range item.BlockedBy {
			if id == blocker.ID {
				explained = true
			}
		}
	}
	if !explained {
		t.Fatal("blocked reason omitted local cross-prefix blocker")
	}
	if err := te.store.CloseIssue(ctx, blocker.ID, "done", "tester", ""); err != nil {
		t.Fatal(err)
	}
	ready, err = te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range ready {
		if item.ID == dependent.ID {
			return
		}
	}
	t.Fatal("closing blocker did not unblock dependent")
}

func TestEmbeddedCrossPrefixBatchCreate(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "b")
	ctx := t.Context()
	if err := te.store.SetConfig(ctx, "allowed_prefixes", "a,b"); err != nil {
		t.Fatal(err)
	}
	blocker := &types.Issue{ID: "b-batch-blocker", Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	dep := &types.Issue{
		ID: "a-batch-dependent", Title: "Dependent", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{
			{IssueID: "a-batch-dependent", DependsOnID: blocker.ID, Type: types.DepBlocks},
			{IssueID: "a-batch-dependent", DependsOnID: "foreign-missing", Type: types.DepRelated},
		},
	}
	if err := te.store.CreateIssues(ctx, []*types.Issue{blocker, dep}, "tester"); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	var local int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND type = 'blocks'",
		[]any{dep.ID, blocker.ID}, &local)
	var external int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_external = ? AND type = 'related'",
		[]any{dep.ID, "foreign-missing"}, &external)
	if local != 1 || external != 1 {
		t.Fatalf("batch target classification: local=%d, external=%d", local, external)
	}
	ready, err := te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range ready {
		if issue.ID == dep.ID {
			t.Fatal("batch dependent is ready while its blocker is open")
		}
	}
	if err := te.store.CloseIssue(ctx, blocker.ID, "done", "tester", ""); err != nil {
		t.Fatal(err)
	}
	ready, err = te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range ready {
		if issue.ID == dep.ID {
			return
		}
	}
	t.Fatal("batch dependent not ready after close")
}
