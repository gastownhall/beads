package dolt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Closing a wisp can recompute durable dependers. The recompute and the wisp
// close must share the SQL transaction that is staged: otherwise the durable
// UPDATE can land in the working set after DOLT_COMMIT and never reach HEAD.
func TestRunInTransactionCheckedCloseWispStagesDurableRecompute(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	depender := crossTierRegularIssue("tx-close-wisp-durable-depender", "durable depender")
	blocker := crossTierWispIssue("tx-close-wisp-blocker", "wisp blocker")
	if err := store.CreateIssue(ctx, depender, "tester"); err != nil {
		t.Fatalf("CreateIssue durable depender: %v", err)
	}
	if err := store.CreateIssue(ctx, blocker, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp blocker: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: depender.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency durable -> wisp: %v", err)
	}

	assertCrossTierIsBlocked(ctx, t, store.db, "issues", depender.ID, true)
	var headBlocked bool
	if err := store.db.QueryRowContext(ctx,
		"SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", depender.ID,
	).Scan(&headBlocked); err != nil {
		t.Fatalf("read depender from HEAD before close: %v", err)
	}
	if !headBlocked {
		t.Fatal("durable depender is not blocked in HEAD before close")
	}

	if err := store.RunInTransaction(ctx, "test: checked-close wisp and publish durable recompute", func(tx storage.Transaction) error {
		_, err := tx.CloseIssueChecked(ctx, blocker.ID, "tester", storage.CloseIssueOptions{Reason: "done"})
		return err
	}); err != nil {
		t.Fatalf("RunInTransaction checked-close wisp: %v", err)
	}

	assertCrossTierIsBlocked(ctx, t, store.db, "issues", depender.ID, false)
	if err := store.db.QueryRowContext(ctx,
		"SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", depender.ID,
	).Scan(&headBlocked); err != nil {
		t.Fatalf("read depender from HEAD after close: %v", err)
	}
	if headBlocked {
		t.Fatal("durable depender recompute remained outside HEAD after checked-close")
	}
	var dirtyIssues int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'issues'",
	).Scan(&dirtyIssues); err != nil {
		t.Fatalf("read issues working-set status: %v", err)
	}
	if dirtyIssues != 0 {
		t.Fatalf("dolt_status contains %d dirty issues row(s) after checked-close, want 0", dirtyIssues)
	}
}

// A checked close must evaluate policy against writes made earlier in the same
// callback even when the dependency row and target issue live in different
// storage planes.
func TestRunInTransactionCheckedCloseSeesSameCallbackCrossTierChild(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	parent := crossTierWispIssue("tx-close-wisp-parent", "wisp parent")
	child := crossTierRegularIssue("tx-close-durable-child", "durable child")
	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp parent: %v", err)
	}
	if err := store.CreateIssue(ctx, child, "tester"); err != nil {
		t.Fatalf("CreateIssue durable child: %v", err)
	}

	err := store.RunInTransaction(ctx, "test: add durable child then checked-close wisp parent", func(tx storage.Transaction) error {
		if err := tx.AddDependency(ctx, &types.Dependency{
			IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild,
		}, "tester"); err != nil {
			return err
		}
		_, err := tx.CloseIssueChecked(ctx, parent.ID, "tester", storage.CloseIssueOptions{Reason: "done"})
		return err
	})
	if !errors.Is(err, storage.ErrCloseOpenChildren) {
		t.Fatalf("RunInTransaction error = %v, want ErrCloseOpenChildren", err)
	}

	var status types.Status
	if err := store.db.QueryRowContext(ctx, "SELECT status FROM wisps WHERE id = ?", parent.ID).Scan(&status); err != nil {
		t.Fatalf("read wisp parent after refusal: %v", err)
	}
	if status != types.StatusOpen {
		t.Fatalf("wisp parent status = %q after refusal, want open", status)
	}
	deps, err := store.GetDependencyRecords(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords after rollback: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("dependency rows after checked-close refusal = %+v, want none", deps)
	}
}

// The symmetric child plane must be visible too: a wisp child edge written
// earlier in the callback participates in the parent's close policy.
func TestRunInTransactionCheckedCloseSeesSameCallbackWispChild(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	parent := crossTierWispIssue("tx-close-wisp-parent-from-wisp", "wisp parent")
	child := crossTierWispIssue("tx-close-wisp-child", "wisp child")
	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp parent: %v", err)
	}
	if err := store.CreateIssue(ctx, child, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp child: %v", err)
	}

	err := store.RunInTransaction(ctx, "test: add wisp child then checked-close wisp parent", func(tx storage.Transaction) error {
		if err := tx.AddDependency(ctx, &types.Dependency{
			IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild,
		}, "tester"); err != nil {
			return err
		}
		_, err := tx.CloseIssueChecked(ctx, parent.ID, "tester", storage.CloseIssueOptions{Reason: "done"})
		return err
	})
	if !errors.Is(err, storage.ErrCloseOpenChildren) {
		t.Fatalf("RunInTransaction error = %v, want ErrCloseOpenChildren", err)
	}

	var status types.Status
	if err := store.db.QueryRowContext(ctx, "SELECT status FROM wisps WHERE id = ?", parent.ID).Scan(&status); err != nil {
		t.Fatalf("read wisp parent after refusal: %v", err)
	}
	if status != types.StatusOpen {
		t.Fatalf("wisp parent status = %q after refusal, want open", status)
	}
	deps, err := store.GetDependencyRecords(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords after rollback: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("dependency rows after checked-close refusal = %+v, want none", deps)
	}
}

