package dolt

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestLabelMutationsBumpUpdatedAt (GH#5442): adding or removing a label is a
// change to the issue, so it must advance issues.updated_at; --updated-after,
// stale-upsert rejection and LWW merge all read that column.
func TestLabelMutationsBumpUpdatedAt(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "lbl-ua",
		Title:     "label updated_at probe",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create: %v", err)
	}

	backdate := func() time.Time {
		t.Helper()
		old := time.Now().UTC().AddDate(0, 0, -15).Truncate(time.Second)
		if _, err := store.db.ExecContext(ctx,
			"UPDATE issues SET updated_at = ? WHERE id = ?", old, issue.ID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		return old
	}
	assertBumped := func(op string, old time.Time) {
		t.Helper()
		got, err := store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("get after %s: %v", op, err)
		}
		if !got.UpdatedAt.After(old) {
			t.Errorf("%s did not bump updated_at: still %s (backdated to %s)",
				op, got.UpdatedAt.UTC().Format(time.RFC3339), old.Format(time.RFC3339))
		}
	}

	old := backdate()
	if err := store.AddLabel(ctx, issue.ID, "alpha", "tester"); err != nil {
		t.Fatalf("add label: %v", err)
	}
	assertBumped("AddLabel", old)

	old = backdate()
	if err := store.RemoveLabel(ctx, issue.ID, "alpha", "tester"); err != nil {
		t.Fatalf("remove label: %v", err)
	}
	assertBumped("RemoveLabel", old)
}

// agedUpdatedAt is decades before any clock these tests run under, so a stamp
// that did not move cannot pass for one that did.
var agedUpdatedAt = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// TestLabelEditsCommitUpdatedAt verifies the commit half of GH#5442 on the
// history-off server store: the owning row's new updated_at must land in the
// committed HEAD for both the direct store operations and the transaction
// wrapper, leaving no unstaged issues diff behind. On unfixed main the label
// row/events change without the issues row ever being written or staged, so
// the backdated working-set diff stays forever unstaged.
func TestLabelEditsCommitUpdatedAt(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID: "lbl-commit", Title: "label commit probe",
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create: %v", err)
	}

	headUpdatedAt := func() time.Time {
		t.Helper()
		var got time.Time
		if err := store.db.QueryRowContext(ctx,
			"SELECT updated_at FROM issues AS OF 'HEAD' WHERE id = ?", issue.ID).Scan(&got); err != nil {
			t.Fatalf("read updated_at at HEAD: %v", err)
		}
		return got.UTC()
	}
	issuesDirty := func() bool {
		t.Helper()
		var dirty int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'issues'").Scan(&dirty); err != nil {
			t.Fatalf("read dolt_status: %v", err)
		}
		return dirty > 0
	}
	assertOp := func(op string) {
		t.Helper()
		if got := headUpdatedAt(); !got.After(agedUpdatedAt) {
			t.Errorf("%s: updated_at at HEAD = %v, want a stamp after %v (the label edit's timestamp write must be committed)",
				op, got, agedUpdatedAt)
		}
		if issuesDirty() {
			t.Errorf("%s: issues is unstaged in dolt_status after the commit; the timestamp write was left in the working set", op)
		}
	}
	backdate := func() {
		t.Helper()
		if _, err := store.db.ExecContext(ctx,
			"UPDATE issues SET updated_at = ? WHERE id = ?", agedUpdatedAt, issue.ID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
	}

	backdate()
	if err := store.AddLabel(ctx, issue.ID, "alpha", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	assertOp("AddLabel")

	backdate()
	if err := store.RemoveLabel(ctx, issue.ID, "alpha", "tester"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	assertOp("RemoveLabel")

	backdate()
	err := store.RunInTransaction(ctx, "test: label add in transaction", func(tx storage.Transaction) error {
		return tx.AddLabel(ctx, issue.ID, "txlabel", "tester")
	})
	if err != nil {
		t.Fatalf("transaction AddLabel: %v", err)
	}
	assertOp("transaction AddLabel")

	// A rename is one label edit per carrier: its stamp must land in the
	// commit the rename itself publishes (the commit list carries the owning
	// table), not stay behind in the working set.
	if err := store.AddLabel(ctx, issue.ID, "rnold", "tester"); err != nil {
		t.Fatalf("seed AddLabel for rename: %v", err)
	}
	backdate()
	renamed, merged, _, err := store.RenameLabel(ctx, "rnold", "rnnew", "tester")
	if err != nil {
		t.Fatalf("RenameLabel: %v", err)
	}
	if renamed != 1 || merged != 0 {
		t.Fatalf("RenameLabel counts = (%d, %d), want (1, 0)", renamed, merged)
	}
	assertOp("RenameLabel")
}

// TestLabelsOnlyPatchStampsUpdatedAndStagesOwningTable covers the `bd update
// --add-label` shape (GH#5442): a guarded update whose patch carries only
// labels never writes the issue row for its scalar half, so the label patch's
// owning-table stamp is the only updated_at write — and the changed-tables set
// must name the routed owning table so the write is committed. A patch that
// leaves the label set unchanged must stamp nothing.
func TestLabelsOnlyPatchStampsUpdatedAndStagesOwningTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}

	backdate := func(t *testing.T, table, id string) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx,
			"UPDATE "+table+" SET updated_at = ? WHERE id = ?", agedUpdatedAt, id); err != nil {
			t.Fatalf("backdate %s/%s: %v", table, id, err)
		}
	}
	assertStamped := func(t *testing.T, op, table, id string) {
		t.Helper()
		var got time.Time
		if err := store.db.QueryRowContext(ctx,
			"SELECT updated_at FROM "+table+" WHERE id = ?", id).Scan(&got); err != nil {
			t.Fatalf("read %s.updated_at for %s: %v", table, id, err)
		}
		if !got.UTC().After(agedUpdatedAt) {
			t.Errorf("%s: %s.updated_at for %s = %v, want a stamp after %v",
				op, table, id, got.UTC(), agedUpdatedAt)
		}
		var dirty int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_status WHERE table_name = ?", table).Scan(&dirty); err != nil {
			t.Fatalf("read dolt_status: %v", err)
		}
		// wisps is dolt_ignored: it can never be staged, so its diff stays
		// listed in dolt_status forever — only tracked tables must be clean.
		if dirty > 0 && table != "wisps" {
			t.Errorf("%s: %s is unstaged in dolt_status after the update commit; ExecuteUpdate must report the owning table", op, table)
		}
	}
	assertUnstamped := func(t *testing.T, table, id string) {
		t.Helper()
		var got time.Time
		if err := store.db.QueryRowContext(ctx,
			"SELECT updated_at FROM "+table+" WHERE id = ?", id).Scan(&got); err != nil {
			t.Fatalf("read %s.updated_at for %s: %v", table, id, err)
		}
		if !got.UTC().Equal(agedUpdatedAt) {
			t.Errorf("no-op label patch moved %s.updated_at for %s to %v, want it preserved at %v",
				table, id, got.UTC(), agedUpdatedAt)
		}
	}

	t.Run("durable", func(t *testing.T) {
		id := "lbl-patch-dur"
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "durable patch probe",
			Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatal(err)
		}
		backdate(t, "issues", id)
		if _, err := operations.Update(ctx, publicops.UpdateRequest{
			Actor: "tester", IssueID: id,
			Patch: publicops.IssuePatch{Labels: publicops.LabelPatch{Add: []string{"patched"}}},
		}); err != nil {
			t.Fatalf("labels-only update: %v", err)
		}
		assertStamped(t, "labels-only update", "issues", id)

		// A patch re-adding a label the issue already carries is a no-op.
		backdate(t, "issues", id)
		if _, err := operations.Update(ctx, publicops.UpdateRequest{
			Actor: "tester", IssueID: id,
			Patch: publicops.IssuePatch{Labels: publicops.LabelPatch{Add: []string{"patched"}}},
		}); err != nil {
			t.Fatalf("no-op labels-only update: %v", err)
		}
		assertUnstamped(t, "issues", id)
	})

	t.Run("wisp", func(t *testing.T) {
		id := "lbl-patch-wisp"
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "wisp patch probe", Ephemeral: true,
			Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatal(err)
		}
		backdate(t, "wisps", id)
		if _, err := operations.Update(ctx, publicops.UpdateRequest{
			Actor: "tester", IssueID: id,
			Patch: publicops.IssuePatch{Labels: publicops.LabelPatch{Add: []string{"wpatched"}}},
		}); err != nil {
			t.Fatalf("labels-only wisp update: %v", err)
		}
		assertStamped(t, "labels-only wisp update", "wisps", id)
	})
}

