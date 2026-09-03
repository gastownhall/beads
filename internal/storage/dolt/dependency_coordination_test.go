package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func TestAddDependencyInTxParentChildCoordinationTouch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	parent, child, blocker, blocked := "coord-parent", "coord-child", "coord-blocker", "coord-blocked"
	for _, id := range []string{parent, child, blocker, blocked} {
		createPerm(t, ctx, store, id)
	}

	key := "dependency-coordination/v1/dependencies/%"
	add := func(dep *types.Dependency) {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin add dependency transaction: %v", err)
		}
		defer tx.Rollback()
		if _, err := issueops.AddDependencyInTx(ctx, tx, dep, "tester", issueops.AddDependencyOpts{}); err != nil {
			t.Fatalf("AddDependencyInTx(%s -> %s): %v", dep.IssueID, dep.DependsOnID, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit add dependency: %v", err)
		}
	}
	add(&types.Dependency{IssueID: child, DependsOnID: parent, Type: types.DepParentChild})

	var value string
	if err := store.db.QueryRowContext(ctx, "SELECT value FROM local_metadata WHERE `key` LIKE ?", key).Scan(&value); err != nil {
		t.Fatalf("read new parent-child coordination token: %v", err)
	}
	if value == "" {
		t.Fatal("new parent-child edge did not write a coordination token")
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE local_metadata SET value = 'sentinel' WHERE `key` LIKE ?", key); err != nil {
		t.Fatalf("seed idempotent sentinel: %v", err)
	}

	add(&types.Dependency{IssueID: child, DependsOnID: parent, Type: types.DepParentChild})
	if err := store.db.QueryRowContext(ctx, "SELECT value FROM local_metadata WHERE `key` LIKE ?", key).Scan(&value); err != nil {
		t.Fatalf("read idempotent coordination token: %v", err)
	}
	if value != "sentinel" {
		t.Fatalf("idempotent parent-child re-add rewrote coordination token = %q, want sentinel", value)
	}

	add(&types.Dependency{IssueID: blocker, DependsOnID: blocked, Type: types.DepBlocks})
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM local_metadata WHERE `key` LIKE ?", key).Scan(&count); err != nil {
		t.Fatalf("count coordination rows after non-parent edge: %v", err)
	}
	if count != 1 {
		t.Fatalf("coordination rows after non-parent edge = %d, want 1", count)
	}
}
