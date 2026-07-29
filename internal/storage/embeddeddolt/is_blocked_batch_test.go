//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestIsBlockedBatchEmbedded is the batch-is_blocked regression on the embedded
// Dolt backend: IsBlockedBatch agrees with per-row IsBlocked for every id and
// reflects an inherited parent-child block (transitive is_blocked with an empty
// direct-blocker set), through the same store surface.
func TestIsBlockedBatchEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "ib")
	ctx := t.Context()

	mk := func(id string) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	add := func(src, tgt string, typ types.DependencyType) {
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s->%s: %v", src, tgt, err)
		}
	}
	mk("ib-blk")
	mk("ib-parent")
	add("ib-parent", "ib-blk", types.DepBlocks)
	mk("ib-child")
	add("ib-child", "ib-parent", types.DepParentChild)
	mk("ib-free")

	ids := []string{"ib-blk", "ib-parent", "ib-child", "ib-free"}
	batch, err := te.store.IsBlockedBatch(ctx, ids)
	if err != nil {
		t.Fatalf("IsBlockedBatch: %v", err)
	}
	for _, id := range ids {
		want, _, err := te.store.IsBlocked(ctx, id)
		if err != nil {
			t.Fatalf("IsBlocked(%s): %v", id, err)
		}
		if batch[id] != want {
			t.Fatalf("IsBlockedBatch[%s] = %v, want %v (per-row IsBlocked)", id, batch[id], want)
		}
	}
	blocked, blockers, err := te.store.IsBlocked(ctx, "ib-child")
	if err != nil {
		t.Fatalf("IsBlocked(ib-child): %v", err)
	}
	if !blocked || len(blockers) != 0 {
		t.Fatalf("ib-child IsBlocked = (%v, %v), want (true, []) — inherited block, empty direct blockers", blocked, blockers)
	}
	if !batch["ib-child"] || !batch["ib-parent"] || batch["ib-free"] {
		t.Fatalf("IsBlockedBatch = %v, want child+parent true, free false", batch)
	}
}
