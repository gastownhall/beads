package conformance

import (
	"context"
	"fmt"
	"sort"
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
// reverse nonblocking dependency commits its concrete dependency row without
// sweeping an unrelated dirty durable row into the same commit.
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
	if err := fixture.Exec(ctx, "UPDATE issues SET title = ? WHERE id = ?", "working title", dirty.ID); err != nil {
		t.Fatalf("dirty unrelated issue: %v", err)
	}
	assertIssueOperationsScalar(t, ctx, fixture, "working dirty title", "working title",
		"SELECT title FROM issues WHERE id = ?", []any{dirty.ID})
	assertIssueOperationsScalar(t, ctx, fixture, "committed dirty title before create", "committed title",
		"SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", []any{dirty.ID})

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
	assertIssueOperationsScalar(t, ctx, fixture, "committed dirty title after create", "committed title",
		"SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", []any{dirty.ID})
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

// ReopenCommentCommitFixture supplies the routes for
// RunReopenCommentIsCommittedWithTheReopen. Each route reopens id with reason
// as actor through one of the backend's commit paths and returns once that
// reopen is published; a route that defers the version commit makes its own
// explicit commit before returning.
type ReopenCommentCommitFixture struct {
	IssuePrefix string
	CreateIssue func(context.Context, *types.Issue, string) error
	CloseIssue  func(ctx context.Context, id, reason, actor, session string) error
	Commit      func(context.Context, string) error
	QueryScalar func(context.Context, string, []any, ...any) error
	Routes      map[string]func(ctx context.Context, id, reason, actor string) error
}

// RunReopenCommentIsCommittedWithTheReopen proves that the comment a reasoned
// reopen writes (GH#5593) is published with the reopen itself: HEAD shows the
// issue open together with its reason comment, and no comments change is left
// in the working set for a later, unrelated commit to sweep up. A route that
// stages only the tables it knows it changed fails here when it forgets the
// comment table, even though the row was written.
func RunReopenCommentIsCommittedWithTheReopen(t *testing.T, ctx context.Context, fixture ReopenCommentCommitFixture) {
	t.Helper()
	names := make([]string, 0, len(fixture.Routes))
	for name := range fixture.Routes {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		reopen := fixture.Routes[name]
		t.Run(name, func(t *testing.T) {
			id := fmt.Sprintf("%s-reopen-commit-%d", fixture.IssuePrefix, i)
			reason := "reopened through " + name
			issue := &types.Issue{ID: id, Title: "reopen commit " + name, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
				t.Fatalf("create %s: %v", id, err)
			}
			if err := fixture.CloseIssue(ctx, id, "done", "closer", ""); err != nil {
				t.Fatalf("close %s: %v", id, err)
			}
			if err := fixture.Commit(ctx, "seed closed issue"); err != nil {
				t.Fatalf("commit seed: %v", err)
			}

			if err := reopen(ctx, id, reason, "reviver"); err != nil {
				t.Fatalf("reopen %s: %v", id, err)
			}

			var status string
			var comments int
			if err := fixture.QueryScalar(ctx, `SELECT
				(SELECT status FROM issues AS OF 'HEAD' WHERE id = ?),
				(SELECT COUNT(*) FROM comments AS OF 'HEAD' WHERE issue_id = ? AND author = ? AND text = ?)`,
				[]any{id, id, "reviver", reason}, &status, &comments); err != nil {
				t.Fatalf("read %s AS OF HEAD: %v", id, err)
			}
			if status != string(types.StatusOpen) || comments != 1 {
				t.Errorf("HEAD after reopen: status = %q with %d reason comments, want open with 1", status, comments)
			}
			var pending int
			if err := fixture.QueryScalar(ctx,
				"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'comments'", nil, &pending); err != nil {
				t.Fatalf("read dolt_status: %v", err)
			}
			if pending != 0 {
				t.Errorf("reopen left the comments table changed in the working set (%d dolt_status rows), want it committed", pending)
			}
		})
	}
}
