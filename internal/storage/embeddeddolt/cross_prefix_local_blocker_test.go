//go:build cgo

package embeddeddolt_test

import (
	"context"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedCrossPrefixLocalBlocksReadiness: a blocks edge whose target has
// another prefix but lives in this same database must block readiness
// (GH#8299). The prefix mismatch alone must not route it to
// depends_on_external, where readiness never sees it.
func TestEmbeddedCrossPrefixLocalBlocksReadiness(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const dependent, blocker = "a-x1", "b-y1"
	for _, id := range []string{dependent, blocker} {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: dependent, DependsOnID: blocker, Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", dependent, blocker, "depends_on_issue_id")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", dependent, 1)
	if crossPrefixReady(t, ctx, te, dependent) {
		t.Fatalf("%s is ready while its local blocker %s is open", dependent, blocker)
	}
	blocked, err := te.store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	explained := false
	for _, issue := range blocked {
		if issue.ID == dependent {
			explained = slices.Contains(issue.BlockedBy, blocker)
			if !explained {
				t.Errorf("%s blocked_by = %v, want it to name %s", dependent, issue.BlockedBy, blocker)
			}
		}
	}
	if !explained {
		t.Fatalf("GetBlockedIssues does not explain %s as blocked by %s", dependent, blocker)
	}

	if err := te.store.CloseIssue(ctx, blocker, "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue %s: %v", blocker, err)
	}
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", dependent, 0)
	if !crossPrefixReady(t, ctx, te, dependent) {
		t.Fatalf("%s is not ready after its only blocker %s closed", dependent, blocker)
	}
}

// assertCrossPrefixEdgeColumn checks that the edge from source to target holds
// its target in column and in no other typed target column.
func assertCrossPrefixEdgeColumn(t *testing.T, ctx context.Context, te *testEnv, table, source, target, column string) {
	t.Helper()
	var issueCol, wispCol, externalCol *string
	te.queryScalar(t, ctx,
		"SELECT depends_on_issue_id, depends_on_wisp_id, depends_on_external FROM "+table+
			" WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		[]any{source, target}, &issueCol, &wispCol, &externalCol)
	got := map[string]*string{"depends_on_issue_id": issueCol, "depends_on_wisp_id": wispCol, "depends_on_external": externalCol}
	for col, value := range got {
		switch {
		case col == column && (value == nil || *value != target):
			t.Errorf("%s edge %s -> %s: %s = %v, want %s", table, source, target, col, value, target)
		case col != column && value != nil:
			t.Errorf("%s edge %s -> %s: %s = %q, want NULL (target belongs in %s)", table, source, target, col, *value, column)
		}
	}
}

func assertCrossPrefixIsBlocked(t *testing.T, ctx context.Context, te *testEnv, table, id string, want int) {
	t.Helper()
	var got int
	te.queryScalar(t, ctx, "SELECT is_blocked FROM "+table+" WHERE id = ?", []any{id}, &got)
	if got != want {
		t.Errorf("%s.is_blocked for %s = %d, want %d", table, id, got, want)
	}
}

func crossPrefixReady(t *testing.T, ctx context.Context, te *testEnv, id string) bool {
	t.Helper()
	ready, err := te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	return slices.ContainsFunc(ready, func(issue *types.Issue) bool { return issue.ID == id })
}

// TestEmbeddedCrossPrefixWispTargetBlocksLikeASamePrefixWisp: an active wisp of
// another prefix is a local wisp target (GH#8299). From an issue source and
// from a wisp source the edge files it in depends_on_wisp_id, and the source's
// blocked state follows it exactly as it follows a same-prefix wisp.
func TestEmbeddedCrossPrefixWispTargetBlocksLikeASamePrefixWisp(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const crossWisp, sameWisp = "b-xw1", "a-sw1"
	type pair struct {
		source, table, sourceTable, target string
	}
	pairs := []pair{
		{"a-xi1", "dependencies", "issues", crossWisp},
		{"a-xws1", "wisp_dependencies", "wisps", crossWisp},
		{"a-si1", "dependencies", "issues", sameWisp},
		{"a-sws1", "wisp_dependencies", "wisps", sameWisp},
	}
	for _, seed := range []struct {
		id        string
		ephemeral bool
	}{
		{crossWisp, true}, {sameWisp, true},
		{"a-xi1", false}, {"a-xws1", true}, {"a-si1", false}, {"a-sws1", true},
	} {
		issue := &types.Issue{ID: seed.id, Title: seed.id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: seed.ephemeral}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", seed.id, err)
		}
	}
	for _, p := range pairs {
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: p.source, DependsOnID: p.target, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", p.source, p.target, err)
		}
		assertCrossPrefixEdgeColumn(t, ctx, te, p.table, p.source, p.target, "depends_on_wisp_id")
		assertCrossPrefixIsBlocked(t, ctx, te, p.sourceTable, p.source, 1)
	}

	for _, id := range []string{crossWisp, sameWisp} {
		if err := te.store.CloseIssue(ctx, id, "done", "tester", ""); err != nil {
			t.Fatalf("CloseIssue %s: %v", id, err)
		}
	}
	for _, p := range pairs {
		assertCrossPrefixIsBlocked(t, ctx, te, p.sourceTable, p.source, 0)
	}
}

