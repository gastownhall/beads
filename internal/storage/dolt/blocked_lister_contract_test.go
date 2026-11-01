package dolt

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBlockedListerContract runs the BlockedLister contract against the
// server-backed store.
func TestBlockedListerContract(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	lister, err := store.BlockedLister()
	if err != nil {
		t.Fatalf("BlockedLister(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, "blk")
	conformance.RunBlockedLister(t, ctx, conformance.BlockedListerFixture{
		IssuePrefix:   kit.IssuePrefix,
		BlockedLister: lister,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		MarkBlocked: func(ctx context.Context, id string) error {
			res, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 WHERE id = ?", id)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				return fmt.Errorf("marked %d rows (err %v), want 1", n, err)
			}
			return nil
		},
	})
}
