package conformance

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// IssueOperationsStagingFixture supplies adapter-specific storage access for
// the issue-operations assertions. Each Run function documents which fields it
// needs; an adapter supplies only those.
type IssueOperationsStagingFixture struct {
	IssuePrefix   string
	Operations    publicops.Lifecycle
	CreateIssue   func(context.Context, *types.Issue, string) error
	AddDependency func(context.Context, *types.Dependency, string) error
	GetReadyWork  func(context.Context, types.WorkFilter) ([]*types.Issue, error)
	SetConfig     func(context.Context, string, string) error
	Commit        func(context.Context, string) error
	Exec          func(context.Context, string, ...any) error
	QueryScalar   func(context.Context, string, []any, ...any) error
	// UpdateRaw drives the backend's generic update funnel with an untyped
	// column map, the way an external-sync or backfill caller does. The typed
	// patch behind Operations.Update carries no closed_at, so this is the only
	// route to the columns the close-lifecycle assertions cover.
	UpdateRaw func(context.Context, string, map[string]any, string) error
}

// RunIssueOperationsCreateReverseNonBlockingStagesConcreteTables proves that a
// reverse nonblocking dependency commits its concrete dependency row, and that
// the create's commit takes other writers' pending rows only whole.
//
// An unrelated issue's pending title and pending label may ride the commit or
// stay pending, but they go together. A backend whose writers share one
// working set stages every issue table with each write that stages one (the
// Dolt server store's issue closure), so that no commit holds rows naming an
// issue it does not hold; such a create commits both. A backend that stages
// only the tables a write changed leaves both pending. A pending row outside
// the issue tables (metadata) stays uncommitted either way: no create commits
// the whole working set (GH#2455).
func RunIssueOperationsCreateReverseNonBlockingStagesConcreteTables(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	source := &types.Issue{
		ID:        fixture.IssuePrefix + "-create-relates-source",
		Title:     "source",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	dirty := &types.Issue{
		ID:        fixture.IssuePrefix + "-create-relates-dirty",
		Title:     "committed title",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	for _, issue := range []*types.Issue{source, dirty} {
		if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed issue %s: %v", issue.ID, err)
		}
	}
	if err := fixture.Commit(ctx, "seed selective create staging"); err != nil {
		t.Fatalf("commit seed state: %v", err)
	}
	const pendingLabel = "working-label"
	pendingKey := fixture.IssuePrefix + ".create-relates-pending"
	if err := fixture.Exec(ctx, "UPDATE issues SET title = ? WHERE id = ?", "working title", dirty.ID); err != nil {
		t.Fatalf("dirty unrelated issue: %v", err)
	}
	if err := fixture.Exec(ctx, "INSERT INTO labels (issue_id, label) VALUES (?, ?)", dirty.ID, pendingLabel); err != nil {
		t.Fatalf("label unrelated issue: %v", err)
	}
	if err := fixture.Exec(ctx, "INSERT INTO metadata (`key`, value) VALUES (?, ?)", pendingKey, "pending"); err != nil {
		t.Fatalf("pending metadata row: %v", err)
	}
	assertIssueOperationsScalar(t, ctx, fixture, "working dirty title", "working title",
		"SELECT title FROM issues WHERE id = ?", []any{dirty.ID})
	assertIssueOperationsScalar(t, ctx, fixture, "committed dirty title before create", "committed title",
		"SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", []any{dirty.ID})
	assertIssueOperationsScalar(t, ctx, fixture, "committed dirty label before create", 0,
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ? AND label = ?", []any{dirty.ID, pendingLabel})

	created, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			Title:     "generated wisp",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		},
		Dependencies: []publicops.CreateDependency{{
			TargetID: source.ID,
			Type:     types.DepRelatesTo,
			Reverse:  true,
		}},
	})
	if err != nil {
		t.Fatalf("create generated wisp: %v", err)
	}
	if created.Issue == nil || created.Issue.ID == "" {
		t.Fatalf("created issue = %#v, want generated wisp ID", created.Issue)
	}

	assertIssueOperationsScalar(t, ctx, fixture, "committed reverse dependency", 1,
		"SELECT COUNT(*) FROM dependencies AS OF 'HEAD' WHERE issue_id = ? AND depends_on_wisp_id = ? AND type = ?",
		[]any{source.ID, created.Issue.ID, types.DepRelatesTo})
	assertIssueOperationsScalar(t, ctx, fixture, "working source blocked state", false,
		"SELECT is_blocked FROM issues WHERE id = ?", []any{source.ID})
	assertIssueOperationsScalar(t, ctx, fixture, "committed source blocked state", false,
		"SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", []any{source.ID})
	assertIssueOperationsScalar(t, ctx, fixture, "working dirty title after create", "working title",
		"SELECT title FROM issues WHERE id = ?", []any{dirty.ID})
	assertIssueOperationsScalar(t, ctx, fixture, "working dirty label after create", 1,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", []any{dirty.ID, pendingLabel})
	var committedTitle string
	if err := fixture.QueryScalar(ctx, "SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", []any{dirty.ID}, &committedTitle); err != nil {
		t.Fatalf("committed dirty title after create: %v", err)
	}
	var committedLabels int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ? AND label = ?",
		[]any{dirty.ID, pendingLabel}, &committedLabels); err != nil {
		t.Fatalf("committed dirty label after create: %v", err)
	}
	switch {
	case committedTitle == "committed title" && committedLabels == 0:
		// The create staged only the tables it changed.
	case committedTitle == "working title" && committedLabels == 1:
		// The create staged every issue table, and both pending rows rode it.
	default:
		t.Fatalf("committed dirty issue after create = title %q and %d %q labels; want both pending rows committed or neither",
			committedTitle, committedLabels, pendingLabel)
	}
	assertIssueOperationsScalar(t, ctx, fixture, "committed pending metadata after create", 0,
		"SELECT COUNT(*) FROM metadata AS OF 'HEAD' WHERE `key` = ?", []any{pendingKey})
	assertIssueOperationsScalar(t, ctx, fixture, "working pending metadata after create", 1,
		"SELECT COUNT(*) FROM metadata WHERE `key` = ?", []any{pendingKey})
}

