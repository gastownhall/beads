//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedTransactionWispLifecyclePersistsDurableDependent verifies that
// a transactional wisp lifecycle transition publishes the durable dependent's
// recomputed blocked state into the versioned history.
func TestEmbeddedTransactionWispLifecyclePersistsDurableDependent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()

	for _, test := range []struct {
		name        string
		wispID      string
		dependerID  string
		wantBlocked bool
		run         func(t *testing.T, store *embeddeddolt.EmbeddedDoltStore, ctx context.Context, wispID string) error
	}{
		{
			name:       "close",
			wispID:     "embedded-tx-wisp-close",
			dependerID: "embedded-tx-close-depender",
			run: func(_ *testing.T, store *embeddeddolt.EmbeddedDoltStore, ctx context.Context, wispID string) error {
				return store.RunInIssueLifecycleTransaction(ctx, "test: close wisp blocker", func(tx storage.IssueLifecycleTransaction) error {
					return tx.CloseIssue(ctx, wispID, "done", "tester", "session")
				})
			},
		},
		{
			name:        "reopen",
			wispID:      "embedded-tx-wisp-reopen",
			dependerID:  "embedded-tx-reopen-depender",
			wantBlocked: true,
			run: func(t *testing.T, store *embeddeddolt.EmbeddedDoltStore, ctx context.Context, wispID string) error {
				return store.RunInIssueLifecycleTransaction(ctx, "test: reopen wisp blocker", func(tx storage.IssueLifecycleTransaction) error {
					changed, err := tx.ReopenIssueWithResult(ctx, wispID, "", "tester")
					if err != nil {
						return err
					}
					if !changed {
						t.Fatal("transactional reopen reported Changed=false")
					}
					return nil
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			te := newTestEnv(t, "txwisp")
			seedEmbeddedTransactionWispBlocker(t, te, ctx, test.dependerID, test.wispID)
			if err := te.store.Commit(ctx, "test: seed wisp blocker"); err != nil {
				t.Fatalf("Commit seed: %v", err)
			}
			if test.name == "reopen" {
				if err := te.store.CloseIssue(ctx, test.wispID, "done", "tester", "session"); err != nil {
					t.Fatalf("seed CloseIssue: %v", err)
				}
				if err := te.store.Commit(ctx, "test: seed closed wisp blocker"); err != nil {
					t.Fatalf("Commit closed seed: %v", err)
				}
			}
			before, err := te.store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit before: %v", err)
			}

			if err := test.run(t, te.store, ctx, test.wispID); err != nil {
				t.Fatalf("RunInTransaction: %v", err)
			}

			after, err := te.store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit after: %v", err)
			}
			if after == before {
				t.Errorf("transactional wisp lifecycle did not publish durable dependent; HEAD remained %s", before)
			}

			var working, committed bool
			te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{test.dependerID}, &working)
			te.queryScalar(t, ctx, "SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", []any{test.dependerID}, &committed)
			if working != test.wantBlocked {
				t.Errorf("working durable depender is_blocked = %t, want %t", working, test.wantBlocked)
			}
			if committed != test.wantBlocked {
				t.Errorf("committed durable depender is_blocked = %t, want %t", committed, test.wantBlocked)
			}
		})
	}
}

func TestEmbeddedTransactionStandaloneWispLifecyclePersistsWithoutDoltCommit(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()

	for _, test := range []struct {
		name       string
		wispID     string
		wantStatus types.Status
		run        func(t *testing.T, store *embeddeddolt.EmbeddedDoltStore, ctx context.Context, wispID string) error
	}{
		{
			name:       "close",
			wispID:     "embedded-tx-standalone-wisp-close",
			wantStatus: types.StatusClosed,
			run: func(_ *testing.T, store *embeddeddolt.EmbeddedDoltStore, ctx context.Context, wispID string) error {
				return store.RunInIssueLifecycleTransaction(ctx, "test: close standalone wisp", func(tx storage.IssueLifecycleTransaction) error {
					return tx.CloseIssue(ctx, wispID, "done", "tester", "session")
				})
			},
		},
		{
			name:       "reopen",
			wispID:     "embedded-tx-standalone-wisp-reopen",
			wantStatus: types.StatusOpen,
			run: func(t *testing.T, store *embeddeddolt.EmbeddedDoltStore, ctx context.Context, wispID string) error {
				if err := store.CloseIssue(ctx, wispID, "done", "tester", "session"); err != nil {
					return err
				}
				return store.RunInIssueLifecycleTransaction(ctx, "test: reopen standalone wisp", func(tx storage.IssueLifecycleTransaction) error {
					changed, err := tx.ReopenIssueWithResult(ctx, wispID, "", "tester")
					if err != nil {
						return err
					}
					if !changed {
						t.Fatal("transactional reopen reported Changed=false")
					}
					return nil
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			te := newTestEnv(t, "txstandalone")
			if err := te.store.CreateIssue(ctx, &types.Issue{ID: test.wispID, Title: test.wispID, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}, "tester"); err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}
			before, err := te.store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit before: %v", err)
			}

			if err := test.run(t, te.store, ctx, test.wispID); err != nil {
				t.Fatalf("RunInIssueLifecycleTransaction: %v", err)
			}
			after, err := te.store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit after: %v", err)
			}
			if after != before {
				t.Fatalf("standalone wisp lifecycle changed HEAD from %s to %s", before, after)
			}
			issue, err := te.store.GetIssue(ctx, test.wispID)
			if err != nil {
				t.Fatalf("GetIssue: %v", err)
			}
			if issue.Status != test.wantStatus {
				t.Errorf("wisp status = %q, want %q", issue.Status, test.wantStatus)
			}
		})
	}
}

