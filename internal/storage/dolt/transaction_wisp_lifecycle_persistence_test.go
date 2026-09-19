package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestRunInTransactionWispLifecyclePersistsDurableDependent verifies that a
// transactional lifecycle transition of an ignored wisp still publishes the
// durable is_blocked recomputation of an issue that depends on it.
func TestRunInTransactionWispLifecyclePersistsDurableDependent(t *testing.T) {
	ctx, store := newTransactionWispLifecycleStore(t)

	t.Run("close", func(t *testing.T) {
		const (
			dependerID = "tx-wisp-close-depender"
			wispID     = "tx-wisp-close-blocker"
		)
		seedTransactionWispBlocker(t, ctx, store, dependerID, wispID)
		before := transactionWispLifecycleHead(t, ctx, store)

		if err := store.RunInIssueLifecycleTransaction(ctx, "test: close wisp blocker", func(tx storage.IssueLifecycleTransaction) error {
			return tx.CloseIssue(ctx, wispID, "done", "tester", "session")
		}); err != nil {
			t.Fatalf("RunInTransaction CloseIssue: %v", err)
		}

		assertTransactionWispLifecyclePostState(t, ctx, store, dependerID, before, false)
	})

	t.Run("reopen", func(t *testing.T) {
		const (
			dependerID = "tx-wisp-reopen-depender"
			wispID     = "tx-wisp-reopen-blocker"
		)
		seedTransactionWispBlocker(t, ctx, store, dependerID, wispID)
		if err := store.CloseIssue(ctx, wispID, "done", "tester", "session"); err != nil {
			t.Fatalf("seed CloseIssue: %v", err)
		}
		if err := store.Commit(ctx, "test: seed closed wisp blocker"); err != nil {
			t.Fatalf("Commit closed seed: %v", err)
		}
		before := transactionWispLifecycleHead(t, ctx, store)

		if err := store.RunInIssueLifecycleTransaction(ctx, "test: reopen wisp blocker", func(tx storage.IssueLifecycleTransaction) error {
			changed, err := tx.ReopenIssueWithResult(ctx, wispID, "", "tester")
			if err != nil {
				return err
			}
			if !changed {
				t.Fatal("transactional reopen reported Changed=false")
			}
			return nil
		}); err != nil {
			t.Fatalf("RunInTransaction reopen: %v", err)
		}

		assertTransactionWispLifecyclePostState(t, ctx, store, dependerID, before, true)
	})
}

// TestRunInIssueLifecycleTransactionStandaloneWispPersistsWithoutDoltCommit
// verifies that ignored-only lifecycle work commits its SQL transaction without
// attempting a version-control commit.
func TestRunInIssueLifecycleTransactionStandaloneWispPersistsWithoutDoltCommit(t *testing.T) {
	ctx, store := newTransactionWispLifecycleStore(t)

	t.Run("close", func(t *testing.T) {
		const wispID = "tx-standalone-wisp-close"
		createWisp(t, ctx, store, wispID)
		before := transactionWispLifecycleHead(t, ctx, store)

		if err := store.RunInIssueLifecycleTransaction(ctx, "test: close standalone wisp", func(tx storage.IssueLifecycleTransaction) error {
			return tx.CloseIssue(ctx, wispID, "done", "tester", "session")
		}); err != nil {
			t.Fatalf("RunInIssueLifecycleTransaction CloseIssue: %v", err)
		}
		assertStandaloneWispLifecycleState(t, ctx, store, wispID, before, types.StatusClosed)
	})

	t.Run("reopen", func(t *testing.T) {
		const wispID = "tx-standalone-wisp-reopen"
		createWisp(t, ctx, store, wispID)
		if err := store.CloseIssue(ctx, wispID, "done", "tester", "session"); err != nil {
			t.Fatalf("seed CloseIssue: %v", err)
		}
		before := transactionWispLifecycleHead(t, ctx, store)

		if err := store.RunInIssueLifecycleTransaction(ctx, "test: reopen standalone wisp", func(tx storage.IssueLifecycleTransaction) error {
			changed, err := tx.ReopenIssueWithResult(ctx, wispID, "", "tester")
			if err != nil {
				return err
			}
			if !changed {
				t.Fatal("transactional reopen reported Changed=false")
			}
			return nil
		}); err != nil {
			t.Fatalf("RunInIssueLifecycleTransaction ReopenIssueWithResult: %v", err)
		}
		assertStandaloneWispLifecycleState(t, ctx, store, wispID, before, types.StatusOpen)
	})
}