// RunIssueOperationsCreateParentChildRecomputesWaitsForClosure proves that
// creating an open child updates an existing waiter on the child's spawner in
// both the working set and committed readiness state.
func RunIssueOperationsCreateParentChildRecomputesWaitsForClosure(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture) {
	t.Helper()

	spawner := &types.Issue{
		ID:        fixture.IssuePrefix + "-create-spawner",
		Title:     "spawner",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	waiter := &types.Issue{
		ID:        fixture.IssuePrefix + "-create-waiter",
		Title:     "waiter",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	for _, issue := range []*types.Issue{spawner, waiter} {
		if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed issue %s: %v", issue.ID, err)
		}
	}
	if err := fixture.AddDependency(ctx, &types.Dependency{
		IssueID:     waiter.ID,
		DependsOnID: spawner.ID,
		Type:        types.DepWaitsFor,
	}, "seed"); err != nil {
		t.Fatalf("seed waits-for dependency: %v", err)
	}
	if err := fixture.Commit(ctx, "seed waits-for create closure"); err != nil {
		t.Fatalf("commit seed state: %v", err)
	}
	assertIssueOperationsScalar(t, ctx, fixture, "working waiter before child create", false,
		"SELECT is_blocked FROM issues WHERE id = ?", []any{waiter.ID})
	assertIssueOperationsScalar(t, ctx, fixture, "committed waiter before child create", false,
		"SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", []any{waiter.ID})
	assertIssueOperationsReady(t, ctx, fixture, waiter.ID, true)

	created, err := fixture.Operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue: &types.Issue{
			Title:     "open child",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		},
		ParentID: spawner.ID,
	})
	if err != nil {
		t.Fatalf("create open child: %v", err)
	}
	if created.Issue == nil || created.Issue.ID != spawner.ID+".1" {
		t.Fatalf("created issue = %#v, want child %s.1", created.Issue, spawner.ID)
	}

	assertIssueOperationsScalar(t, ctx, fixture, "committed parent-child dependency", 1,
		"SELECT COUNT(*) FROM dependencies AS OF 'HEAD' WHERE issue_id = ? AND depends_on_issue_id = ? AND type = ?",
		[]any{created.Issue.ID, spawner.ID, types.DepParentChild})
	assertIssueOperationsScalar(t, ctx, fixture, "working waiter after child create", true,
		"SELECT is_blocked FROM issues WHERE id = ?", []any{waiter.ID})
	assertIssueOperationsScalar(t, ctx, fixture, "committed waiter after child create", true,
		"SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", []any{waiter.ID})
	assertIssueOperationsReady(t, ctx, fixture, waiter.ID, false)
}

func assertIssueOperationsScalar[T comparable](t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, name string, want T, query string, args []any) {
	t.Helper()
	var got T
	if err := fixture.QueryScalar(ctx, query, args, &got); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func assertIssueOperationsReady(t *testing.T, ctx context.Context, fixture IssueOperationsStagingFixture, issueID string, want bool) {
	t.Helper()
	ready, err := fixture.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("get ready work: %v", err)
	}
	for _, issue := range ready {
		if issue != nil && issue.ID == issueID {
			if !want {
				t.Fatalf("ready work contains %s, want absent", issueID)
			}
			return
		}
	}
	if want {
		t.Fatalf("ready work omits %s, want present", issueID)
	}
}
