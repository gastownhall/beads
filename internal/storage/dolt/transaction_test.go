package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestRunInTransactionCallbackConnectionErrorIsNotReplayed establishes the
// public at-most-once callback contract. The callback's error looks transient,
// but the caller may have performed external work before returning it.
func TestRunInTransactionCallbackConnectionErrorIsNotReplayed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	calls := 0
	err := store.RunInTransaction(ctx, "test: callback at most once", func(storage.Transaction) error {
		calls++
		if calls == 1 {
			return errors.New("invalid connection")
		}
		return nil
	})
	if err == nil {
		t.Fatal("callback connection error returned nil")
	}
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
}

// TestRunInIssueLifecycleTransactionRollbackDoesNotReplayCallback keeps the
// public lifecycle callback outside withRetryTx's rollback-safe retry loop.
// The SQL transaction may safely be retried internally elsewhere, but this
// callback can perform caller-owned work and therefore runs at most once.
func TestRunInIssueLifecycleTransactionRollbackDoesNotReplayCallback(t *testing.T) {
	rollback := &mysql.MySQLError{
		Number:  1105,
		Message: "Merge conflict detected, @autocommit transaction rolled back",
	}
	store := &DoltStore{}
	calls := 0
	runnerCalls := 0

	err := store.runInIssueLifecycleTransaction(context.Background(), "test: lifecycle callback at most once", func(storage.IssueLifecycleTransaction) error {
		calls++
		return nil
	}, func(_ context.Context, fn func(*sql.Tx) error) error {
		runnerCalls++
		if err := fn(nil); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("RunInIssueLifecycleTransaction() error = %v, want %v", err, rollback)
	}
	if calls != 1 {
		t.Fatalf("lifecycle callback calls = %d, want 1", calls)
	}
	if runnerCalls != 1 {
		t.Fatalf("lifecycle transaction attempts = %d, want 1", runnerCalls)
	}
}

func TestPublicTransactionSetupRetriesRollbackSafeErrorsBeforeCallback(t *testing.T) {
	rollback1105 := &mysql.MySQLError{
		Number:  1105,
		Message: "Merge conflict detected, @autocommit transaction rolled back",
	}
	tests := []struct {
		name string
		err  error
		run  func(*DoltStore, error, *int, *int) error
	}{
		{
			name: "transaction exact Dolt rollback",
			err:  rollback1105,
			run: func(store *DoltStore, setupErr error, attempts, callbacks *int) error {
				return store.runInTransaction(context.Background(), "test: setup retry", func(storage.Transaction) error {
					*callbacks++
					return nil
				}, func(_ context.Context, _ string, fn func(storage.Transaction) error) error {
					*attempts++
					if *attempts == 1 {
						return setupErr
					}
					return fn(nil)
				})
			},
		},
		{
			name: "transaction deadlock",
			err:  &mysql.MySQLError{Number: 1213, Message: "deadlock"},
			run: func(store *DoltStore, setupErr error, attempts, callbacks *int) error {
				return store.runInTransaction(context.Background(), "test: setup retry", func(storage.Transaction) error {
					*callbacks++
					return nil
				}, func(_ context.Context, _ string, fn func(storage.Transaction) error) error {
					*attempts++
					if *attempts == 1 {
						return setupErr
					}
					return fn(nil)
				})
			},
		},
		{
			name: "lifecycle lock timeout",
			err:  &mysql.MySQLError{Number: 1205, Message: "lock wait timeout"},
			run: func(store *DoltStore, setupErr error, attempts, callbacks *int) error {
				return store.runInIssueLifecycleTransaction(context.Background(), "test: setup retry", func(storage.IssueLifecycleTransaction) error {
					*callbacks++
					return nil
				}, func(_ context.Context, fn func(*sql.Tx) error) error {
					*attempts++
					if *attempts == 1 {
						return setupErr
					}
					return fn(nil)
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &DoltStore{}
			attempts := 0
			callbacks := 0
			if err := tt.run(store, tt.err, &attempts, &callbacks); err != nil {
				t.Fatalf("transaction wrapper error = %v, want nil", err)
			}
			if attempts != 2 {
				t.Fatalf("setup attempts = %d, want 2", attempts)
			}
			if callbacks != 1 {
				t.Fatalf("callback calls = %d, want 1", callbacks)
			}
		})
	}
}

// TestRunInTransactionSerializationConflictInvokesCallbacksOnce orders two
// independent handles so the stale transaction loses at commit. The public
// callbacks must still each run once, and the winner's content must survive.
func TestRunInTransactionSerializationConflictInvokesCallbacksOnce(t *testing.T) {
	storeA, cleanupA := setupConcurrentTestStore(t)
	defer cleanupA()

	ctx, cancel := testContext(t)
	defer cancel()

	storeB, err := New(ctx, &Config{
		Path:           t.TempDir(),
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		ServerHost:     "127.0.0.1",
		ServerPort:     testServerPort,
		Database:       storeA.database,
		MaxOpenConns:   2,
	})
	if err != nil {
		t.Fatalf("open second store for %s: %v", storeA.database, err)
	}
	defer storeB.Close()

	issue := &types.Issue{
		ID:          "test-tx-at-most-once",
		Title:       "at-most-once transaction",
		Description: "initial",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := storeA.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}

	var callsA, callsB atomic.Int32
	aPrepared := make(chan struct{})
	bPrepared := make(chan struct{})
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	errA := make(chan error, 1)
	errB := make(chan error, 1)

	go func() {
		errA <- storeA.RunInTransaction(ctx, "test: winner transaction", func(tx storage.Transaction) error {
			callsA.Add(1)
			if err := tx.UpdateIssue(ctx, issue.ID, map[string]interface{}{
				"description": "winner",
			}, "winner"); err != nil {
				return err
			}
			close(aPrepared)
			return waitForTransactionRelease(ctx, releaseA)
		})
	}()

	go func() {
		errB <- storeB.RunInTransaction(ctx, "test: stale transaction", func(tx storage.Transaction) error {
			callsB.Add(1)
			if err := tx.UpdateIssue(ctx, issue.ID, map[string]interface{}{
				"description": "stale",
			}, "stale"); err != nil {
				return err
			}
			close(bPrepared)
			return waitForTransactionRelease(ctx, releaseB)
		})
	}()

	waitForTransactionPrepared(t, ctx, aPrepared, "winner")
	waitForTransactionPrepared(t, ctx, bPrepared, "stale")
	close(releaseA)
	if err := <-errA; err != nil {
		t.Fatalf("winner transaction: %v", err)
	}
	close(releaseB)
	err = <-errB
	if err == nil {
		t.Fatal("stale transaction succeeded, want serialization conflict")
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || (mysqlErr.Number != 1213 && mysqlErr.Number != 1205) {
		t.Fatalf("stale transaction error = %v, want MySQL 1213 or 1205", err)
	}
	if errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("stale transaction error = %v unexpectedly marks an indeterminate commit", err)
	}
	if got := callsA.Load(); got != 1 {
		t.Errorf("winner callback calls = %d, want 1", got)
	}
	if got := callsB.Load(); got != 1 {
		t.Errorf("stale callback calls = %d, want 1", got)
	}

	freshDB, err := sql.Open("mysql", storeA.connStr)
	if err != nil {
		t.Fatalf("open fresh SQL handle: %v", err)
	}
	defer freshDB.Close()
	var description string
	if err := freshDB.QueryRowContext(ctx,
		"SELECT description FROM issues WHERE id = ?", issue.ID).Scan(&description); err != nil {
		t.Fatalf("read winner result from fresh SQL handle: %v", err)
	}
	if description != "winner" {
		t.Errorf("fresh SQL description = %q, want winner", description)
	}
}

func waitForTransactionPrepared(t *testing.T, ctx context.Context, prepared <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-prepared:
	case <-ctx.Done():
		t.Fatalf("%s transaction was not prepared: %v", name, ctx.Err())
	}
}

func waitForTransactionRelease(ctx context.Context, release <-chan struct{}) error {
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

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

	assertRecordedEventCount(ctx, t, store.db, issue.ID, types.EventClosed, 1)
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
		uuid.Must(uuid.NewV7()).String(), issue.ID, types.EventCommented, "tester", strayComment,
	); err != nil {
		t.Fatalf("insert stray event: %v", err)
	}

	if err := store.RunInTransaction(ctx, "test: already closed does not stage events", func(tx storage.Transaction) error {
		return tx.CloseIssue(ctx, issue.ID, "still done", "tester", "session-2")
	}); err != nil {
		t.Fatalf("RunInTransaction CloseIssue already closed: %v", err)
	}

	// events is dolt_ignored since 0062: the stray row can never leak into a
	// commit because nothing events-shaped is committed at all — but it must
	// still be durable in the working set alongside the close event.
	assertEventsNotCommitted(ctx, t, store.db)
	var got int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ? AND comment = ?",
		issue.ID, types.EventCommented, strayComment,
	).Scan(&got); err != nil {
		t.Fatalf("count stray events: %v", err)
	}
	if got != 1 {
		t.Fatalf("stray event count = %d, want 1", got)
	}
	assertRecordedEventCount(ctx, t, store.db, issue.ID, types.EventClosed, 1)
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

	assertRecordedEventCount(ctx, t, store.db, issue.ID, types.EventLabelAdded, 1)
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

	assertRecordedEventCount(ctx, t, store.db, issue.ID, types.EventLabelRemoved, 1)
}