func TestRunInIssueLifecycleTransactionSameStatusDoesNotPublishUnrelatedDurableChanges(t *testing.T) {
	ctx, store := newTransactionWispLifecycleStore(t)
	const (
		targetID = "tx-same-status-target"
		dirtyID  = "tx-same-status-dirty"
	)
	createPerm(t, ctx, store, targetID)
	createPerm(t, ctx, store, dirtyID)
	before := transactionWispLifecycleHead(t, ctx, store)

	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", "working-only title", dirtyID); err != nil {
		t.Fatalf("stage unrelated issue edit: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"UPDATE events SET comment = ? WHERE issue_id = ? AND event_type = ?",
		"working-only event", dirtyID, string(types.EventCreated),
	); err != nil {
		t.Fatalf("stage unrelated event edit: %v", err)
	}

	if err := store.RunInIssueLifecycleTransaction(ctx, "test: same-status update", func(tx storage.IssueLifecycleTransaction) error {
		return tx.UpdateIssue(ctx, targetID, map[string]interface{}{"status": types.StatusOpen}, "tester")
	}); err != nil {
		t.Fatalf("RunInIssueLifecycleTransaction: %v", err)
	}
	if after := transactionWispLifecycleHead(t, ctx, store); after != before {
		t.Errorf("same-status transaction changed HEAD from %s to %s", before, after)
	}

	assertDirectSameStatusUnrelatedChangesWorkingOnly(t, ctx, store, dirtyID)
}

func TestRunInIssueLifecycleTransactionStandaloneWispMutationDoesNotPublishUnrelatedDurableChanges(t *testing.T) {
	ctx, store := newTransactionWispLifecycleStore(t)
	const (
		durableID       = "tx-wisp-isolation-durable"
		wispID          = "tx-wisp-isolation-wisp"
		baselineLabel   = "durable-baseline"
		workingLabel    = "durable-working-only"
		removeWispLabel = "remove-from-wisp"
		addWispLabel    = "add-to-wisp"
	)

	createPerm(t, ctx, store, durableID)
	if err := store.AddLabel(ctx, durableID, baselineLabel, "tester"); err != nil {
		t.Fatalf("AddLabel durable baseline: %v", err)
	}
	createWisp(t, ctx, store, wispID)
	if err := store.AddLabel(ctx, wispID, removeWispLabel, "tester"); err != nil {
		t.Fatalf("AddLabel wisp baseline: %v", err)
	}
	if err := store.Commit(ctx, "test: seed durable baseline"); err != nil {
		t.Fatalf("Commit baseline: %v", err)
	}
	before := transactionWispLifecycleHead(t, ctx, store)

	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", "durable working-only", durableID); err != nil {
		t.Fatalf("stage durable issue edit: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE events SET comment = ? WHERE issue_id = ? AND event_type = ?",
		"durable working-only", durableID, string(types.EventCreated)); err != nil {
		t.Fatalf("stage durable event edit: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE labels SET label = ? WHERE issue_id = ? AND label = ?",
		workingLabel, durableID, baselineLabel); err != nil {
		t.Fatalf("stage durable label edit: %v", err)
	}

	if err := store.RunInIssueLifecycleTransaction(ctx, "test: mutate standalone wisp", func(tx storage.IssueLifecycleTransaction) error {
		if err := tx.UpdateIssue(ctx, wispID, map[string]interface{}{"title": "wisp updated"}, "tester"); err != nil {
			return err
		}
		if err := tx.RemoveLabel(ctx, wispID, removeWispLabel, "tester"); err != nil {
			return err
		}
		return tx.AddLabel(ctx, wispID, addWispLabel, "tester")
	}); err != nil {
		t.Fatalf("RunInIssueLifecycleTransaction: %v", err)
	}
	if after := transactionWispLifecycleHead(t, ctx, store); after != before {
		t.Errorf("standalone wisp mutation changed HEAD from %s to %s", before, after)
	}

	wisp, err := store.GetIssue(ctx, wispID)
	if err != nil {
		t.Fatalf("GetIssue wisp: %v", err)
	}
	if wisp.Title != "wisp updated" {
		t.Errorf("wisp title = %q, want %q", wisp.Title, "wisp updated")
	}
	labels, err := store.GetLabels(ctx, wispID)
	if err != nil {
		t.Fatalf("GetLabels wisp: %v", err)
	}
	if len(labels) != 1 || labels[0] != addWispLabel {
		t.Errorf("wisp labels = %v, want [%s]", labels, addWispLabel)
	}
	events, err := store.GetEvents(ctx, wispID, 0)
	if err != nil {
		t.Fatalf("GetEvents wisp: %v", err)
	}
	eventCounts := make(map[types.EventType]int)
	for _, event := range events {
		eventCounts[event.EventType]++
	}
	if eventCounts[types.EventUpdated] != 1 || eventCounts[types.EventLabelRemoved] != 1 || eventCounts[types.EventLabelAdded] != 2 {
		t.Errorf("wisp event counts = updated:%d label_removed:%d label_added:%d",
			eventCounts[types.EventUpdated], eventCounts[types.EventLabelRemoved], eventCounts[types.EventLabelAdded])
	}

	var workingTitle, headTitle string
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", durableID).Scan(&workingTitle); err != nil {
		t.Fatalf("read working durable issue: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", durableID).Scan(&headTitle); err != nil {
		t.Fatalf("read durable issue AS OF HEAD: %v", err)
	}
	if workingTitle != "durable working-only" || headTitle != "perm "+durableID {
		t.Errorf("durable issue titles = working:%q HEAD:%q", workingTitle, headTitle)
	}

	var headLabelCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ? AND label = ?",
		durableID, workingLabel,
	).Scan(&headLabelCount); err != nil {
		t.Fatalf("read durable label AS OF HEAD: %v", err)
	}
	if headLabelCount != 0 {
		t.Errorf("durable working-only label count AS OF HEAD = %d, want 0", headLabelCount)
	}

	// events is dolt_ignored since migration 0062 (bd-red8u): it has no HEAD
	// state, so the working-only audit edit can only be read from the working
	// set. "Nothing was published" is already carried by the HEAD-unchanged
	// assertion above; what remains to check is that the wisp mutation left the
	// unrelated audit edit intact.
	var workingEventCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ? AND comment = ?",
		durableID, string(types.EventCreated), "durable working-only",
	).Scan(&workingEventCount); err != nil {
		t.Fatalf("read durable event in working set: %v", err)
	}
	if workingEventCount != 1 {
		t.Errorf("durable working-only event count = %d, want 1", workingEventCount)
	}
}

