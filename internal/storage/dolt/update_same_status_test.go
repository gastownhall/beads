package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestUpdateIssueSameStatusDoesNotCreateLifecycleMutation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "same-status-no-lifecycle-mutation"
	createPerm(t, ctx, store, id)
	before, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents, err := store.GetEvents(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateIssue(ctx, id, map[string]interface{}{"status": types.StatusOpen}, "tester"); err != nil {
		t.Fatalf("same-status UpdateIssue: %v", err)
	}
	afterSameStatus, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	afterSameStatusEvents, err := store.GetEvents(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if afterSameStatus.RowVersion != before.RowVersion {
		t.Errorf("same-status update changed RowVersion from %d to %d", before.RowVersion, afterSameStatus.RowVersion)
	}
	if len(afterSameStatusEvents) != len(beforeEvents) {
		t.Errorf("same-status update recorded %d events, want %d", len(afterSameStatusEvents), len(beforeEvents))
	}

	if err := store.UpdateIssue(ctx, id, map[string]interface{}{"status": types.StatusOpen, "title": "renamed"}, "tester"); err != nil {
		t.Fatalf("same-status scalar UpdateIssue: %v", err)
	}
	afterScalar, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if afterScalar.Title != "renamed" {
		t.Errorf("mixed scalar update title = %q, want renamed", afterScalar.Title)
	}
	events, err := store.GetEvents(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var updated, statusChanged int
	for _, event := range events {
		switch event.EventType {
		case types.EventUpdated:
			updated++
		case types.EventStatusChanged:
			statusChanged++
		}
	}
	if len(events) != len(beforeEvents)+1 || updated != 1 || statusChanged != 0 {
		t.Errorf("mixed scalar same-status events = updated:%d status_changed:%d total:%d", updated, statusChanged, len(events))
	}
}

func TestDoltStoreSameStatusUpdatesDoNotPublishUnrelatedDurableChanges(t *testing.T) {
	for _, test := range []struct {
		name    string
		checked bool
	}{
		{name: "UpdateIssue"},
		{name: "UpdateIssueChecked", checked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()

			targetID := "store-same-status-target-" + test.name
			dirtyID := "store-same-status-dirty-" + test.name
			createPerm(t, ctx, store, targetID)
			createPerm(t, ctx, store, dirtyID)
			targetBefore, err := store.GetIssue(ctx, targetID)
			if err != nil {
				t.Fatalf("GetIssue target before: %v", err)
			}
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

			updates := map[string]interface{}{"status": types.StatusOpen}
			if test.checked {
				version := targetBefore.RowVersion
				err = store.UpdateIssueChecked(ctx, targetID, updates, "tester", storage.UpdateIssueOptions{ExpectedVersion: &version})
			} else {
				err = store.UpdateIssue(ctx, targetID, updates, "tester")
			}
			if err != nil {
				t.Fatalf("%s: %v", test.name, err)
			}
			if after := transactionWispLifecycleHead(t, ctx, store); after != before {
				t.Errorf("%s same-status update changed HEAD from %s to %s", test.name, before, after)
			}
			targetAfter, err := store.GetIssue(ctx, targetID)
			if err != nil {
				t.Fatalf("GetIssue target after: %v", err)
			}
			if targetAfter.RowVersion != targetBefore.RowVersion {
				t.Errorf("%s same-status update changed RowVersion from %d to %d",
					test.name, targetBefore.RowVersion, targetAfter.RowVersion)
			}
			assertDirectSameStatusUnrelatedChangesWorkingOnly(t, ctx, store, dirtyID)
		})
	}
}

func TestUpdateIssuePreservesCallerUpdatesAfterSameStatusNormalization(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "same-status-preserves-caller-map"
	createPerm(t, ctx, store, id)
	updates := map[string]interface{}{"status": types.StatusOpen}

	if err := store.UpdateIssue(ctx, id, updates, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if got, ok := updates["status"]; !ok || got != types.StatusOpen {
		t.Fatalf("caller updates after same-status update = %#v, want status=open", updates)
	}
}