func TestEmbeddedTransactionSameStatusDoesNotPublishUnrelatedDurableChanges(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "txsamestatus")
	const (
		targetID = "embedded-tx-same-status-target"
		dirtyID  = "embedded-tx-same-status-dirty"
	)
	for _, issue := range []*types.Issue{
		{ID: targetID, Title: "target baseline", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: dirtyID, Title: "dirty baseline", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	if err := te.store.Commit(ctx, "test: seed same-status baseline"); err != nil {
		t.Fatalf("Commit baseline: %v", err)
	}
	before, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit before: %v", err)
	}

	te.exec(t, ctx, "UPDATE issues SET title = ? WHERE id = ?", "working-only title", dirtyID)
	te.exec(t, ctx,
		"UPDATE events SET comment = ? WHERE issue_id = ? AND event_type = ?",
		"working-only event", dirtyID, string(types.EventCreated))

	if err := te.store.RunInIssueLifecycleTransaction(ctx, "test: same-status update", func(tx storage.IssueLifecycleTransaction) error {
		return tx.UpdateIssue(ctx, targetID, map[string]interface{}{"status": types.StatusOpen}, "tester")
	}); err != nil {
		t.Fatalf("RunInIssueLifecycleTransaction: %v", err)
	}
	after, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit after: %v", err)
	}
	if after != before {
		t.Errorf("same-status transaction changed HEAD from %s to %s", before, after)
	}

	var workingTitle, headTitle string
	te.queryScalar(t, ctx, "SELECT title FROM issues WHERE id = ?", []any{dirtyID}, &workingTitle)
	te.queryScalar(t, ctx, "SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", []any{dirtyID}, &headTitle)
	if workingTitle != "working-only title" || headTitle != "dirty baseline" {
		t.Errorf("unrelated issue titles = working:%q HEAD:%q", workingTitle, headTitle)
	}
	// events is dolt_ignored since migration 0062 (bd-red8u). On the
	// production-shaped database this harness builds, the table is absent from
	// committed history entirely, so an AS OF 'HEAD' probe errors rather than
	// proving anything; the plane contract itself is pinned by
	// TestEmbeddedMigration0062FlipsEventsToIgnoredPlane. "Nothing was
	// published" is already carried by the HEAD-unchanged assertion above, so
	// what is left is that the unrelated audit edit survives in the working set.
	var workingEvent int
	te.queryScalar(t, ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ? AND comment = ?",
		[]any{dirtyID, string(types.EventCreated), "working-only event"}, &workingEvent)
	if workingEvent != 1 {
		t.Errorf("unrelated working-set event edit count = %d, want 1", workingEvent)
	}
}