func assertDirectSameStatusUnrelatedChangesWorkingOnly(t *testing.T, ctx context.Context, store *DoltStore, dirtyID string) {
	t.Helper()
	var workingTitle, headTitle string
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", dirtyID).Scan(&workingTitle); err != nil {
		t.Fatalf("read working unrelated issue: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", dirtyID).Scan(&headTitle); err != nil {
		t.Fatalf("read unrelated issue AS OF HEAD: %v", err)
	}
	if workingTitle != "working-only title" || headTitle != "perm "+dirtyID {
		t.Errorf("unrelated issue titles = working:%q HEAD:%q", workingTitle, headTitle)
	}
	// events is dolt_ignored since migration 0062 (bd-red8u): there is no HEAD
	// state to compare against. Every caller of this helper asserts that HEAD
	// did not move, which is the stronger form of "nothing was published"; the
	// remaining events-specific claim is that the same-status update left the
	// unrelated working-set audit edit alone.
	var workingEvent int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ? AND comment = ?",
		dirtyID, string(types.EventCreated), "working-only event",
	).Scan(&workingEvent); err != nil {
		t.Fatalf("read working unrelated event: %v", err)
	}
	if workingEvent != 1 {
		t.Errorf("unrelated working-set event edit count = %d, want 1", workingEvent)
	}
}

func newTransactionWispLifecycleStore(t *testing.T) (context.Context, *DoltStore) {
	t.Helper()
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	return ctx, store
}

func seedTransactionWispBlocker(t *testing.T, ctx context.Context, store *DoltStore, dependerID, wispID string) {
	t.Helper()
	createPerm(t, ctx, store, dependerID)
	createWisp(t, ctx, store, wispID)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: dependerID, DependsOnID: wispID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if err := store.Commit(ctx, "test: seed wisp blocker"); err != nil {
		t.Fatalf("Commit seed: %v", err)
	}
	var blocked bool
	if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", dependerID).Scan(&blocked); err != nil {
		t.Fatalf("read seed durable depender: %v", err)
	}
	if !blocked {
		t.Fatalf("seed durable depender %s is not blocked", dependerID)
	}
	var committed bool
	if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", dependerID).Scan(&committed); err != nil {
		t.Fatalf("read committed seed durable depender: %v", err)
	}
	if !committed {
		t.Fatalf("committed seed durable depender %s is not blocked", dependerID)
	}
}

func transactionWispLifecycleHead(t *testing.T, ctx context.Context, store *DoltStore) string {
	t.Helper()
	head, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	return head
}

func assertTransactionWispLifecyclePostState(t *testing.T, ctx context.Context, store *DoltStore, dependerID, before string, wantBlocked bool) {
	t.Helper()
	after := transactionWispLifecycleHead(t, ctx, store)
	if after == before {
		t.Errorf("transactional wisp lifecycle did not publish durable dependent; HEAD remained %s", before)
	}

	var working bool
	if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", dependerID).Scan(&working); err != nil {
		t.Fatalf("read working durable depender: %v", err)
	}
	if working != wantBlocked {
		t.Errorf("working durable depender is_blocked = %t, want %t", working, wantBlocked)
	}

	var committed bool
	if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", dependerID).Scan(&committed); err != nil {
		t.Fatalf("read durable depender AS OF HEAD: %v", err)
	}
	if committed != wantBlocked {
		t.Errorf("committed durable depender is_blocked = %t, want %t", committed, wantBlocked)
	}
}

func assertStandaloneWispLifecycleState(t *testing.T, ctx context.Context, store *DoltStore, wispID, before string, wantStatus types.Status) {
	t.Helper()
	if after := transactionWispLifecycleHead(t, ctx, store); after != before {
		t.Fatalf("standalone wisp lifecycle changed HEAD from %s to %s", before, after)
	}
	issue, err := store.GetIssue(ctx, wispID)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", wispID, err)
	}
	if issue.Status != wantStatus {
		t.Errorf("wisp status = %q, want %q", issue.Status, wantStatus)
	}
}
