package dolt

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// What is left here is what the Lifecycle contract cannot own.
//
// The guard table and the ExpectedVersion suite that used to live in this file
// are gone: the contract states the same promises at three backends, in the
// raw-row plane with event-delta counters, and four of its cases now carry the
// seedings this file was the only holder of — a stale is_blocked column whose
// blockers have closed, a closed row that still reads blocked, children counted
// across both dependency planes, and one child edge resident in both. The
// wrapper-routing questions no contract can ask moved to
// checked_wrapper_smoke_test.go.
//
// These three stay because their subject is not the close's SEMANTICS:
// two-transaction serialization, savepoint restoration on a pinned connection,
// and a corrupt graph the role fixtures cannot build.

// TestCloseIssueCheckedRefusesADanglingParentBeforeCountingChildren pins the
// ORDER of the two reads a checked close starts with: the target is resolved
// first, so an ID that names no row is ErrNotFound even when a parent-child
// edge names it as a parent. Getting it backwards reports "close blocked by
// open children" for an issue that does not exist, which is what a caller then
// tries to fix by closing children that are not really children.
//
// The state is seeded with the foreign key checks off, because it is what a
// deletion that outran its cascade leaves — not something a supported verb
// produces. That is also why it stays here rather than moving into the
// contract: it is a corrupt-graph regression pin, and the promise the contract
// states generically would not have caught the original defect.
func TestCloseIssueCheckedRefusesADanglingParentBeforeCountingChildren(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const child, missing = "cic-dangling-child", "cic-dangling-parent"
	createPerm(t, ctx, store, child)
	if _, err := store.db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("disable FK checks: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, 'parent-child', NOW(), 'tester')", child, missing); err != nil {
		t.Fatalf("insert dangling parent-child edge: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
		t.Fatalf("re-enable FK checks: %v", err)
	}

	_, err := store.CloseIssueChecked(ctx, missing, "tester", storage.CloseIssueOptions{Reason: "done"})
	if !errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrCloseOpenChildren) {
		t.Fatalf("err = %v, want ErrNotFound but not ErrCloseOpenChildren", err)
	}
}

// TestCloseIssueCheckedParentChildInsertCannotPhantomMerge is a controlled
// two-transaction Dolt regression: a close which observes no children and a
// concurrently-created open parent-child edge must not both commit. If they do,
// the resulting durable state can preserve an overlapping stale-snapshot
// decision instead of forcing one writer to retry against the other.
func TestCloseIssueCheckedParentChildInsertCannotPhantomMerge(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const (
		parent = "close-child-phantom-parent"
		child  = "close-child-phantom-child"
	)
	createPerm(t, ctx, store, parent)
	createPerm(t, ctx, store, child)

	// Deliberately start both transactions before either writes. The close reads
	// zero children before txAdd inserts its edge; committing close first makes
	// this interleaving deterministic rather than timing-dependent.
	txClose, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin close transaction: %v", err)
	}
	defer txClose.Rollback() // no-op after Commit; preserves cleanup on early Fatal.
	txAdd, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin add-child transaction: %v", err)
	}
	defer txAdd.Rollback()

	closeResult, err := issueops.CloseIssueCheckedInTx(ctx, txClose, parent, "done", "tester", "", false, nil)
	if err != nil {
		t.Fatalf("checked close before concurrent edge: %v", err)
	}
	if closeResult.OpenChildren != 0 || closeResult.AlreadyClosed {
		t.Fatalf("close precondition result = %+v, want a real close after observing zero children", closeResult)
	}

	if _, err := issueops.AddDependencyInTx(ctx, txAdd, &types.Dependency{
		IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
	}, "tester", issueops.AddDependencyOpts{}); err != nil {
		t.Fatalf("insert concurrent parent-child edge: %v", err)
	}

	closeCommitErr := txClose.Commit()
	addCommitErr := txAdd.Commit()
	t.Logf("controlled close/add commits: close=%v add=%v", closeCommitErr, addCommitErr)
	if closeCommitErr == nil && addCommitErr == nil {
		var parentStatus, childStatus string
		if err := store.db.QueryRowContext(ctx, "SELECT status FROM issues WHERE id = ?", parent).Scan(&parentStatus); err != nil {
			t.Fatalf("read parent after both commits: %v", err)
		}
		if err := store.db.QueryRowContext(ctx, "SELECT status FROM issues WHERE id = ?", child).Scan(&childStatus); err != nil {
			t.Fatalf("read child after both commits: %v", err)
		}
		t.Fatalf("phantom parent-child merge: checked close and edge insert both committed (parent=%s child=%s); one transaction must conflict or be refused", parentStatus, childStatus)
	}

	// A conflict on either commit is the required serialization outcome. Keep
	// both errors in the failure below should a future Dolt version return an
	// unexpected non-serialization result from a changed transaction contract.
	if closeCommitErr != nil && !isSerializationError(closeCommitErr) {
		t.Fatalf("close commit error = %v, want a serialization conflict", closeCommitErr)
	}
	if addCommitErr != nil && !isSerializationError(addCommitErr) {
		t.Fatalf("add-child commit error = %v, want a serialization conflict", addCommitErr)
	}
}

