//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedBlockedRecheckSettlesDependentAfterCommit is the release-line
// regression test for the embedded half of gastownhall/beads#6716. It uses
// only store entry points that exist before the fix, so it compiles on both
// sides of it and fails without it.
//
// Embedded transactions serialize, so the two-session race cannot be staged
// here. What the fix promises on this tier is the contract: a dependent a
// committed close recomputed is recomputed again on a fresh snapshot. The
// test stands in for the unseen sibling with a write inside the same
// transaction that bypasses the recompute, after the close's own recompute
// has run and left rb-c blocked.
func TestEmbeddedBlockedRecheckSettlesDependentAfterCommit(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), ".beads"), "recheckreg", "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "rb"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"rb-a", "rb-b", "rb-c"} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, blocker := range []string{"rb-a", "rb-b"} {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: "rb-c", DependsOnID: blocker, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("add dependency rb-c -> %s: %v", blocker, err)
		}
	}

	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		if _, err := issueops.CloseIssueInTx(ctx, tx, "rb-a", "done", "tester", ""); err != nil {
			return err
		}
		var blocked bool
		if err := tx.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = 'rb-c'").Scan(&blocked); err != nil {
			return err
		}
		if !blocked {
			t.Fatal("rb-c unblocked in-transaction while rb-b was still open")
		}
		_, err := tx.ExecContext(ctx, "UPDATE issues SET status = 'closed', closed_at = NOW() WHERE id = 'rb-b'")
		return err
	}); err != nil {
		t.Fatalf("close rb-a: %v", err)
	}

	ready, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if !slices.ContainsFunc(ready, func(i *types.Issue) bool { return i.ID == "rb-c" }) {
		t.Fatal("rb-c has no open blockers but is missing from ready work (stale is_blocked)")
	}
}