// assertRecordedEventCount counts audit rows in the working-set events table.
// events is dolt_ignored since migration 0062 (bd-red8u): rows are durable and
// visible to every client of the store but never part of committed history,
// so there is no committed variant of this assertion anymore — see
// assertEventsNotCommitted for the plane check.
func assertRecordedEventCount(ctx context.Context, t *testing.T, db *sql.DB, issueID string, eventType types.EventType, want int) {
	t.Helper()

	var got int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		issueID, eventType,
	).Scan(&got); err != nil {
		t.Fatalf("count recorded %s events for %s: %v", eventType, issueID, err)
	}
	if got != want {
		t.Fatalf("recorded %s event count for %s = %d, want %d", eventType, issueID, got, want)
	}
}

// assertEventsNotCommitted pins the 0062 plane contract: no events ROW ever
// reaches committed history. On a production-shaped database the table itself
// is absent at HEAD (the AS OF probe errors — the embedded contract tests
// assert that stronger form), but the shared branch-per-test database
// deliberately materializes an EMPTY events shell at HEAD so branches inherit
// the schema (testutil.MaterializeLocalTableSchemasForBranchTests), so here
// the probe may also succeed with zero rows. Any committed row is a
// regression on both shapes.
func assertEventsNotCommitted(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()

	var got int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events AS OF 'HEAD'").Scan(&got); err == nil && got != 0 {
		t.Fatalf("events has %d rows at HEAD; want none in committed history (dolt_ignored, 0062)", got)
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
