//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestBlockedListerContract runs the BlockedLister contract against the
// embedded store.
func TestBlockedListerContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "blk")
	lister, err := te.store.BlockedLister()
	if err != nil {
		t.Fatalf("BlockedLister(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, "blk")
	conformance.RunBlockedLister(t, t.Context(), conformance.BlockedListerFixture{
		IssuePrefix:   kit.IssuePrefix,
		BlockedLister: lister,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		MarkBlocked: func(ctx context.Context, id string) error {
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
			if err != nil {
				return err
			}
			defer func() { _ = cleanup() }()
			res, err := db.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 WHERE id = ?", id)
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
