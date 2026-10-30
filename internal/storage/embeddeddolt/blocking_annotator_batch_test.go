//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// perItemBlockingAnnotation is the external-dependency annotator's answer as it
// was computed before it batched: the backend's own annotation, then one
// IsBlocked per item through the policy store, whose answer replaces a blocked
// item's BlockedBy. The batched annotator must equal it item for item.
func perItemBlockingAnnotation(ctx context.Context, t *testing.T, inner storage.DoltStorage, policy *externaldeps.Store, req publicops.BlockingRequest) publicops.BlockingResult {
	t.Helper()
	annotator, err := inner.BlockingAnnotator()
	if err != nil {
		t.Fatalf("inner BlockingAnnotator: %v", err)
	}
	result, err := annotator.AnnotateBlocking(ctx, req)
	if err != nil {
		t.Fatalf("inner AnnotateBlocking: %v", err)
	}
	for i := range result.Items {
		blocked, blockers, err := policy.IsBlocked(ctx, result.Items[i].ID)
		if err != nil {
			t.Fatalf("IsBlocked(%s): %v", result.Items[i].ID, err)
		}
		if blocked {
			result.Items[i].BlockedBy = blockers
		}
	}
	return result
}

// countingPolicyStore wraps local in the external-dependency policy, with
// project "remote" resolving to remote and every other project unconfigured.
// opens counts foreign-project opens.
func countingPolicyStore(local, remote storage.DoltStorage, opens *atomic.Int64) *externaldeps.Store {
	return externaldeps.New(
		local,
		func(project externaldeps.ProjectName) (string, bool) {
			return string(project), project == "remote"
		},
		func(_ context.Context, _ string) (storage.DoltStorage, error) {
			opens.Add(1)
			return nonClosingStore{DoltStorage: remote}, nil
		},
	)
}