// A staged child that is already closed is not an open-child refusal. The
// edge's coordination touch and the close share the callback transaction.
func TestRunInTransactionCheckedCloseAllowsSameCallbackClosedWispChild(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	parent := crossTierWispIssue("tx-close-wisp-parent-from-closed-wisp", "wisp parent")
	child := crossTierWispIssue("tx-close-closed-wisp-child", "closed wisp child")
	child.Status = types.StatusClosed
	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp parent: %v", err)
	}
	if err := store.CreateIssue(ctx, child, "tester"); err != nil {
		t.Fatalf("CreateIssue closed wisp child: %v", err)
	}

	if err := store.RunInTransaction(ctx, "test: add closed wisp child then checked-close wisp parent", func(tx storage.Transaction) error {
		if err := tx.AddDependency(ctx, &types.Dependency{
			IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild,
		}, "tester"); err != nil {
			return err
		}
		result, err := tx.CloseIssueChecked(ctx, parent.ID, "tester", storage.CloseIssueOptions{Reason: "done"})
		if err == nil && result.OpenChildren != 0 {
			return fmt.Errorf("checked-close OpenChildren = %d, want 0", result.OpenChildren)
		}
		return err
	}); err != nil {
		t.Fatalf("RunInTransaction checked-close with staged closed wisp child: %v", err)
	}

	var status types.Status
	if err := store.db.QueryRowContext(ctx, "SELECT status FROM wisps WHERE id = ?", parent.ID).Scan(&status); err != nil {
		t.Fatalf("read wisp parent after close: %v", err)
	}
	if status != types.StatusClosed {
		t.Fatalf("wisp parent status = %q, want closed", status)
	}
	deps, err := store.GetDependencyRecords(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords after commit: %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != parent.ID || deps[0].Type != types.DepParentChild {
		t.Fatalf("committed dependency rows = %+v, want closed child -> parent", deps)
	}
}

// A wisp's blocker edge and derived is_blocked flag written earlier in the
// callback must participate in its close policy.
func TestRunInTransactionCheckedCloseSeesSameCallbackWispBlocker(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	target := crossTierWispIssue("tx-close-wisp-blocked", "wisp target")
	blocker := crossTierRegularIssue("tx-close-wisp-live-blocker", "durable blocker")
	if err := store.CreateIssue(ctx, target, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp target: %v", err)
	}
	if err := store.CreateIssue(ctx, blocker, "tester"); err != nil {
		t.Fatalf("CreateIssue durable blocker: %v", err)
	}

	err := store.RunInTransaction(ctx, "test: block then checked-close wisp", func(tx storage.Transaction) error {
		if err := tx.AddDependency(ctx, &types.Dependency{
			IssueID: target.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
		}, "tester"); err != nil {
			return err
		}
		_, err := tx.CloseIssueChecked(ctx, target.ID, "tester", storage.CloseIssueOptions{Reason: "done"})
		return err
	})
	if !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("RunInTransaction error = %v, want ErrCloseBlocked", err)
	}

	var status types.Status
	if err := store.db.QueryRowContext(ctx, "SELECT status FROM wisps WHERE id = ?", target.ID).Scan(&status); err != nil {
		t.Fatalf("read wisp target after refusal: %v", err)
	}
	if status != types.StatusOpen {
		t.Fatalf("wisp target status = %q after refusal, want open", status)
	}
	deps, err := store.GetDependencyRecords(ctx, target.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords after rollback: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("dependency rows after checked-close refusal = %+v, want none", deps)
	}
}