// TestEmbeddedCrossPrefixBatchCreate: a batch create whose edges target local
// issues of another prefix — one created earlier, one created in the same
// batch — files both in depends_on_issue_id, so they gate readiness (GH#8299).
func TestEmbeddedCrossPrefixBatchCreate(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const existing, inBatch, source = "c-pre1", "b-new1", "a-bsrc1"
	// Batch create validates explicit ids against the configured prefixes.
	if err := te.store.SetConfig(ctx, "allowed_prefixes", "b,c"); err != nil {
		t.Fatalf("SetConfig allowed_prefixes: %v", err)
	}
	if err := te.store.CreateIssue(ctx, &types.Issue{ID: existing, Title: existing, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
		t.Fatalf("CreateIssue %s: %v", existing, err)
	}
	batch := []*types.Issue{
		{ID: inBatch, Title: inBatch, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: source, Title: source, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Dependencies: []*types.Dependency{
			{DependsOnID: existing, Type: types.DepBlocks},
			{DependsOnID: inBatch, Type: types.DepBlocks},
		}},
	}
	if err := te.store.CreateIssues(ctx, batch, "tester"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}

	for _, target := range []string{existing, inBatch} {
		assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", source, target, "depends_on_issue_id")
	}
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", source, 1)
	if crossPrefixReady(t, ctx, te, source) {
		t.Fatalf("%s is ready while its local blockers %s and %s are open", source, existing, inBatch)
	}
}

// TestEmbeddedCrossPrefixImportBatchTargetAfterSources: an import-shaped
// batch (SkipPrefixValidation, explicit foreign-prefix ids) whose sources
// block on a pre-existing cross-prefix target and on one created later in
// the same batch files both edges in depends_on_issue_id. The dependency pass
// runs after every row of the batch is written, so the batch's presence reads
// see the target even though it follows its source (GH#8299).
func TestEmbeddedCrossPrefixImportBatchTargetAfterSources(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: "b-pre", Title: "pre-existing cross-prefix target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue b-pre: %v", err)
	}
	batch := []*types.Issue{
		{ID: "a-isrc1", Title: "import source one", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{DependsOnID: "b-pre", Type: types.DepBlocks}}},
		{ID: "a-isrc2", Title: "import source two", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{DependsOnID: "b-itgt", Type: types.DepBlocks}}},
		{ID: "b-itgt", Title: "cross-prefix target created after its source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	if err := te.store.CreateIssuesWithFullOptions(ctx, batch, "tester", storage.BatchCreateOptions{
		SkipPrefixValidation: true,
	}); err != nil {
		t.Fatalf("CreateIssuesWithFullOptions: %v", err)
	}

	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", "a-isrc1", "b-pre", "depends_on_issue_id")
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", "a-isrc2", "b-itgt", "depends_on_issue_id")
}