// TestPolicyBlockingAnnotatorBatchEqualsPerItem pins that the policy
// annotator's batched answer is the per-item answer it replaced, across every
// shape that decides it: live local blockers of each blocking type (and the
// closed and pinned ones that do not count), a block inherited through the
// parent chain, waits-for and conditional-blocks gates on both planes, a
// satisfied, an unsatisfied and an unconfigured external ref, a non-blocking
// external edge, a mix of local and external, an unblocked row and an id with
// no row. It also pins the local batch
// read (IsBlockedWithBlockersBatch) against per-row IsBlocked, and that the
// batched page opens the foreign project once.
func TestPolicyBlockingAnnotatorBatchEqualsPerItem(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "ab")
	remote := openExternalDependencyStore(t, "remote")
	ctx := t.Context()

	for _, p := range []struct {
		id, capability string
		shipped        bool
	}{{"remote-shipped", "shipped", true}, {"remote-pending", "pending", false}} {
		if err := remote.CreateIssue(ctx, &types.Issue{ID: p.id, Title: p.id, Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", p.id, err)
		}
		if err := remote.AddLabel(ctx, p.id, "provides:"+p.capability, "tester"); err != nil {
			t.Fatalf("AddLabel(%s): %v", p.id, err)
		}
		if p.shipped {
			if err := remote.CloseIssue(ctx, p.id, "shipped", "tester", ""); err != nil {
				t.Fatalf("CloseIssue(%s): %v", p.id, err)
			}
		}
	}

	mk := func(id string, status types.Status, ephemeral bool) {
		t.Helper()
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
		switch status {
		case types.StatusClosed:
			if err := te.store.CloseIssue(ctx, id, "done", "tester", ""); err != nil {
				t.Fatalf("CloseIssue(%s): %v", id, err)
			}
		case types.StatusPinned:
			if err := te.store.UpdateIssue(ctx, id, map[string]interface{}{"status": string(types.StatusPinned)}, "tester"); err != nil {
				t.Fatalf("pin %s: %v", id, err)
			}
		}
	}
	add := func(src, tgt string, typ types.DependencyType) {
		t.Helper()
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
			t.Fatalf("AddDependency(%s -> %s %s): %v", src, tgt, typ, err)
		}
	}

	mk("ab-x", types.StatusOpen, false)
	mk("ab-y", types.StatusOpen, false)
	mk("ab-done", types.StatusClosed, false)
	mk("ab-pin", types.StatusPinned, false)
	mk("ab-wx", types.StatusOpen, true)
	for _, id := range []string{"ab-blk", "ab-two", "ab-blk-done", "ab-blk-pin", "ab-parent", "ab-child", "ab-grandchild",
		"ab-wait", "ab-sat", "ab-unsat", "ab-gone", "ab-mixed", "ab-sat-local", "ab-related", "ab-free"} {
		mk(id, types.StatusOpen, false)
	}
	mk("ab-cond", types.StatusOpen, true)
	mk("ab-wisp-blk", types.StatusOpen, true)

	add("ab-blk", "ab-x", types.DepBlocks)
	add("ab-two", "ab-y", types.DepBlocks)
	add("ab-two", "ab-x", types.DepBlocks)
	add("ab-blk-done", "ab-done", types.DepBlocks)
	add("ab-blk-pin", "ab-pin", types.DepBlocks)
	add("ab-parent", "ab-x", types.DepBlocks)
	add("ab-child", "ab-parent", types.DepParentChild)
	add("ab-grandchild", "ab-child", types.DepParentChild)
	add("ab-wait", "ab-x", types.DepWaitsFor)
	add("ab-cond", "ab-x", types.DepConditionalBlocks)
	add("ab-wisp-blk", "ab-wx", types.DepBlocks)
	add("ab-sat", "external:remote:shipped", types.DepBlocks)
	add("ab-unsat", "external:remote:pending", types.DepBlocks)
	add("ab-gone", "external:gone:thing", types.DepBlocks)
	add("ab-mixed", "ab-x", types.DepBlocks)
	add("ab-mixed", "external:remote:pending", types.DepBlocks)
	add("ab-mixed", "external:remote:shipped", types.DepBlocks)
	add("ab-sat-local", "ab-x", types.DepBlocks)
	add("ab-sat-local", "external:remote:shipped", types.DepBlocks)
	add("ab-related", "external:remote:pending", types.DepRelated)

	ids := []string{"ab-x", "ab-blk", "ab-two", "ab-blk-done", "ab-blk-pin", "ab-parent", "ab-child", "ab-grandchild",
		"ab-wait", "ab-cond", "ab-wisp-blk", "ab-sat", "ab-unsat", "ab-gone", "ab-mixed", "ab-sat-local", "ab-related",
		"ab-free", "ab-missing", "ab-blk"}

	// The local batch read answers exactly what per-row IsBlocked does.
	local, err := te.store.IsBlockedWithBlockersBatch(ctx, ids)
	if err != nil {
		t.Fatalf("IsBlockedWithBlockersBatch: %v", err)
	}
	for _, id := range ids {
		blocked, blockers, err := te.store.IsBlocked(ctx, id)
		if err != nil {
			t.Fatalf("IsBlocked(%s): %v", id, err)
		}
		got, ok := local[id]
		if ok != blocked || !reflect.DeepEqual(got, blockers) {
			t.Errorf("IsBlockedWithBlockersBatch[%s] = (%v, %#v), want IsBlocked's (%v, %#v)", id, ok, got, blocked, blockers)
		}
	}

	var opens atomic.Int64
	policy := countingPolicyStore(te.store, remote, &opens)
	annotator, err := policy.BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator: %v", err)
	}
	req := publicops.BlockingRequest{IDs: ids}
	want := perItemBlockingAnnotation(ctx, t, te.store, policy, req)
	opens.Store(0)
	got, err := annotator.AnnotateBlocking(ctx, req)
	if err != nil {
		t.Fatalf("AnnotateBlocking: %v", err)
	}
	if n := opens.Load(); n != 1 {
		t.Errorf("AnnotateBlocking opened the foreign project %d times, want once per request", n)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AnnotateBlocking differs from the per-item answer:\n got  %s\n want %s", dumpBlocking(got), dumpBlocking(want))
	}

	// Spot-check the decisive rows, so the equality above is not two equal
	// wrong answers.
	blockedBy := map[string]string{}
	for _, item := range got.Items {
		blockedBy[item.ID] = fmt.Sprint(item.BlockedBy)
	}
	for id, want := range map[string]string{
		"ab-blk":       "[ab-x]",
		"ab-two":       "[ab-x ab-y]",
		"ab-unsat":     "[external:remote:pending]",
		"ab-gone":      "[external:gone:thing]",
		"ab-mixed":     "[ab-x external:remote:pending]",
		"ab-sat-local": "[ab-x]",
		"ab-related":   "[]",
		"ab-free":      "[]",
	} {
		if blockedBy[id] != want {
			t.Errorf("blocked_by[%s] = %s, want %s", id, blockedBy[id], want)
		}
	}
}

