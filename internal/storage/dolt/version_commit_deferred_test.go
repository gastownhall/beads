package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	rootissueops "github.com/steveyegge/beads/issueops"
)

// bd-4wamg: a context carrying WithDeferredVersionCommit must leave writes in
// the Dolt working set — no per-write version commit — until an explicit
// commit point flushes them (CommitPending / bd dolt commit). This is the
// server-mode half of --dolt-auto-commit batch/off; the CLI sets the context
// in issueOpsContext, and this store honors it in stageAndCommit — the
// staging every GH#5752-migrated path's publication funnels through, so the
// deferral now holds uniformly, the sweeper included.
func TestDeferredVersionCommitLeavesWritesForCommitPending(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Baseline: a plain create advances history.
	before := doltCommitCount(ctx, t, store)
	seedIssues(ctx, t, store, "test-defer-baseline")
	if after := doltCommitCount(ctx, t, store); after <= before {
		t.Fatalf("plain create: commit count %d -> %d, want an advance", before, after)
	}

	// Deferred: the same write must not advance history.
	deferredCtx := issueops.WithDeferredVersionCommit(ctx)
	before = doltCommitCount(ctx, t, store)
	if err := store.CreateIssues(deferredCtx, []*types.Issue{
		{ID: "test-defer-pending", Title: "deferred write", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}, "tester"); err != nil {
		t.Fatalf("deferred create: %v", err)
	}
	if after := doltCommitCount(ctx, t, store); after != before {
		t.Fatalf("deferred create advanced history: commit count %d -> %d, want unchanged", before, after)
	}

	// The write is durable in SQL regardless of version-commit deferral.
	got, err := store.GetIssue(ctx, "test-defer-pending")
	if err != nil || got == nil {
		t.Fatalf("deferred issue not readable back: %v", err)
	}

	// An explicit commit point flushes the pending working set as one commit.
	committed, err := store.CommitPending(ctx, "tester")
	if err != nil {
		t.Fatalf("CommitPending: %v", err)
	}
	if !committed {
		t.Fatal("CommitPending reported nothing to commit; deferred write missing from working set")
	}
	if after := doltCommitCount(ctx, t, store); after != before+1 {
		t.Fatalf("CommitPending: commit count %d -> %d, want exactly +1", before, after)
	}
}

// TestDeferredVersionCommitUpdatePath covers the update verb's commit site
// (the shared post-tx publisher) under deferral.
func TestDeferredVersionCommitUpdatePath(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedIssues(ctx, t, store, "test-defer-update")

	deferredCtx := issueops.WithDeferredVersionCommit(ctx)
	before := doltCommitCount(ctx, t, store)
	if err := store.UpdateIssue(deferredCtx, "test-defer-update", map[string]interface{}{
		"priority": 1,
	}, "tester"); err != nil {
		t.Fatalf("deferred update: %v", err)
	}
	if after := doltCommitCount(ctx, t, store); after != before {
		t.Fatalf("deferred update advanced history: commit count %d -> %d, want unchanged", before, after)
	}

	committed, err := store.CommitPending(ctx, "tester")
	if err != nil {
		t.Fatalf("CommitPending: %v", err)
	}
	if !committed {
		t.Fatal("CommitPending reported nothing to commit after deferred update")
	}
}

// TestDeferredVersionCommitDeletePath covers the delete verb's commit site
// (the role deleter's post-tx publication) under deferral — the site the
// bd-4wamg review found unwired at the CLI layer (delete.go passed a bare
// rootCtx).
func TestDeferredVersionCommitDeletePath(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedIssues(ctx, t, store, "test-defer-del-a", "test-defer-del-b")

	del, err := store.Deleter()
	if err != nil {
		t.Fatalf("Deleter: %v", err)
	}

	// Plain ctx: a forced delete advances history (per-write commit).
	before := doltCommitCount(ctx, t, store)
	if _, err := del.Delete(ctx, rootissueops.DeleteRequest{
		Actor: "tester", IDs: []string{"test-defer-del-a"}, Force: true,
	}); err != nil {
		t.Fatalf("plain delete: %v", err)
	}
	if after := doltCommitCount(ctx, t, store); after <= before {
		t.Fatalf("plain delete: commit count %d -> %d, want an advance", before, after)
	}

	// Deferred ctx: the delete lands in SQL but must not advance history.
	deferredCtx := issueops.WithDeferredVersionCommit(ctx)
	before = doltCommitCount(ctx, t, store)
	if _, err := del.Delete(deferredCtx, rootissueops.DeleteRequest{
		Actor: "tester", IDs: []string{"test-defer-del-b"}, Force: true,
	}); err != nil {
		t.Fatalf("deferred delete: %v", err)
	}
	if after := doltCommitCount(ctx, t, store); after != before {
		t.Fatalf("deferred delete advanced history: commit count %d -> %d, want unchanged", before, after)
	}
	if got, err := store.GetIssue(ctx, "test-defer-del-b"); err == nil && got != nil {
		t.Fatal("deferred delete did not remove the row from SQL")
	}

	committed, err := store.CommitPending(ctx, "tester")
	if err != nil {
		t.Fatalf("CommitPending: %v", err)
	}
	if !committed {
		t.Fatal("CommitPending reported nothing to commit after deferred delete")
	}
}

// TestDeferredVersionCommitSweepPath covers the role sweeper under deferral
// (GH#5752 AC-6). The sweeper's pre-migration in-tx staging had no deferral
// check — an intentional correction of that inconsistency: a deferred-context
// sweep now leaves its deletions in the working set for the explicit commit
// point, like every other write. The tier is durable (bd prune) because a
// wisps-plane sweep deletes only dolt-ignored tables, whose publication is
// empty by design — nothing a deferral could suppress.
func TestDeferredVersionCommitSweepPath(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Seed two closed durable issues — the durable tier sweep's input.
	seedIssues(ctx, t, store, "test-defer-sweep-a", "test-defer-sweep-b")
	for _, id := range []string{"test-defer-sweep-a", "test-defer-sweep-b"} {
		if err := store.CloseIssue(ctx, id, "done", "tester", "seed-session"); err != nil {
			t.Fatalf("close %s: %v", id, err)
		}
	}

	sweeper, err := store.Sweeper()
	if err != nil {
		t.Fatalf("Sweeper: %v", err)
	}

	// Plain ctx: the sweep is a publishing operation — it advances history.
	before := doltCommitCount(ctx, t, store)
	res, err := sweeper.Sweep(ctx, rootissueops.SweepRequest{
		Tier:      rootissueops.SweepDurable,
		IDPattern: "test-defer-sweep-a",
	})
	if err != nil {
		t.Fatalf("plain sweep: %v", err)
	}
	if res.Swept != 1 {
		t.Fatalf("plain sweep swept %d, want 1", res.Swept)
	}
	if after := doltCommitCount(ctx, t, store); after <= before {
		t.Fatalf("plain sweep: commit count %d -> %d, want an advance", before, after)
	}

	// Deferred ctx: the sweep lands in SQL but must not advance history.
	deferredCtx := issueops.WithDeferredVersionCommit(ctx)
	before = doltCommitCount(ctx, t, store)
	res, err = sweeper.Sweep(deferredCtx, rootissueops.SweepRequest{
		Tier:      rootissueops.SweepDurable,
		IDPattern: "test-defer-sweep-b",
	})
	if err != nil {
		t.Fatalf("deferred sweep: %v", err)
	}
	if res.Swept != 1 {
		t.Fatalf("deferred sweep swept %d, want 1", res.Swept)
	}
	if after := doltCommitCount(ctx, t, store); after != before {
		t.Fatalf("deferred sweep advanced history: commit count %d -> %d, want unchanged", before, after)
	}
	var remaining int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", "test-defer-sweep-b").Scan(&remaining); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("deferred sweep left %d issues rows, want the deletion durable in SQL", remaining)
	}

	// The explicit commit point flushes the deferred sweep as one commit.
	committed, err := store.CommitPending(ctx, "tester")
	if err != nil {
		t.Fatalf("CommitPending: %v", err)
	}
	if !committed {
		t.Fatal("CommitPending reported nothing to commit after deferred sweep")
	}
	if after := doltCommitCount(ctx, t, store); after != before+1 {
		t.Fatalf("CommitPending: commit count %d -> %d, want exactly +1", before, after)
	}
}
