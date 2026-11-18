package dolt

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// countIssueOperationStatements runs one issue-operation body inside a
// transaction on a counted connection to store's database and returns how many
// statements the body sent. BEGIN and COMMIT are outside the count.
func countIssueOperationStatements(t *testing.T, store *DoltStore, run func(ctx context.Context, tx *sql.Tx) error) int64 {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	var counts sqlcount.Counts
	db, closeDB, err := openCountedDoltConn(store.connStr, &counts)
	if err != nil {
		t.Fatalf("openCountedDoltConn: %v", err)
	}
	defer closeDB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	counts.Reset()
	if err := run(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("issue operation: %v", err)
	}
	sent := counts.Total()
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return sent
}

// TestIssueOperationStatementCounts pins how many statements each guarded
// issue operation sends inside its transaction on the Dolt server, with an
// empty wisps table. The update cases reuse the row the operation already
// read instead of reading it again, skip the lease delete when that row has no
// lease, and hydrate the result's labels and outgoing dependency records from
// the table that answered for the body.
func TestIssueOperationStatementCounts(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seed := func(id string) {
		t.Helper()
		issue := &types.Issue{ID: id, Title: "count " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("cnt-status")
	seed("cnt-leased")
	if err := store.ClaimIssue(storageissueops.WithLeaseTTL(ctx, time.Hour), "cnt-leased", "holder"); err != nil {
		t.Fatalf("claim cnt-leased: %v", err)
	}
	seed("cnt-claim")
	seed("cnt-claim-patch")
	seed("cnt-close")
	seed("cnt-reopen")
	if err := store.CloseIssue(ctx, "cnt-reopen", "done", "seed", ""); err != nil {
		t.Fatalf("close cnt-reopen: %v", err)
	}
	seed("cnt-comment")

	update := func(request publicops.UpdateRequest) func(context.Context, *sql.Tx) error {
		return func(ctx context.Context, tx *sql.Tx) error {
			_, _, err := storageissueops.ExecuteUpdate(ctx, tx, request)
			return err
		}
	}
	cases := []struct {
		name string
		run  func(context.Context, *sql.Tx) error
		want int64
	}{
		{
			name: "create",
			run: func(ctx context.Context, tx *sql.Tx) error {
				_, _, err := storageissueops.ExecuteCreate(ctx, tx, publicops.CreateRequest{
					Actor: "tester",
					Issue: &types.Issue{Title: "created", Priority: 2, IssueType: types.TypeTask},
				})
				return err
			},
			want: 17,
		},
		{
			name: "update with a status change and no lease",
			run: update(publicops.UpdateRequest{Actor: "tester", IssueID: "cnt-status", Patch: publicops.IssuePatch{
				Status: publicops.Field[publicops.Status]{Set: true, Value: publicops.StatusBlocked},
			}}),
			want: 9,
		},
		{
			name: "update with an existing lease",
			run: update(publicops.UpdateRequest{Actor: "tester", IssueID: "cnt-leased", Patch: publicops.IssuePatch{
				Status: publicops.Field[publicops.Status]{Set: true, Value: publicops.StatusOpen},
			}}),
			want: 10,
		},
		{
			name: "claim only",
			run:  update(publicops.UpdateRequest{Actor: "worker", IssueID: "cnt-claim", Claim: true}),
			want: 17,
		},
		{
			name: "claim plus patch",
			run: update(publicops.UpdateRequest{Actor: "worker", IssueID: "cnt-claim-patch", Claim: true, Patch: publicops.IssuePatch{
				Title: publicops.Field[string]{Set: true, Value: "claimed and retitled"},
			}}),
			want: 21,
		},
		{
			name: "close",
			run: func(ctx context.Context, tx *sql.Tx) error {
				_, _, err := storageissueops.ExecuteClose(ctx, tx, publicops.CloseRequest{Actor: "tester", IssueID: "cnt-close", Reason: "done"})
				return err
			},
			want: 27,
		},
		{
			name: "reopen",
			run: func(ctx context.Context, tx *sql.Tx) error {
				_, _, err := storageissueops.ExecuteReopen(ctx, tx, publicops.ReopenRequest{Actor: "tester", IssueID: "cnt-reopen"})
				return err
			},
			want: 21,
		},
		{
			name: "comment",
			run: func(ctx context.Context, tx *sql.Tx) error {
				_, _, err := storageissueops.ExecuteAddComment(ctx, tx, publicops.AddCommentRequest{Author: "tester", IssueID: "cnt-comment", Text: "hello"})
				return err
			},
			want: 5,
		},
	}
	for _, tc := range cases {
		got := countIssueOperationStatements(t, store, tc.run)
		t.Logf("%s: %d statements", tc.name, got)
		if got != tc.want {
			t.Errorf("%s sent %d statements, want %d", tc.name, got, tc.want)
		}
	}
}

// TestIssueOperationsUpdateOverlappingIDReturnsDurableRelations: when an ID
// has a row in both issues and wisps, the update result's body comes from
// issues, and so do its labels and outgoing dependency records. Before, the
// labels and records were read again with wisp-first routing and came from
// wisp_labels and wisp_dependencies, so the result mixed two rows.
func TestIssueOperationsUpdateOverlappingIDReturnsDurableRelations(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, id := range []string{"ovl-anchor", "ovl-durable-target", "ovl-wisp-target"} {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if err := store.AddLabel(ctx, "ovl-anchor", "durable-label", "seed"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "ovl-anchor", DependsOnID: "ovl-durable-target", Type: types.DepBlocks}, "seed"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral)
		 VALUES ('ovl-anchor', 'wisp copy', '', '', '', '', 'open', 2, 'task', 1)`,
		`INSERT INTO wisp_labels (issue_id, label) VALUES ('ovl-anchor', 'wisp-label')`,
		`INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by)
		 VALUES ('ovl-wisp-edge', 'ovl-anchor', 'ovl-wisp-target', 'blocks', 'seed')`,
	} {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed the wisp copy: %v", err)
		}
	}

	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "tester", IssueID: "ovl-anchor", Patch: publicops.IssuePatch{
		Priority: publicops.Field[int]{Set: true, Value: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Issue.Title != "ovl-anchor" {
		t.Fatalf("result body title = %q, want the durable row's %q", result.Issue.Title, "ovl-anchor")
	}
	if !slices.Equal(result.Issue.Labels, []string{"durable-label"}) {
		t.Errorf("result labels = %v, want the durable [durable-label]", result.Issue.Labels)
	}
	var targets []string
	for _, dep := range result.Issue.Dependencies {
		targets = append(targets, dep.DependsOnID)
	}
	if !slices.Equal(targets, []string{"ovl-durable-target"}) {
		t.Errorf("result outgoing dependency targets = %v, want the durable [ovl-durable-target]", targets)
	}
}

// TestIssueOperationsUpdateClaimThenReleaseDeletesTheNewLease: a claim in the
// same update inserts a lease row, so the lease delete must be decided from
// the row read after the claim. The row read before the claim has no lease,
// and deciding from it would skip the delete and leave a lease on an issue the
// update released.
func TestIssueOperationsUpdateClaimThenReleaseDeletesTheNewLease(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{ID: "lease-claim-release", Title: "claim then release", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := operations.Update(ctx, publicops.UpdateRequest{
		Actor:   "worker",
		IssueID: issue.ID,
		Claim:   true,
		Patch: publicops.IssuePatch{
			Status:   publicops.Field[publicops.Status]{Set: true, Value: publicops.StatusOpen},
			Assignee: publicops.Field[string]{Set: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Issue.Status != publicops.StatusOpen || result.Issue.Assignee != "" || result.Issue.LeaseExpiresAt != nil {
		t.Fatalf("claim then release = status %q, assignee %q, lease %v; want open, unassigned, no lease",
			result.Issue.Status, result.Issue.Assignee, result.Issue.LeaseExpiresAt)
	}
	var leases int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM leases WHERE issue_id = ?`, issue.ID).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("leases rows for %s = %d after the update released the claim, want 0", issue.ID, leases)
	}
}