// TestPolicyBlockingAnnotatorForeignOpensAreSublinear measures the page the
// shadow serve refreshes: N ids, every one held by an external ref into the
// same project and half of them by a local blocker too. The per-item answer
// opens the foreign project once per id; the batched one opens it once per
// request, whatever N is. Both timings are logged.
func TestPolicyBlockingAnnotatorForeignOpensAreSublinear(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "sl")
	remote := openExternalDependencyStore(t, "remote")
	ctx := t.Context()
	if err := remote.CreateIssue(ctx, &types.Issue{ID: "remote-pending", Title: "p", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}, "tester"); err != nil {
		t.Fatalf("CreateIssue(remote): %v", err)
	}
	if err := remote.AddLabel(ctx, "remote-pending", "provides:pending", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if err := te.store.CreateIssue(ctx, &types.Issue{ID: "sl-x", Title: "x", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
		t.Fatalf("CreateIssue(sl-x): %v", err)
	}

	const n = 40
	ids := make([]string, 0, n)
	for i := range n {
		id := fmt.Sprintf("sl-%03d", i)
		if err := te.store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: id, DependsOnID: "external:remote:pending", Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("AddDependency(%s external): %v", id, err)
		}
		if i%2 == 0 {
			if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: id, DependsOnID: "sl-x", Type: types.DepBlocks}, "tester"); err != nil {
				t.Fatalf("AddDependency(%s local): %v", id, err)
			}
		}
		ids = append(ids, id)
	}

	var opens atomic.Int64
	policy := countingPolicyStore(te.store, remote, &opens)
	annotator, err := policy.BlockingAnnotator()
	if err != nil {
		t.Fatalf("BlockingAnnotator: %v", err)
	}

	for _, size := range []int{n / 4, n} {
		sized := publicops.BlockingRequest{IDs: ids[:size]}
		opens.Store(0)
		started := time.Now()
		want := perItemBlockingAnnotation(ctx, t, te.store, policy, sized)
		perItem, perItemOpens := time.Since(started), opens.Load()

		opens.Store(0)
		started = time.Now()
		got, err := annotator.AnnotateBlocking(ctx, sized)
		if err != nil {
			t.Fatalf("AnnotateBlocking(%d): %v", size, err)
		}
		batched, batchedOpens := time.Since(started), opens.Load()

		t.Logf("N=%d: per-item %v (%d foreign opens), batched %v (%d foreign opens)", size, perItem, perItemOpens, batched, batchedOpens)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("N=%d: batched answer differs from the per-item one", size)
		}
		if perItemOpens != int64(size) {
			t.Errorf("N=%d: per-item opened the foreign project %d times, want %d (the baseline this test measures against)", size, perItemOpens, size)
		}
		if batchedOpens != 1 {
			t.Errorf("N=%d: batched opened the foreign project %d times, want 1 whatever N is", size, batchedOpens)
		}
	}
}

func dumpBlocking(result publicops.BlockingResult) string {
	out := ""
	for _, item := range result.Items {
		out += fmt.Sprintf("\n   %s blocked_by=%#v blocks=%#v parent=%q", item.ID, item.BlockedBy, item.Blocks, item.Parent)
	}
	return out
}
