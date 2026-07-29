package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func TestRunInTransactionIgnoredWritesStayOnActiveBranch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	branch, err := store.CurrentBranch(ctx)
	if err != nil {
		t.Fatalf("current branch: %v", err)
	}

	wispID := "test-wisp-branch-local"
	wisp := &types.Issue{
		ID:        wispID,
		Title:     "branch-local ignored tx wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.RunInTransaction(ctx, "test: create branch-local wisp", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, wisp, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction create wisp: %v", err)
	}

	assertWispCount(ctx, t, store.db, wispID, 1)

	if err := store.Checkout(ctx, "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	assertWispCount(ctx, t, store.db, wispID, 0)

	if err := store.Checkout(ctx, branch); err != nil {
		t.Fatalf("checkout %s: %v", branch, err)
	}
	assertWispCount(ctx, t, store.db, wispID, 1)
}

func TestRunInTransactionWispCreatePersistsInitialSideTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createdAt := time.Date(2026, 5, 22, 6, 0, 0, 0, time.UTC)
	wisp := &types.Issue{
		ID:        "test-wisp-tx-side-tables",
		Title:     "transactional wisp with initial side tables",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
		Labels:    []string{"alpha", "beta"},
		Comments: []*types.Comment{{
			Author:    "tester",
			Text:      "seed comment",
			CreatedAt: createdAt,
		}},
	}
	if err := store.RunInTransaction(ctx, "test: create wisp side tables", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, wisp, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction create wisp: %v", err)
	}

	assertWispCount(ctx, t, store.db, wisp.ID, 1)
	assertTableCount(ctx, t, store.db, "wisp_labels", wisp.ID, 2)
	assertTableCount(ctx, t, store.db, "wisp_comments", wisp.ID, 1)

	var labelEventCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ? AND event_type = ?",
		wisp.ID, types.EventLabelAdded,
	).Scan(&labelEventCount); err != nil {
		t.Fatalf("query wisp label events for %s: %v", wisp.ID, err)
	}
	if labelEventCount != 2 {
		t.Fatalf("wisp label event count for %s = %d, want 2", wisp.ID, labelEventCount)
	}
}

func TestRunInTransactionCloseIssueEmitsEvent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:          "test-tx-close-event",
		Title:       "transaction close emits event",
		Description: "exercise doltTransaction.CloseIssue",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if err := store.RunInTransaction(ctx, "test: close emits event", func(tx storage.Transaction) error {
		return tx.CloseIssue(ctx, issue.ID, "done", "tester", "session-1")
	}); err != nil {
		t.Fatalf("RunInTransaction CloseIssue: %v", err)
	}

	assertCommittedEventCount(ctx, t, store.db, issue.ID, types.EventClosed, 1)
}

func TestRunInTransactionAlreadyClosedDoesNotCommitUnrelatedEvent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:          "test-tx-close-noop-event",
		Title:       "transaction no-op close leaves events alone",
		Description: "exercise doltTransaction.CloseIssue already-closed path",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "session-1"); err != nil {
		t.Fatalf("CloseIssue seed: %v", err)
	}

	const strayComment = "uncommitted stray event"
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO events (id, issue_id, event_type, actor, comment) VALUES (?, ?, ?, ?, ?)",
		issueops.NewEventID(), issue.ID, types.EventCommented, "tester", strayComment,
	); err != nil {
		t.Fatalf("insert stray event: %v", err)
	}

	if err := store.RunInTransaction(ctx, "test: already closed does not stage events", func(tx storage.Transaction) error {
		return tx.CloseIssue(ctx, issue.ID, "still done", "tester", "session-2")
	}); err != nil {
		t.Fatalf("RunInTransaction CloseIssue already closed: %v", err)
	}

	var got int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events AS OF 'HEAD' WHERE issue_id = ? AND event_type = ? AND comment = ?",
		issue.ID, types.EventCommented, strayComment,
	).Scan(&got); err != nil {
		t.Fatalf("count committed stray events: %v", err)
	}
	if got != 0 {
		t.Fatalf("committed stray event count = %d, want 0", got)
	}
	assertCommittedEventCount(ctx, t, store.db, issue.ID, types.EventClosed, 1)
}

func TestRunInTransactionAddLabelEmitsEvent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:          "test-tx-add-label-event",
		Title:       "transaction add label emits event",
		Description: "exercise doltTransaction.AddLabel",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if err := store.RunInTransaction(ctx, "test: add label emits event", func(tx storage.Transaction) error {
		return tx.AddLabel(ctx, issue.ID, "triaged", "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction AddLabel: %v", err)
	}

	assertCommittedEventCount(ctx, t, store.db, issue.ID, types.EventLabelAdded, 1)
}