// Every transaction read and mutation surface resolves a dual-resident ID to
// its wisp copy, except SearchIssues, which deliberately hydrates from the one
// table selected by its filter. This anomalous state is a compatibility gate:
// no method may silently switch aggregates after the facade resolves a plane.
func TestRunInTransactionPrefersWispForDualResidentID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	const (
		id        = "tx-dual-resident-routing"
		blockerID = "tx-dual-resident-durable-blocker"
	)
	createPerm(t, ctx, store, id)
	createPerm(t, ctx, store, blockerID)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: id, DependsOnID: blockerID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("seed durable twin blocker: %v", err)
	}
	if err := store.Commit(ctx, "test: seed dual-resident durable twin"); err != nil {
		t.Fatalf("commit durable twin seed: %v", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx for wisp twin: %v", err)
	}
	if err := issueops.InsertIssueStrictInTx(ctx, tx, "wisps", &types.Issue{
		ID: id, Title: "wisp canonical", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Ephemeral: true,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed wisp twin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit wisp twin: %v", err)
	}

	var durableBefore, wispBefore int64
	var durableTitleBefore string
	var durableBlockedBefore bool
	if err := store.db.QueryRowContext(ctx,
		"SELECT title, row_lock, is_blocked FROM issues WHERE id = ?", id,
	).Scan(&durableTitleBefore, &durableBefore, &durableBlockedBefore); err != nil {
		t.Fatalf("read durable revision before transaction: %v", err)
	}
	if !durableBlockedBefore {
		t.Fatal("durable twin seed is_blocked = false, want true")
	}
	if err := store.db.QueryRowContext(ctx, "SELECT row_lock FROM wisps WHERE id = ?", id).Scan(&wispBefore); err != nil {
		t.Fatalf("read wisp revision before transaction: %v", err)
	}

	if err := store.RunInTransaction(ctx, "test: dual-resident routing", func(tx storage.Transaction) error {
		before, err := tx.GetIssue(ctx, id)
		if err != nil {
			return err
		}
		if before.Title != "wisp canonical" || before.RowVersion != wispBefore {
			return fmt.Errorf("GetIssue resolved title %q revision %d, want wisp revision %d", before.Title, before.RowVersion, wispBefore)
		}
		blocked, blockers, err := tx.IsBlocked(ctx, id)
		if err != nil {
			return err
		}
		if blocked || len(blockers) != 0 {
			return fmt.Errorf("IsBlocked resolved durable twin: blocked=%t blockers=%v, want false/none", blocked, blockers)
		}
		batch, err := tx.IsBlockedBatch(ctx, []string{id, blockerID})
		if err != nil {
			return err
		}
		if batch[id] {
			return fmt.Errorf("IsBlockedBatch[%s] = true, want wisp twin false", id)
		}
		search, err := tx.SearchIssues(ctx, durableTitleBefore, types.IssueFilter{IDs: []string{id}})
		if err != nil {
			return err
		}
		if len(search) != 1 || search[0].Title != durableTitleBefore || search[0].Ephemeral {
			return fmt.Errorf("durable SearchIssues hydration = %+v, want durable matching twin", search)
		}
		if err := tx.UpdateIssue(ctx, id, map[string]interface{}{"title": "wisp updated"}, "tester"); err != nil {
			return err
		}
		if err := tx.TouchIssue(ctx, id, "tester"); err != nil {
			return err
		}
		afterTouch, err := tx.GetIssue(ctx, id)
		if err != nil {
			return err
		}
		if afterTouch.RowVersion == wispBefore {
			return fmt.Errorf("TouchIssue left the transaction's canonical wisp revision unchanged at %d", wispBefore)
		}
		expected := afterTouch.RowVersion
		_, err = tx.CloseIssueChecked(ctx, id, "tester", storage.CloseIssueOptions{
			Reason: "done", ExpectedVersion: &expected,
		})
		return err
	}); err != nil {
		t.Fatalf("RunInTransaction dual-resident routing: %v", err)
	}

	var durableAfter, wispAfter int64
	var durableTitleAfter, wispTitleAfter string
	var durableBlockedAfter, wispBlockedAfter bool
	var durableStatus, wispStatus types.Status
	if err := store.db.QueryRowContext(ctx,
		"SELECT title, row_lock, status, is_blocked FROM issues WHERE id = ?", id,
	).Scan(&durableTitleAfter, &durableAfter, &durableStatus, &durableBlockedAfter); err != nil {
		t.Fatalf("read durable twin after transaction: %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT title, row_lock, status, is_blocked FROM wisps WHERE id = ?", id,
	).Scan(&wispTitleAfter, &wispAfter, &wispStatus, &wispBlockedAfter); err != nil {
		t.Fatalf("read wisp twin after transaction: %v", err)
	}
	if durableTitleAfter != durableTitleBefore || durableAfter != durableBefore || durableStatus != types.StatusOpen || !durableBlockedAfter {
		t.Fatalf("durable twin changed: title/revision/status/blocked = %q/%d/%q/%t, want %q/%d/open/true",
			durableTitleAfter, durableAfter, durableStatus, durableBlockedAfter, durableTitleBefore, durableBefore)
	}
	if wispTitleAfter != "wisp updated" || wispAfter == wispBefore || wispStatus != types.StatusClosed || wispBlockedAfter {
		t.Fatalf("wisp twin title/revision/status/blocked = %q/%d/%q/%t, want updated/changed from %d/closed/false",
			wispTitleAfter, wispAfter, wispStatus, wispBlockedAfter, wispBefore)
	}
}

// This is the exact journal-off failure that motivated the unified callback
// transaction. The dependency temporarily blocks a durable row, then closing
// its wisp target must settle that row before the durable revision is built.
func TestRunInTransactionTouchAddDurableDependencyThenCloseWispSettlesHead(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	depender := crossTierRegularIssue("tx-touch-add-close-durable", "durable depender")
	blocker := crossTierWispIssue("tx-touch-add-close-wisp", "wisp blocker")
	if err := store.CreateIssue(ctx, depender, "tester"); err != nil {
		t.Fatalf("CreateIssue durable depender: %v", err)
	}
	if err := store.CreateIssue(ctx, blocker, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp blocker: %v", err)
	}

	if err := store.RunInTransaction(ctx, "test: touch wisp, add durable blocker edge, close wisp", func(tx storage.Transaction) error {
		if err := tx.TouchIssue(ctx, blocker.ID, "tester"); err != nil {
			return err
		}
		if err := tx.AddDependency(ctx, &types.Dependency{
			IssueID: depender.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
		}, "tester"); err != nil {
			return err
		}
		_, err := tx.CloseIssueChecked(ctx, blocker.ID, "tester", storage.CloseIssueOptions{Reason: "done"})
		return err
	}); err != nil {
		t.Fatalf("RunInTransaction touch/add/close: %v", err)
	}

	var wispStatus types.Status
	if err := store.db.QueryRowContext(ctx, "SELECT status FROM wisps WHERE id = ?", blocker.ID).Scan(&wispStatus); err != nil {
		t.Fatalf("read closed wisp: %v", err)
	}
	if wispStatus != types.StatusClosed {
		t.Fatalf("wisp status = %q, want closed", wispStatus)
	}
	assertCrossTierIsBlocked(ctx, t, store.db, "issues", depender.ID, false)
	var headBlocked bool
	if err := store.db.QueryRowContext(ctx,
		"SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", depender.ID,
	).Scan(&headBlocked); err != nil {
		t.Fatalf("read settled durable depender AS OF HEAD: %v", err)
	}
	if headBlocked {
		t.Fatal("durable depender remained blocked in HEAD after its wisp target closed")
	}
	var headEdgeCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dependencies AS OF 'HEAD'
		WHERE issue_id = ? AND depends_on_wisp_id = ? AND type = 'blocks'
	`, depender.ID, blocker.ID).Scan(&headEdgeCount); err != nil {
		t.Fatalf("read durable dependency AS OF HEAD: %v", err)
	}
	if headEdgeCount != 1 {
		t.Fatalf("durable dependency count AS OF HEAD = %d, want 1", headEdgeCount)
	}
}

// Removing a committed wisp child edge must be visible to checked-close in
// the same callback. A second snapshot used to re-read the stale edge and
// refuse with ErrCloseOpenChildren.
func TestRunInTransactionRemoveWispChildThenCloseParentSeesRemoval(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	parent := crossTierWispIssue("tx-remove-child-parent", "wisp parent")
	child := crossTierWispIssue("tx-remove-child-child", "wisp child")
	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}
	if err := store.CreateIssue(ctx, child, "tester"); err != nil {
		t.Fatalf("CreateIssue child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("seed wisp child edge: %v", err)
	}
	headBefore, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit before ignored-only callback: %v", err)
	}

	if err := store.RunInTransaction(ctx, "test: remove wisp child then close parent", func(tx storage.Transaction) error {
		if err := tx.RemoveDependency(ctx, child.ID, parent.ID, "tester"); err != nil {
			return err
		}
		_, err := tx.CloseIssueChecked(ctx, parent.ID, "tester", storage.CloseIssueOptions{Reason: "done"})
		return err
	}); err != nil {
		t.Fatalf("RunInTransaction remove child then close: %v", err)
	}

	var status types.Status
	if err := store.db.QueryRowContext(ctx, "SELECT status FROM wisps WHERE id = ?", parent.ID).Scan(&status); err != nil {
		t.Fatalf("read wisp parent: %v", err)
	}
	if status != types.StatusClosed {
		t.Fatalf("wisp parent status = %q, want closed", status)
	}
	var edgeCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND depends_on_wisp_id = ?",
		child.ID, parent.ID,
	).Scan(&edgeCount); err != nil {
		t.Fatalf("read removed wisp child edge: %v", err)
	}
	if edgeCount != 0 {
		t.Fatalf("wisp child edge count = %d, want 0", edgeCount)
	}
	headAfter, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit after ignored-only callback: %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("ignored-only callback changed HEAD from %s to %s", headBefore, headAfter)
	}
}

// Wisp-source dependency mutations can change inherited blocked state on a
// durable child. The transaction must stage issues even though the explicit
// edge lives only in the ignored wisp dependency table.
func TestRunInTransactionWispDependencyMutationPublishesDurableDescendant(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	parent := crossTierWispIssue("tx-wisp-dep-publish-parent", "wisp parent")
	child := crossTierRegularIssue("tx-wisp-dep-publish-child", "durable child")
	blocker := crossTierRegularIssue("tx-wisp-dep-publish-blocker", "durable blocker")
	for _, issue := range []*types.Issue{parent, child, blocker} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("seed durable child -> wisp parent: %v", err)
	}
	if err := store.Commit(ctx, "test: seed cross-plane hierarchy"); err != nil {
		t.Fatalf("commit hierarchy seed: %v", err)
	}

	if err := store.RunInTransaction(ctx, "test: block wisp parent and publish durable child", func(tx storage.Transaction) error {
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID: parent.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
		}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction add wisp blocker: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, child.ID, true)

	if err := store.RunInTransaction(ctx, "test: unblock wisp parent and publish durable child", func(tx storage.Transaction) error {
		return tx.RemoveDependency(ctx, parent.ID, blocker.ID, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction remove wisp blocker: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, child.ID, false)
}

// The batch-create path is also an upsert/import path. Updating an existing
// wisp there can add a blocking edge and recompute durable descendants, so the
// facade must consume the wisp batch's changed-table result instead of assuming
// every reported side effect belongs to a Dolt-ignored table.
func TestRunInTransactionWispBatchCreatePublishesDurableDerivedRows(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	parent := crossTierWispIssue("tx-wisp-batch-publish-parent", "wisp parent")
	child := crossTierRegularIssue("tx-wisp-batch-publish-child", "durable child")
	blocker := crossTierRegularIssue("tx-wisp-batch-publish-blocker", "durable blocker")
	for _, issue := range []*types.Issue{parent, child, blocker} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("seed durable child -> wisp parent: %v", err)
	}
	if err := store.Commit(ctx, "test: seed wisp batch hierarchy"); err != nil {
		t.Fatalf("commit wisp batch hierarchy: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, child.ID, false)

	upsert := crossTierWispIssue(parent.ID, "wisp parent with blocker")
	upsert.Dependencies = []*types.Dependency{{
		IssueID: parent.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}}
	if err := store.RunInTransaction(ctx, "test: wisp batch create publishes durable child", func(tx storage.Transaction) error {
		return tx.CreateIssues(ctx, []*types.Issue{upsert}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction wisp batch upsert: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, child.ID, true)
}

// Deleting a wisp removes durable inbound edges and can unblock their durable
// sources. Both the edge deletion and every derived issue row belong in HEAD.
func TestRunInTransactionDeleteWispPublishesDurableDerivedRows(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	depender := crossTierRegularIssue("tx-delete-wisp-depender", "durable depender")
	blocker := crossTierWispIssue("tx-delete-wisp-blocker", "wisp blocker")
	if err := store.CreateIssue(ctx, depender, "tester"); err != nil {
		t.Fatalf("CreateIssue depender: %v", err)
	}
	if err := store.CreateIssue(ctx, blocker, "tester"); err != nil {
		t.Fatalf("CreateIssue blocker: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: depender.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("seed durable blocker edge: %v", err)
	}
	if err := store.Commit(ctx, "test: seed delete-wisp graph"); err != nil {
		t.Fatalf("commit delete-wisp seed: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, depender.ID, true)

	if err := store.RunInTransaction(ctx, "test: delete wisp blocker", func(tx storage.Transaction) error {
		return tx.DeleteIssue(ctx, blocker.ID)
	}); err != nil {
		t.Fatalf("RunInTransaction delete wisp: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, depender.ID, false)
	var workingWispCount, headEdgeCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", blocker.ID).Scan(&workingWispCount); err != nil {
		t.Fatalf("read deleted wisp: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dependencies AS OF 'HEAD'
		WHERE issue_id = ? AND depends_on_wisp_id = ?
	`, depender.ID, blocker.ID).Scan(&headEdgeCount); err != nil {
		t.Fatalf("read deleted durable edge AS OF HEAD: %v", err)
	}
	if workingWispCount != 0 || headEdgeCount != 0 {
		t.Fatalf("post-delete counts = wisp:%d durable edge in HEAD:%d, want 0/0", workingWispCount, headEdgeCount)
	}
}

// A generic wisp status update is also a lifecycle transition. When it closes
// a blocker, the transaction must publish its durable dependers.
func TestRunInTransactionUpdateWispStatusPublishesDurableDerivedRows(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	depender := crossTierRegularIssue("tx-update-wisp-depender", "durable depender")
	blocker := crossTierWispIssue("tx-update-wisp-blocker", "wisp blocker")
	if err := store.CreateIssue(ctx, depender, "tester"); err != nil {
		t.Fatalf("CreateIssue depender: %v", err)
	}
	if err := store.CreateIssue(ctx, blocker, "tester"); err != nil {
		t.Fatalf("CreateIssue blocker: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: depender.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("seed blocker edge: %v", err)
	}
	if err := store.Commit(ctx, "test: seed update-wisp graph"); err != nil {
		t.Fatalf("commit update-wisp seed: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, depender.ID, true)

	if err := store.RunInTransaction(ctx, "test: update wisp status to closed", func(tx storage.Transaction) error {
		return tx.UpdateIssue(ctx, blocker.ID, map[string]interface{}{"status": types.StatusClosed}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction update wisp status: %v", err)
	}
	assertBlockedWorkingAndHead(ctx, t, store, depender.ID, false)
}

// A caller may catch a policy refusal and commit an independent sibling write.
// The refused UpdateIssue must roll back its coordination touch to a savepoint.
func TestRunInTransactionCaughtUpdateCloseRefusalRestoresCoordination(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	store.SetEventsJournalEnabled(false)

	ctx, cancel := testContext(t)
	defer cancel()

	const (
		parent  = "tx-update-savepoint-parent"
		child   = "tx-update-savepoint-child"
		sibling = "tx-update-savepoint-sibling"
	)
	for _, id := range []string{parent, child, sibling} {
		createPerm(t, ctx, store, id)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("seed child edge: %v", err)
	}
	before := readDependencyCoordination(ctx, t, store)

	if err := store.RunInTransaction(ctx, "test: catch update close refusal and commit sibling", func(tx storage.Transaction) error {
		err := tx.UpdateIssue(ctx, parent, map[string]interface{}{"status": types.StatusClosed}, "tester")
		if !errors.Is(err, storage.ErrCloseOpenChildren) {
			return fmt.Errorf("UpdateIssue refusal = %v, want ErrCloseOpenChildren", err)
		}
		return tx.AddLabel(ctx, sibling, "survives-refusal", "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction caught update refusal: %v", err)
	}

	issue, err := store.GetIssue(ctx, parent)
	if err != nil {
		t.Fatalf("GetIssue parent: %v", err)
	}
	if issue.Status != types.StatusOpen {
		t.Fatalf("refused parent status = %q, want open", issue.Status)
	}
	var headLabelCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ? AND label = ?",
		sibling, "survives-refusal",
	).Scan(&headLabelCount); err != nil {
		t.Fatalf("read sibling label AS OF HEAD: %v", err)
	}
	if headLabelCount != 1 {
		t.Fatalf("sibling label count AS OF HEAD = %d, want 1", headLabelCount)
	}
	after := readDependencyCoordination(ctx, t, store)
	if !equalStringMap(after, before) {
		t.Fatalf("coordination rows changed across caught refusal: before=%v after=%v", before, after)
	}
}

// Plane-aware journal capture must snapshot the wisp selected by the
// transaction facade, never the durable twin sharing its ID.
func TestRunInTransactionDualResidentJournalSnapshotsSelectedWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const (
		id       = "tx-dual-journal"
		targetID = "tx-dual-journal-target"
	)
	createPerm(t, ctx, store, id)
	createPerm(t, ctx, store, targetID)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx for wisp twin: %v", err)
	}
	if err := issueops.InsertIssueStrictInTx(ctx, tx, "wisps", &types.Issue{
		ID: id, Title: "journal wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Ephemeral: true,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed journal wisp twin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit journal wisp twin: %v", err)
	}
	enableJournalForTest(t, store)
	clearJournal(t, store)

	if err := store.RunInTransaction(ctx, "test: dual-resident plane-aware journal", func(tx storage.Transaction) error {
		if err := tx.UpdateIssue(ctx, id, map[string]interface{}{"title": "journal wisp updated"}, "tester"); err != nil {
			return err
		}
		if err := tx.AddLabel(ctx, id, "wisp-label", "tester"); err != nil {
			return err
		}
		if err := tx.AddComment(ctx, id, "tester", "wisp comment"); err != nil {
			return err
		}
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID: id, DependsOnID: targetID, Type: types.DepRelated,
		}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction dual journal: %v", err)
	}

	rows, err := store.db.QueryContext(ctx,
		"SELECT op, issue_json FROM bd_events_journal WHERE issue_id = ? ORDER BY seq", id)
	if err != nil {
		t.Fatalf("query dual journal rows: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var op string
		var raw []byte
		if err := rows.Scan(&op, &raw); err != nil {
			t.Fatalf("scan dual journal row: %v", err)
		}
		var snapshot types.Issue
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatalf("decode %s journal snapshot: %v", op, err)
		}
		if snapshot.Title != "journal wisp updated" || !snapshot.Ephemeral {
			t.Fatalf("%s journal snapshot selected durable twin: title=%q ephemeral=%t", op, snapshot.Title, snapshot.Ephemeral)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dual journal rows: %v", err)
	}
	if count != 4 {
		t.Fatalf("dual journal row count = %d, want 4 (update, label, comment, dep_add)", count)
	}
}

// Deleting the wisp selected for a dual-resident ID must not journal edges
// owned by the durable twin. Because the journal has one logical ID namespace,
// the surviving durable twin resurfaces as an update snapshot rather than a
// delete followed by an implicit, unreported replacement.
func TestRunInTransactionDeleteDualResidentWispJournalsResurfacedDurableTwin(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const (
		id                = "tx-dual-delete-journal"
		durableTargetID   = "tx-dual-delete-durable-target"
		wispTargetID      = "tx-dual-delete-wisp-target"
		durableTwinTitle  = "durable twin survives"
		selectedWispTitle = "selected wisp is deleted"
	)
	for _, issue := range []*types.Issue{
		{ID: id, Title: durableTwinTitle, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: durableTargetID, Title: "durable edge target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: wispTargetID, Title: "wisp edge target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: id, DependsOnID: durableTargetID, Type: types.DepRelated,
	}, "tester"); err != nil {
		t.Fatalf("seed durable twin edge: %v", err)
	}

	seedTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx for wisp twin: %v", err)
	}
	if err := issueops.InsertIssueStrictInTx(ctx, seedTx, "wisps", &types.Issue{
		ID: id, Title: selectedWispTitle, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Ephemeral: true,
	}); err != nil {
		_ = seedTx.Rollback()
		t.Fatalf("seed delete wisp twin: %v", err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatalf("commit delete wisp twin: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: id, DependsOnID: wispTargetID, Type: types.DepRelated,
	}, "tester"); err != nil {
		t.Fatalf("seed selected wisp edge: %v", err)
	}

	enableJournalForTest(t, store)
	clearJournal(t, store)
	if err := store.RunInTransaction(ctx, "test: delete dual-resident selected wisp", func(tx storage.Transaction) error {
		return tx.DeleteIssue(ctx, id)
	}); err != nil {
		t.Fatalf("RunInTransaction dual delete: %v", err)
	}

	got, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue resurfaced durable twin: %v", err)
	}
	if got.Title != durableTwinTitle || got.Ephemeral {
		t.Fatalf("resurfaced issue = title:%q ephemeral:%t, want durable twin %q", got.Title, got.Ephemeral, durableTwinTitle)
	}
	for _, query := range []struct {
		name string
		sql  string
		args []any
		want int
	}{
		{
			name: "selected wisp row removed",
			sql:  "SELECT COUNT(*) FROM wisps WHERE id = ?",
			args: []any{id}, want: 0,
		},
		{
			name: "selected wisp outgoing edge removed",
			sql:  "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
			args: []any{id, wispTargetID}, want: 0,
		},
		{
			name: "durable twin outgoing edge preserved in working set",
			sql:  "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
			args: []any{id, durableTargetID}, want: 1,
		},
		{
			name: "durable twin outgoing edge preserved at HEAD",
			sql:  "SELECT COUNT(*) FROM dependencies AS OF 'HEAD' WHERE issue_id = ? AND depends_on_issue_id = ?",
			args: []any{id, durableTargetID}, want: 1,
		},
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, query.sql, query.args...).Scan(&count); err != nil {
			t.Fatalf("%s: %v", query.name, err)
		}
		if count != query.want {
			t.Fatalf("%s count = %d, want %d", query.name, count, query.want)
		}
	}

	rows, err := store.db.QueryContext(ctx,
		"SELECT op, issue_json, dep_json FROM bd_events_journal WHERE issue_id = ? ORDER BY seq", id)
	if err != nil {
		t.Fatalf("query dual-delete journal rows: %v", err)
	}
	defer rows.Close()
	var depRemoves, updates, deletes int
	for rows.Next() {
		var op string
		var issueJSON, depJSON []byte
		if err := rows.Scan(&op, &issueJSON, &depJSON); err != nil {
			t.Fatalf("scan dual-delete journal row: %v", err)
		}
		switch issueops.EventOp(op) {
		case issueops.EventDepRemove:
			depRemoves++
			var dep issueops.EventDep
			if err := json.Unmarshal(depJSON, &dep); err != nil {
				t.Fatalf("decode dep_remove payload: %v", err)
			}
			if dep.Target != wispTargetID || dep.Kind != string(types.DepRelated) {
				t.Fatalf("dep_remove = %+v, want only selected wisp edge to %s", dep, wispTargetID)
			}
			var snapshot types.Issue
			if err := json.Unmarshal(issueJSON, &snapshot); err != nil {
				t.Fatalf("decode dep_remove wisp snapshot: %v", err)
			}
			if snapshot.Title != selectedWispTitle || !snapshot.Ephemeral {
				t.Fatalf("dep_remove snapshot = title:%q ephemeral:%t, want selected wisp", snapshot.Title, snapshot.Ephemeral)
			}
		case issueops.EventUpdate:
			updates++
			var snapshot types.Issue
			if err := json.Unmarshal(issueJSON, &snapshot); err != nil {
				t.Fatalf("decode resurfaced durable snapshot: %v", err)
			}
			if snapshot.Title != durableTwinTitle || snapshot.Ephemeral {
				t.Fatalf("resurfaced update snapshot = title:%q ephemeral:%t, want durable twin", snapshot.Title, snapshot.Ephemeral)
			}
		case issueops.EventDelete:
			deletes++
		default:
			t.Fatalf("unexpected dual-delete journal op %q", op)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dual-delete journal rows: %v", err)
	}
	if depRemoves != 1 || updates != 1 || deletes != 0 {
		t.Fatalf("dual-delete journal counts = dep_remove:%d update:%d delete:%d, want 1/1/0", depRemoves, updates, deletes)
	}
}

// The non-transaction facade has dedicated single and batched wisp delete
// workers. Pin the same one-ID journal contract there so those duplicated
// paths cannot regress to reporting the untouched durable twin's edges.
func TestDoltStoreDualResidentWispDeletesJournalResurfacedDurableTwin(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()
	enableJournalForTest(t, store)

	for _, tc := range []struct {
		name   string
		delete func(string) error
	}{
		{
			name: "single",
			delete: func(id string) error {
				return store.DeleteIssue(ctx, id)
			},
		},
		{
			name: "batch",
			delete: func(id string) error {
				_, err := store.DeleteIssues(ctx, []string{id}, false, true, false)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "dual-public-delete-" + tc.name
			targetID := id + "-target"
			durableTitle := "durable twin " + tc.name
			for _, issue := range []*types.Issue{
				{ID: id, Title: durableTitle, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
				{ID: targetID, Title: "durable target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			} {
				if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
					t.Fatalf("CreateIssue %s: %v", issue.ID, err)
				}
			}
			if err := store.AddDependency(ctx, &types.Dependency{
				IssueID: id, DependsOnID: targetID, Type: types.DepRelated,
			}, "tester"); err != nil {
				t.Fatalf("seed durable twin edge: %v", err)
			}
			seedTx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("BeginTx for wisp twin: %v", err)
			}
			if err := issueops.InsertIssueStrictInTx(ctx, seedTx, "wisps", &types.Issue{
				ID: id, Title: "selected wisp " + tc.name, Status: types.StatusOpen, Priority: 2,
				IssueType: types.TypeTask, Ephemeral: true,
			}); err != nil {
				_ = seedTx.Rollback()
				t.Fatalf("seed wisp twin: %v", err)
			}
			if err := seedTx.Commit(); err != nil {
				t.Fatalf("commit wisp twin: %v", err)
			}

			clearJournal(t, store)
			if err := tc.delete(id); err != nil {
				t.Fatalf("delete selected wisp: %v", err)
			}

			var wisps, durableEdges, headDurableEdges int
			if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", id).Scan(&wisps); err != nil {
				t.Fatalf("read wisp count: %v", err)
			}
			if err := store.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?", id, targetID,
			).Scan(&durableEdges); err != nil {
				t.Fatalf("read durable edge: %v", err)
			}
			if err := store.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM dependencies AS OF 'HEAD' WHERE issue_id = ? AND depends_on_issue_id = ?", id, targetID,
			).Scan(&headDurableEdges); err != nil {
				t.Fatalf("read durable edge AS OF HEAD: %v", err)
			}
			if wisps != 0 || durableEdges != 1 || headDurableEdges != 1 {
				t.Fatalf("post-delete rows = wisps:%d durable-edge:%d HEAD-edge:%d, want 0/1/1", wisps, durableEdges, headDurableEdges)
			}

			var depRemoves, updates, deletes int
			if err := store.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ? AND op = 'dep_remove'", id,
			).Scan(&depRemoves); err != nil {
				t.Fatalf("count dep_remove journal rows: %v", err)
			}
			if err := store.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ? AND op = 'update'", id,
			).Scan(&updates); err != nil {
				t.Fatalf("count update journal rows: %v", err)
			}
			if err := store.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ? AND op = 'delete'", id,
			).Scan(&deletes); err != nil {
				t.Fatalf("count delete journal rows: %v", err)
			}
			if depRemoves != 0 || updates != 1 || deletes != 0 {
				t.Fatalf("journal counts = dep_remove:%d update:%d delete:%d, want 0/1/0", depRemoves, updates, deletes)
			}
			var raw []byte
			if err := store.db.QueryRowContext(ctx,
				"SELECT issue_json FROM bd_events_journal WHERE issue_id = ? AND op = 'update'", id,
			).Scan(&raw); err != nil {
				t.Fatalf("read resurfaced update snapshot: %v", err)
			}
			var snapshot types.Issue
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatalf("decode resurfaced update snapshot: %v", err)
			}
			if snapshot.Title != durableTitle || snapshot.Ephemeral {
				t.Fatalf("resurfaced snapshot = title:%q ephemeral:%t, want durable twin %q", snapshot.Title, snapshot.Ephemeral, durableTitle)
			}
		})
	}
}