// TestCloseIssueCheckedPinnedConnRefusalRestoresCoordinationSavepoint proves
// that the production pinned-connection UOW can catch a checked-close refusal
// and commit a sibling operation without persisting the refused close's
// coordination writes.
func TestCloseIssueCheckedPinnedConnRefusalRestoresCoordinationSavepoint(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const (
		parent  = "close-savepoint-parent"
		child   = "close-savepoint-child"
		sibling = "close-savepoint-sibling"
	)
	for _, id := range []string{parent, child, sibling} {
		createPerm(t, ctx, store, id)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency(%s -> %s): %v", child, parent, err)
	}

	coordinationValue := func(t *testing.T, tier string) *string {
		t.Helper()
		var value string
		err := store.db.QueryRowContext(ctx,
			"SELECT value FROM local_metadata WHERE `key` LIKE ?",
			"dependency-coordination/v1/"+tier+"/%",
		).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			t.Fatalf("read %s coordination cell: %v", tier, err)
		}
		return &value
	}
	beforeDurable := coordinationValue(t, "dependencies")
	beforeWisp := coordinationValue(t, "wisp_dependencies")
	if beforeDurable == nil || beforeWisp != nil {
		t.Fatalf("pre-call coordination = durable:%v wisp:%v, want durable token and absent wisp token", beforeDurable, beforeWisp)
	}

	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin transaction connection: %v", err)
	}
	transactionOpen := true
	defer func() {
		if transactionOpen {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
		_ = conn.Close()
	}()
	if _, err := conn.ExecContext(ctx, "START TRANSACTION"); err != nil {
		t.Fatalf("start pinned-connection transaction: %v", err)
	}

	_, err = issueops.CloseIssueCheckedInTx(ctx, conn, parent, "done", "tester", "", false, nil)
	if !errors.Is(err, storage.ErrCloseOpenChildren) {
		t.Fatalf("checked close error = %v, want ErrCloseOpenChildren", err)
	}
	if _, err := issueops.CloseIssueInTx(ctx, conn, sibling, "done", "tester", ""); err != nil {
		t.Fatalf("close sibling after refused parent close: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("commit sibling close after refusal: %v", err)
	}
	transactionOpen = false
	if err := conn.Close(); err != nil {
		t.Fatalf("release committed transaction connection: %v", err)
	}

	for _, check := range []struct {
		id     string
		status types.Status
	}{{parent, types.StatusOpen}, {sibling, types.StatusClosed}} {
		issue, err := store.GetIssue(ctx, check.id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", check.id, err)
		}
		if issue.Status != check.status {
			t.Fatalf("%s status = %q, want %q", check.id, issue.Status, check.status)
		}
	}

	for _, check := range []struct {
		tier string
		want *string
	}{
		{tier: "dependencies", want: beforeDurable},
		{tier: "wisp_dependencies", want: beforeWisp},
	} {
		got := coordinationValue(t, check.tier)
		if (got == nil) != (check.want == nil) {
			t.Fatalf("%s coordination presence = %v, want %v", check.tier, got != nil, check.want != nil)
		}
		if got != nil && *got != *check.want {
			t.Fatalf("%s coordination value = %q, want pre-call %q", check.tier, *got, *check.want)
		}
	}
}
