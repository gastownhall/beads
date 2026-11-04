//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// seedDeferredParentPlane puts a future-deferred parent, a child hanging off it
// by a parent-child edge, and one unrelated free issue into the store. Ready
// work must return the free issue and withhold the child: its parent is not
// workable yet, so neither is it.
func seedDeferredParentPlane(t *testing.T, te *testEnv, prefix string) {
	t.Helper()
	ctx := t.Context()
	deferUntil := time.Now().Add(24 * time.Hour).UTC()

	parent := &types.Issue{
		ID:         prefix + "-parent",
		Title:      "deferred parent",
		Status:     types.StatusOpen,
		Priority:   2,
		IssueType:  types.TypeTask,
		DeferUntil: &deferUntil,
	}
	if err := te.store.CreateIssue(ctx, parent, "tester"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}
	for _, id := range []string{prefix + "-child", prefix + "-free"} {
		issue := &types.Issue{
			ID:        id,
			Title:     id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	dep := &types.Dependency{
		IssueID:     prefix + "-child",
		DependsOnID: prefix + "-parent",
		Type:        types.DepParentChild,
	}
	if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
}

// readyWorkIDs drives the domain/db repository straight over an embedded
// connection. *sql.DB satisfies db.Runner (domain/db/runner.go:8-12), so the
// repository can be built without the uow stack -- which is what makes this
// end-to-end test possible at all.
func readyWorkIDs(t *testing.T, te *testEnv) ([]string, error) {
	t.Helper()
	ctx := context.Background()
	raw, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()

	page, err := db.NewIssueSQLRepository(raw).GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(page.Items))
	for _, issue := range page.Items {
		ids = append(ids, issue.ID)
	}
	return ids, nil
}

// TestReadyWorkWithMissingDeferredParentTables is the end-to-end twin of the
// sqlmock guards on domain/db's deferred-parent walk, and the one test on this
// PR that separates base from fix against a real database.
//
// The walk joins a dependency table to an issue table over four pairs, and
// three of the four name a table every beads database must have. The gate
// tolerated any table-not-exist error and continued, so renaming `dependencies`
// away, holding the other three tables present, swallows edges 1 and 2 while
// edges 3 and 4 answer. Ready work came back with a nil error and the child of
// a deferred parent in it, because the walk that would have excluded the child
// returned an incomplete set.
//
// `issues` gets no case here on purpose. The walk swallows it the same way, but
// the ready-work union reads `issues` downstream and fails there regardless, so
// renaming it away cannot separate base from fix at this surface. See the PR's
// Limitations.
func TestReadyWorkWithMissingDeferredParentTables(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("missing_dependencies_is_an_error", func(t *testing.T) {
		te := newTestEnv(t, "rwd")
		seedDeferredParentPlane(t, te, "rwd")
		te.assertRowExists(t, t.Context(), "issues", "rwd-parent")
		te.exec(t, t.Context(), "RENAME TABLE dependencies TO dependencies_renamed_away")

		ids, err := readyWorkIDs(t, te)
		if err == nil {
			t.Fatalf("ready work answered over a database with no dependencies table, returning %v", ids)
		}
		// Not a substring check: "wisp_dependencies" contains "dependencies",
		// so a substring assertion cannot tell apart the two tables this test
		// exists to tell apart. IsMissingTable matches the whole name.
		if !dberrors.IsMissingTable(err, "dependencies") {
			t.Fatalf("error is not the missing-dependencies failure: %v", err)
		}
	})

	// The fixture control: a broken seed -- a child that was never actually
	// blocked by its parent -- passes the assertion above and fails here.
	t.Run("healthy_plane_withholds_deferred_child", func(t *testing.T) {
		te := newTestEnv(t, "rwh")
		seedDeferredParentPlane(t, te, "rwh")

		ids, err := readyWorkIDs(t, te)
		if err != nil {
			t.Fatalf("ready work over a healthy database: %v", err)
		}
		if len(ids) != 1 || ids[0] != "rwh-free" {
			t.Fatalf("ready work = %v, want [rwh-free] (the deferred parent's child must be withheld)", ids)
		}
	})

	// The over-tightening control, and the reason it renames rather than
	// leaving the database healthy: a healthy schema raises no table-not-exist
	// error at all, so the gate is never consulted and a "never tolerate"
	// narrowing would sail through. `wisps` is one of the two tables a database
	// may legitimately lack, so its absence must still leave the walk running
	// and the deferred child withheld. Narrow the tolerance too far and edge 2
	// (dependencies/wisps) errors out instead of being skipped, and this
	// reddens.
	t.Run("missing_wisps_still_withholds_deferred_child", func(t *testing.T) {
		te := newTestEnv(t, "rww")
		seedDeferredParentPlane(t, te, "rww")
		te.exec(t, t.Context(), "RENAME TABLE wisps TO wisps_renamed_away")

		ids, err := readyWorkIDs(t, te)
		if err != nil {
			t.Fatalf("ready work over a database with no wisps table: %v", err)
		}
		if len(ids) != 1 || ids[0] != "rww-free" {
			t.Fatalf("ready work = %v, want [rww-free] (the deferred parent's child must still be withheld)", ids)
		}
	})
}

// TestReadyWorkCustomStatusFailures distinguishes required-schema errors and
// already-canceled database calls from successful empty readiness responses.
func TestReadyWorkCustomStatusFailures(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cf")
	ctx := t.Context()
	if err := te.store.SetConfig(ctx, "status.custom", "review:active"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id       string
		status   types.Status
		priority int
	}{{"cf-review", "review", 0}, {"cf-open", types.StatusOpen, 1}} {
		if err := te.store.CreateIssue(ctx, &types.Issue{ID: row.id, Title: row.id, Status: row.status, Priority: row.priority, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	filter := types.WorkFilter{Status: types.StatusOpen, IncludeEphemeral: true, SortPolicy: types.SortPolicyPriority}
	assertReadyActiveRead(t, te, filter, []string{"cf-review", "cf-open"}, 2, false)
	operations := []struct {
		name   string
		capped bool
		read   func(*testing.T, context.Context, issueops.DBTX, types.WorkFilter) error
	}{
		{"counts", true, func(t *testing.T, ctx context.Context, tx issueops.DBTX, f types.WorkFilter) error {
			rows, err := issueops.GetReadyWorkWithCountsInTx(ctx, tx, f)
			if len(rows) != 0 {
				t.Fatalf("failed counts returned candidates: %v", readyIDs(rows))
			}
			return err
		}},
		{"page_total", true, func(t *testing.T, ctx context.Context, tx issueops.DBTX, f types.WorkFilter) error {
			rows, total, err := issueops.GetReadyWorkWithCountsAndTotalInTx(ctx, tx, f)
			if len(rows) != 0 || total != 0 {
				t.Fatalf("failed page returned candidates/total: %v/%d", readyIDs(rows), total)
			}
			return err
		}},
		{"counter", false, func(t *testing.T, ctx context.Context, tx issueops.DBTX, f types.WorkFilter) error {
			n, err := issueops.CountReadyWorkInTx(ctx, tx, f)
			if n != 0 {
				t.Fatalf("failed counter returned total %d", n)
			}
			return err
		}},
		{"domain_plain", true, func(t *testing.T, ctx context.Context, tx issueops.DBTX, f types.WorkFilter) error {
			page, err := db.NewIssueSQLRepository(tx).GetReadyWork(ctx, f)
			if len(page.Items) != 0 || page.HasMore {
				t.Fatal("failed domain plain returned a successful page")
			}
			return err
		}},
		{"domain_counts", true, func(t *testing.T, ctx context.Context, tx issueops.DBTX, f types.WorkFilter) error {
			page, err := db.NewIssueSQLRepository(tx).GetReadyWorkWithCounts(ctx, f)
			if len(page.Items) != 0 || page.HasMore {
				t.Fatal("failed domain counts returned a successful page")
			}
			return err
		}},
		{"claim", false, func(t *testing.T, ctx context.Context, tx issueops.DBTX, f types.WorkFilter) error {
			winner, err := issueops.ClaimReadyIssueInTx(ctx, tx, f, "alice")
			if winner != nil {
				t.Fatalf("failed claim returned winner %s", winner.ID)
			}
			return err
		}},
	}
	assertUnchanged := func(t *testing.T) {
		t.Helper()
		for _, row := range []struct{ id, status string }{{"cf-review", "review"}, {"cf-open", "open"}} {
			var status, assignee string
			var unstarted bool
			te.queryScalar(t, t.Context(), "SELECT status, COALESCE(assignee, ''), started_at IS NULL FROM issues WHERE id = ?", []any{row.id}, &status, &assignee, &unstarted)
			if status != row.status || assignee != "" || !unstarted {
				t.Fatalf("failed operation changed candidate %s: %s/%s/unstarted=%v", row.id, status, assignee, unstarted)
			}
		}
		var claims int
		te.queryScalar(t, t.Context(), "SELECT COUNT(*) FROM events WHERE issue_id IN (?, ?) AND event_type = 'claimed'", []any{"cf-review", "cf-open"}, &claims)
		if claims != 0 {
			t.Fatalf("failed operations emitted %d claim events", claims)
		}
	}
	t.Run("pre_canceled", func(t *testing.T) {
		for _, op := range operations {
			t.Run(op.name, func(t *testing.T) {
				var operationErr error
				withCountingReadTx(t, te, func(tx issueops.DBTX) error {
					canceled, cancel := context.WithCancel(t.Context())
					cancel()
					operationErr = op.read(t, canceled, tx, filter)
					return nil
				})
				if !errors.Is(operationErr, context.Canceled) {
					t.Fatalf("canceled operation error = %v, want context.Canceled", operationErr)
				}
			})
		}
		assertUnchanged(t)
	})
	t.Run("max_rows", func(t *testing.T) {
		for _, op := range operations {
			if !op.capped {
				continue
			}
			t.Run(op.name, func(t *testing.T) {
				var operationErr error
				withCountingReadTx(t, te, func(tx issueops.DBTX) error {
					capped := filter
					capped.MaxRows = 1
					capped.MaxRowsSource = "area4 fixture"
					operationErr = op.read(t, t.Context(), tx, capped)
					return nil
				})
				var capErr *issueops.ErrTooManyRows
				if !errors.As(operationErr, &capErr) || capErr.Cap != 1 {
					t.Fatalf("cap refusal = %v, want typed cap1", operationErr)
				}
			})
		}
		assertUnchanged(t)
	})
	t.Run("missing_status_table", func(t *testing.T) {
		te.exec(t, t.Context(), "RENAME TABLE custom_statuses TO custom_statuses_unavailable")
		for _, op := range operations {
			t.Run(op.name, func(t *testing.T) {
				var operationErr error
				withCountingReadTx(t, te, func(tx issueops.DBTX) error { operationErr = op.read(t, t.Context(), tx, filter); return nil })
				if !dberrors.IsMissingTable(operationErr, "custom_statuses") {
					t.Fatalf("required status table failure = %v", operationErr)
				}
			})
		}
		assertUnchanged(t)
	})
}