func TestDoltStoreDualResidentClaimFamilyPrefersWisp(t *testing.T) {
	for _, tc := range []struct {
		name         string
		wispStatus   types.Status
		wispAssignee string
		mutate       func(context.Context, *DoltStore, string) error
		wantStatus   types.Status
		wantAssignee string
	}{
		{
			name: "claim", wispStatus: types.StatusOpen,
			mutate: func(ctx context.Context, store *DoltStore, id string) error {
				return store.ClaimIssue(ctx, id, "worker")
			},
			wantStatus: types.StatusInProgress, wantAssignee: "worker",
		},
		{
			name: "unclaim", wispStatus: types.StatusInProgress, wispAssignee: "worker",
			mutate: func(ctx context.Context, store *DoltStore, id string) error {
				return store.UnclaimIssue(ctx, id, "worker", false)
			},
			wantStatus: types.StatusOpen,
		},
		{
			name: "conditional-unclaim", wispStatus: types.StatusInProgress, wispAssignee: "worker",
			mutate: func(ctx context.Context, store *DoltStore, id string) error {
				return store.UnclaimIssueIfAssignee(ctx, id, "releaser", "worker")
			},
			wantStatus: types.StatusOpen,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()
			enableJournalForTest(t, store)

			id := "dual-claim-family-" + tc.name
			createPerm(t, ctx, store, id)
			seedTx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("BeginTx for wisp twin: %v", err)
			}
			if err := issueops.InsertIssueStrictInTx(ctx, seedTx, "wisps", &types.Issue{
				ID: id, Title: "canonical wisp " + tc.name, Status: tc.wispStatus,
				Assignee: tc.wispAssignee, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
			}); err != nil {
				_ = seedTx.Rollback()
				t.Fatalf("seed wisp twin: %v", err)
			}
			if err := seedTx.Commit(); err != nil {
				t.Fatalf("commit wisp twin: %v", err)
			}
			clearJournal(t, store)

			if err := tc.mutate(ctx, store, id); err != nil {
				t.Fatalf("%s canonical wisp: %v", tc.name, err)
			}

			var wispAssignee, durableAssignee string
			var wispStatus, durableStatus types.Status
			if err := store.db.QueryRowContext(ctx,
				"SELECT COALESCE(assignee, ''), status FROM wisps WHERE id = ?", id,
			).Scan(&wispAssignee, &wispStatus); err != nil {
				t.Fatalf("read wisp twin: %v", err)
			}
			if err := store.db.QueryRowContext(ctx,
				"SELECT COALESCE(assignee, ''), status FROM issues WHERE id = ?", id,
			).Scan(&durableAssignee, &durableStatus); err != nil {
				t.Fatalf("read durable twin: %v", err)
			}
			if wispAssignee != tc.wantAssignee || wispStatus != tc.wantStatus ||
				durableAssignee != "" || durableStatus != types.StatusOpen {
				t.Fatalf("post-%s planes wisp=(%q,%s) durable=(%q,%s), want (%q,%s) and unchanged durable",
					tc.name, wispAssignee, wispStatus, durableAssignee, durableStatus, tc.wantAssignee, tc.wantStatus)
			}
			assertLatestJournalIssueSnapshot(t, ctx, store, id, "canonical wisp "+tc.name, true)
		})
	}
}