func TestRunInTransactionRemoveLabelEmitsEvent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:          "test-tx-remove-label-event",
		Title:       "transaction remove label emits event",
		Description: "exercise doltTransaction.RemoveLabel",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.AddLabel(ctx, issue.ID, "triaged", "tester"); err != nil {
		t.Fatalf("AddLabel seed: %v", err)
	}

	if err := store.RunInTransaction(ctx, "test: remove label emits event", func(tx storage.Transaction) error {
		return tx.RemoveLabel(ctx, issue.ID, "triaged", "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction RemoveLabel: %v", err)
	}

	assertCommittedEventCount(ctx, t, store.db, issue.ID, types.EventLabelRemoved, 1)
}

func assertCommittedEventCount(ctx context.Context, t *testing.T, db *sql.DB, issueID string, eventType types.EventType, want int) {
	t.Helper()

	var got int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events AS OF 'HEAD' WHERE issue_id = ? AND event_type = ?",
		issueID, eventType,
	).Scan(&got); err != nil {
		t.Fatalf("count committed %s events for %s: %v", eventType, issueID, err)
	}
	if got != want {
		t.Fatalf("committed %s event count for %s = %d, want %d", eventType, issueID, got, want)
	}
}

func TestRunInTransactionCreateIssuesMixedWispReadYourWrites(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	regular := &types.Issue{
		ID:        "test-mixed-batch-regular",
		Title:     "regular issue in mixed transaction batch",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	wisp := &types.Issue{
		ID:        "test-mixed-batch-wisp",
		Title:     "wisp issue in mixed transaction batch",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
		Labels:    []string{"seed"},
	}
	if err := store.RunInTransaction(ctx, "test: create mixed transaction batch", func(tx storage.Transaction) error {
		if err := tx.CreateIssues(ctx, []*types.Issue{regular, wisp}, "tester"); err != nil {
			return err
		}
		got, err := tx.GetIssue(ctx, wisp.ID)
		if err != nil {
			return err
		}
		if got.ID != wisp.ID || !got.Ephemeral {
			return fmt.Errorf("GetIssue(%s) = %+v, want active wisp", wisp.ID, got)
		}
		if err := tx.AddLabel(ctx, wisp.ID, "txn", "tester"); err != nil {
			return err
		}
		labels, err := tx.GetLabels(ctx, wisp.ID)
		if err != nil {
			return err
		}
		if len(labels) != 2 || labels[0] != "seed" || labels[1] != "txn" {
			return fmt.Errorf("wisp labels in tx = %v, want [seed txn]", labels)
		}
		return nil
	}); err != nil {
		t.Fatalf("RunInTransaction mixed CreateIssues: %v", err)
	}

	assertIssueCount(ctx, t, store.db, regular.ID, 1)
	assertWispCount(ctx, t, store.db, wisp.ID, 1)
	assertTableCount(ctx, t, store.db, "wisp_labels", wisp.ID, 2)
}

func TestRunInTransactionCreateIssuesAllWispBatchReconcilesChildCounters(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	parent := &types.Issue{
		ID:        "test-tx-wisp-parent",
		Title:     "transactional wisp parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	child := &types.Issue{
		ID:        parent.ID + ".3",
		Title:     "transactional wisp child",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.RunInTransaction(ctx, "test: create wisp transaction batch", func(tx storage.Transaction) error {
		return tx.CreateIssues(ctx, []*types.Issue{parent, child}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction all-wisp CreateIssues: %v", err)
	}

	var lastChild int
	if err := store.db.QueryRowContext(ctx,
		"SELECT last_child FROM wisp_child_counters WHERE parent_id = ?",
		parent.ID,
	).Scan(&lastChild); err != nil {
		t.Fatalf("read wisp child counter: %v", err)
	}
	if lastChild != 3 {
		t.Fatalf("wisp last_child = %d, want 3", lastChild)
	}
}

func TestValidateCreateIssuesMixedBucketDependenciesRejectsCrossBucketEdges(t *testing.T) {
	regularA := &types.Issue{ID: "test-regular-a", IssueType: types.TypeTask}
	regularB := &types.Issue{ID: "test-regular-b", IssueType: types.TypeTask}
	wispA := &types.Issue{ID: "test-wisp-a", IssueType: types.TypeTask, Ephemeral: true}
	wispB := &types.Issue{ID: "test-wisp-b", IssueType: types.TypeTask, Ephemeral: true}

	tests := []struct {
		name      string
		regulars  []*types.Issue
		wisps     []*types.Issue
		wantError bool
	}{
		{
			name: "regular to wisp",
			regulars: []*types.Issue{{
				ID:        regularA.ID,
				IssueType: types.TypeTask,
				Dependencies: []*types.Dependency{{
					DependsOnID: wispA.ID,
					Type:        types.DepBlocks,
				}},
			}},
			wisps:     []*types.Issue{wispA},
			wantError: true,
		},
		{
			name:     "wisp to regular",
			regulars: []*types.Issue{regularA},
			wisps: []*types.Issue{{
				ID:        wispA.ID,
				IssueType: types.TypeTask,
				Ephemeral: true,
				Dependencies: []*types.Dependency{{
					DependsOnID: regularA.ID,
					Type:        types.DepBlocks,
				}},
			}},
			wantError: true,
		},
		{
			name: "same bucket dependencies",
			regulars: []*types.Issue{
				regularB,
				{
					ID:        regularA.ID,
					IssueType: types.TypeTask,
					Dependencies: []*types.Dependency{{
						DependsOnID: regularB.ID,
						Type:        types.DepBlocks,
					}},
				},
			},
			wisps: []*types.Issue{
				wispB,
				{
					ID:        wispA.ID,
					IssueType: types.TypeTask,
					Ephemeral: true,
					Dependencies: []*types.Dependency{{
						DependsOnID: wispB.ID,
						Type:        types.DepBlocks,
					}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := append(append([]*types.Issue{}, tt.regulars...), tt.wisps...)
			err := issueops.ValidateCreateIssuesMixedBucketDependencies(issues)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "cross-bucket dependency") {
					t.Fatalf("error = %v, want cross-bucket dependency", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}

func TestRunInTransactionCreateIssuesRejectsRegularToWispBatchDependency(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	regular := &types.Issue{
		ID:        "test-mixed-batch-regular-dep-source",
		Title:     "regular issue with wisp dependency",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Dependencies: []*types.Dependency{{
			DependsOnID: "test-mixed-batch-wisp-dep-target",
			Type:        types.DepBlocks,
		}},
	}
	wisp := &types.Issue{
		ID:        "test-mixed-batch-wisp-dep-target",
		Title:     "wisp dependency target",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	err := store.RunInTransaction(ctx, "test: reject regular-to-wisp batch dependency", func(tx storage.Transaction) error {
		return tx.CreateIssues(ctx, []*types.Issue{regular, wisp}, "tester")
	})
	if err == nil || !strings.Contains(err.Error(), "cross-bucket dependency") {
		t.Fatalf("RunInTransaction mixed CreateIssues error = %v, want cross-bucket dependency", err)
	}

	assertIssueCount(ctx, t, store.db, regular.ID, 0)
	assertWispCount(ctx, t, store.db, wisp.ID, 0)
}

func TestRunInTransactionCreateIssuesRejectsWispToRegularBatchDependency(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	regular := &types.Issue{
		ID:        "test-mixed-batch-regular-dep-target",
		Title:     "regular dependency target",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	wisp := &types.Issue{
		ID:        "test-mixed-batch-wisp-dep-source",
		Title:     "wisp issue with regular dependency",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
		Dependencies: []*types.Dependency{{
			DependsOnID: regular.ID,
			Type:        types.DepBlocks,
		}},
	}
	err := store.RunInTransaction(ctx, "test: reject wisp-to-regular batch dependency", func(tx storage.Transaction) error {
		return tx.CreateIssues(ctx, []*types.Issue{regular, wisp}, "tester")
	})
	if err == nil || !strings.Contains(err.Error(), "cross-bucket dependency") {
		t.Fatalf("RunInTransaction mixed CreateIssues error = %v, want cross-bucket dependency", err)
	}

	assertIssueCount(ctx, t, store.db, regular.ID, 0)
	assertWispCount(ctx, t, store.db, wisp.ID, 0)
}

func TestRunInTransactionCreateIssuesSkipsExplicitIDPrefixValidation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "foreign-explicit-batch-id",
		Title:     "explicit ID outside configured prefix",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.RunInTransaction(ctx, "test: create explicit id batch", func(tx storage.Transaction) error {
		return tx.CreateIssues(ctx, []*types.Issue{issue}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction explicit-ID CreateIssues: %v", err)
	}

	assertIssueCount(ctx, t, store.db, issue.ID, 1)
}

func assertIssueCount(ctx context.Context, t *testing.T, db *sql.DB, id string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("query issue count for %s: %v", id, err)
	}
	if got != want {
		t.Fatalf("issue count for %s = %d, want %d", id, got, want)
	}
}

func assertWispCount(ctx context.Context, t *testing.T, db *sql.DB, id string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("query wisp count for %s: %v", id, err)
	}
	if got != want {
		t.Fatalf("wisp count for %s = %d, want %d", id, got, want)
	}
}

func assertTableCount(ctx context.Context, t *testing.T, db *sql.DB, table, id string, want int) {
	t.Helper()
	var got int
	query := "SELECT COUNT(*) FROM " + table + " WHERE issue_id = ?"
	if err := db.QueryRowContext(ctx, query, id).Scan(&got); err != nil {
		t.Fatalf("query %s count for %s: %v", table, id, err)
	}
	if got != want {
		t.Fatalf("%s count for %s = %d, want %d", table, id, got, want)
	}
}