// TestWispLabelTransactionLeavesDurableDiffUnswept pins the routing half of
// GH#5442's staging change: a wisp label transaction marks the WISP owning
// table, never a hardcoded durable "issues", so an unrelated preexisting
// durable-row diff must survive the wisp commit unstaged.
func TestWispLabelTransactionLeavesDurableDiffUnswept(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	wisp := &types.Issue{
		ID: "lbl-wisp-tx", Title: "wisp label tx probe", Ephemeral: true,
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	stray := &types.Issue{
		ID: "lbl-stray", Title: "stray durable row",
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, stray, "tester"); err != nil {
		t.Fatalf("create stray: %v", err)
	}
	// An unrelated durable diff the operator has not committed.
	if _, err := store.db.ExecContext(ctx,
		"UPDATE issues SET title = 'stray uncommitted edit' WHERE id = ?", stray.ID); err != nil {
		t.Fatalf("dirty issues: %v", err)
	}

	if err := store.RunInTransaction(ctx, "test: wisp label add", func(tx storage.Transaction) error {
		return tx.AddLabel(ctx, wisp.ID, "wisp-only", "tester")
	}); err != nil {
		t.Fatalf("transaction AddLabel(wisp): %v", err)
	}

	var strayTitle string
	if err := store.db.QueryRowContext(ctx,
		"SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", stray.ID).Scan(&strayTitle); err != nil {
		t.Fatalf("read stray title at HEAD: %v", err)
	}
	if strayTitle != "stray durable row" {
		t.Fatalf("stray durable edit was swept into the wisp commit: HEAD title = %q", strayTitle)
	}
	var dirty int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'issues'").Scan(&dirty); err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	if dirty == 0 {
		t.Fatalf("issues diff was swept into the wisp commit; it must stay unstaged")
	}
}