func TestDoltStorePromoteDualResidentUsesCanonicalWispPreimage(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	enableJournalForTest(t, store)

	const id = "dual-promote-canonical-wisp"
	createPerm(t, ctx, store, id)
	seedTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx for wisp twin: %v", err)
	}
	if err := issueops.InsertIssueStrictInTx(ctx, seedTx, "wisps", &types.Issue{
		ID: id, Title: "canonical wisp promoted", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}); err != nil {
		_ = seedTx.Rollback()
		t.Fatalf("seed wisp twin: %v", err)
	}
	if _, err := seedTx.ExecContext(ctx,
		"INSERT INTO wisp_labels (issue_id, label) VALUES (?, 'from-wisp')", id,
	); err != nil {
		_ = seedTx.Rollback()
		t.Fatalf("seed wisp label: %v", err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatalf("commit wisp twin: %v", err)
	}
	clearJournal(t, store)

	if err := store.PromoteFromEphemeral(ctx, id, "tester"); err != nil {
		t.Fatalf("PromoteFromEphemeral: %v", err)
	}

	var title string
	var ephemeral, noHistory bool
	var wisps, promotedLabels int
	if err := store.db.QueryRowContext(ctx,
		"SELECT title, ephemeral, no_history FROM issues WHERE id = ?", id,
	).Scan(&title, &ephemeral, &noHistory); err != nil {
		t.Fatalf("read promoted issue: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", id).Scan(&wisps); err != nil {
		t.Fatalf("read remaining wisp: %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = 'from-wisp'", id,
	).Scan(&promotedLabels); err != nil {
		t.Fatalf("read promoted label: %v", err)
	}
	if title != "canonical wisp promoted" || ephemeral || noHistory || wisps != 0 || promotedLabels != 1 {
		t.Fatalf("promoted aggregate = title:%q flags:%t/%t wisps:%d labels:%d", title, ephemeral, noHistory, wisps, promotedLabels)
	}
	assertLatestJournalIssueSnapshot(t, ctx, store, id, "canonical wisp promoted", false)
}

func assertLatestJournalIssueSnapshot(t *testing.T, ctx context.Context, store *DoltStore, id, wantTitle string, wantWisp bool) {
	t.Helper()
	var raw []byte
	if err := store.db.QueryRowContext(ctx,
		"SELECT issue_json FROM bd_events_journal WHERE issue_id = ? ORDER BY seq DESC LIMIT 1", id,
	).Scan(&raw); err != nil {
		t.Fatalf("read latest journal snapshot: %v", err)
	}
	var snapshot types.Issue
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("decode latest journal snapshot: %v", err)
	}
	if snapshot.Title != wantTitle || snapshot.Ephemeral != wantWisp {
		t.Fatalf("latest journal snapshot = title:%q ephemeral:%t, want %q/%t", snapshot.Title, snapshot.Ephemeral, wantTitle, wantWisp)
	}
}

func assertBlockedWorkingAndHead(ctx context.Context, t *testing.T, store *DoltStore, id string, want bool) {
	t.Helper()
	var working, head bool
	if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", id).Scan(&working); err != nil {
		t.Fatalf("read working is_blocked for %s: %v", id, err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", id).Scan(&head); err != nil {
		t.Fatalf("read HEAD is_blocked for %s: %v", id, err)
	}
	if working != want || head != want {
		t.Fatalf("is_blocked for %s = working:%t HEAD:%t, want %t/%t", id, working, head, want, want)
	}
}

func readDependencyCoordination(ctx context.Context, t *testing.T, store *DoltStore) map[string]string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, `
		SELECT `+"`key`"+`, value FROM local_metadata
		WHERE `+"`key`"+` LIKE 'dependency-coordination/%'
		ORDER BY `+"`key`"+`
	`)
	if err != nil {
		t.Fatalf("query dependency coordination: %v", err)
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatalf("scan dependency coordination: %v", err)
		}
		result[key] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dependency coordination: %v", err)
	}
	return result
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