func TestEmbeddedTransactionStandaloneWispMutationDoesNotPublishUnrelatedDurableChanges(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "txwispisolation")
	const (
		durableID       = "tx-wisp-isolation-durable"
		wispID          = "tx-wisp-isolation-wisp"
		baselineLabel   = "durable-baseline"
		workingLabel    = "durable-working-only"
		removeWispLabel = "remove-from-wisp"
		addWispLabel    = "add-to-wisp"
	)

	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: durableID, Title: "durable baseline", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue durable: %v", err)
	}
	if err := te.store.AddLabel(ctx, durableID, baselineLabel, "tester"); err != nil {
		t.Fatalf("AddLabel durable baseline: %v", err)
	}
	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: wispID, Title: "wisp baseline", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}
	if err := te.store.AddLabel(ctx, wispID, removeWispLabel, "tester"); err != nil {
		t.Fatalf("AddLabel wisp baseline: %v", err)
	}
	if err := te.store.Commit(ctx, "test: seed durable baseline"); err != nil {
		t.Fatalf("Commit baseline: %v", err)
	}
	before, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit before: %v", err)
	}

	te.exec(t, ctx, "UPDATE issues SET title = ? WHERE id = ?", "durable working-only", durableID)
	te.exec(t, ctx, "UPDATE events SET comment = ? WHERE issue_id = ? AND event_type = ?",
		"durable working-only", durableID, string(types.EventCreated))
	te.exec(t, ctx, "UPDATE labels SET label = ? WHERE issue_id = ? AND label = ?",
		workingLabel, durableID, baselineLabel)

	if err := te.store.RunInIssueLifecycleTransaction(ctx, "test: mutate standalone wisp", func(tx storage.IssueLifecycleTransaction) error {
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

	after, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit after: %v", err)
	}
	if after != before {
		t.Errorf("standalone wisp mutation changed HEAD from %s to %s", before, after)
	}

	wisp, err := te.store.GetIssue(ctx, wispID)
	if err != nil {
		t.Fatalf("GetIssue wisp: %v", err)
	}
	if wisp.Title != "wisp updated" {
		t.Errorf("wisp title = %q, want %q", wisp.Title, "wisp updated")
	}
	labels, err := te.store.GetLabels(ctx, wispID)
	if err != nil {
		t.Fatalf("GetLabels wisp: %v", err)
	}
	if len(labels) != 1 || labels[0] != addWispLabel {
		t.Errorf("wisp labels = %v, want [%s]", labels, addWispLabel)
	}
	for _, event := range []struct {
		eventType types.EventType
		comment   string
	}{
		{eventType: types.EventUpdated},
		{eventType: types.EventLabelRemoved, comment: "Removed label: " + removeWispLabel},
		{eventType: types.EventLabelAdded, comment: "Added label: " + addWispLabel},
	} {
		var count int
		if event.comment == "" {
			te.queryScalar(t, ctx,
				"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ? AND event_type = ?",
				[]any{wispID, string(event.eventType)}, &count)
		} else {
			te.queryScalar(t, ctx,
				"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ? AND event_type = ? AND comment = ?",
				[]any{wispID, string(event.eventType), event.comment}, &count)
		}
		if count != 1 {
			t.Errorf("wisp event %s/%q count = %d, want 1", event.eventType, event.comment, count)
		}
	}

	var workingTitle, headTitle string
	te.queryScalar(t, ctx, "SELECT title FROM issues WHERE id = ?", []any{durableID}, &workingTitle)
	te.queryScalar(t, ctx, "SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", []any{durableID}, &headTitle)
	if workingTitle != "durable working-only" || headTitle != "durable baseline" {
		t.Errorf("durable issue titles = working:%q HEAD:%q", workingTitle, headTitle)
	}

	// events is dolt_ignored since migration 0062 (bd-red8u) and has no HEAD
	// state on this production-shaped database (see the same-status test above
	// and TestEmbeddedMigration0062FlipsEventsToIgnoredPlane). Publication is
	// covered by the HEAD-unchanged assertion; the durable label check below
	// still exercises a versioned side table at HEAD.
	var workingEvent int
	te.queryScalar(t, ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ? AND comment = ?",
		[]any{durableID, string(types.EventCreated), "durable working-only"}, &workingEvent)
	if workingEvent != 1 {
		t.Errorf("durable working-set event edit count = %d, want 1", workingEvent)
	}

	var workingLabelCount, headLabelCount int
	te.queryScalar(t, ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?",
		[]any{durableID, workingLabel}, &workingLabelCount)
	te.queryScalar(t, ctx,
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ? AND label = ?",
		[]any{durableID, workingLabel}, &headLabelCount)
	if workingLabelCount != 1 || headLabelCount != 0 {
		t.Errorf("durable label edit counts = working:%d HEAD:%d", workingLabelCount, headLabelCount)
	}
}

func seedEmbeddedTransactionWispBlocker(t *testing.T, te *testEnv, ctx context.Context, dependerID, wispID string) {
	t.Helper()
	if err := te.store.CreateIssue(ctx, &types.Issue{ID: dependerID, Title: dependerID, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
		t.Fatalf("CreateIssue durable: %v", err)
	}
	if err := te.store.CreateIssue(ctx, &types.Issue{ID: wispID, Title: wispID, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: dependerID, DependsOnID: wispID, Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	var blocked bool
	te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{dependerID}, &blocked)
	if !blocked {
		t.Fatal("seed durable depender is not blocked")
	}
}
