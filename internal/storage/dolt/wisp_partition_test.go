package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// =============================================================================
// PartitionWispIDsInTx tests
//
// These lock in the GH#3414 fix: one batched wisp-partition query instead of
// N per-ID round-trips. They also cover boundary cases (empty, unknown IDs,
// >queryBatchSize batches) and ensure mixed-wisp bulk hydrators still route
// correctly through the refactored code paths.
// =============================================================================

// createPerm creates a permanent issue with the given ID.
func createPerm(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	issue := &types.Issue{
		ID:        id,
		Title:     "perm " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create perm %s: %v", id, err)
	}
}

// createWisp creates an ephemeral (wisp) issue with the given ID.
func createWisp(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	issue := &types.Issue{
		ID:        id,
		Title:     "wisp " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create wisp %s: %v", id, err)
	}
}

// partitionInTx runs PartitionWispIDsInTx inside a store read transaction.
func partitionInTx(t *testing.T, ctx context.Context, store *DoltStore, ids []string) (wispIDs, permIDs []string) {
	t.Helper()
	err := store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		wispIDs, permIDs, err = issueops.PartitionWispIDsInTx(ctx, tx, ids)
		return err
	})
	if err != nil {
		t.Fatalf("PartitionWispIDsInTx: %v", err)
	}
	return wispIDs, permIDs
}

func TestPartitionWispIDsInTx_Empty(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	wispIDs, permIDs := partitionInTx(t, ctx, store, nil)
	if len(wispIDs) != 0 || len(permIDs) != 0 {
		t.Errorf("empty input: want ([], []), got (%v, %v)", wispIDs, permIDs)
	}

	wispIDs, permIDs = partitionInTx(t, ctx, store, []string{})
	if len(wispIDs) != 0 || len(permIDs) != 0 {
		t.Errorf("empty slice: want ([], []), got (%v, %v)", wispIDs, permIDs)
	}
}

func TestPartitionWispIDsInTx_AllPerm(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ids := []string{"part-all-perm-1", "part-all-perm-2", "part-all-perm-3"}
	for _, id := range ids {
		createPerm(t, ctx, store, id)
	}

	wispIDs, permIDs := partitionInTx(t, ctx, store, ids)
	if len(wispIDs) != 0 {
		t.Errorf("expected no wisp IDs, got %v", wispIDs)
	}
	if len(permIDs) != len(ids) {
		t.Errorf("expected %d perm IDs, got %d (%v)", len(ids), len(permIDs), permIDs)
	}
	// Input ordering should be preserved.
	for i, want := range ids {
		if permIDs[i] != want {
			t.Errorf("perm ordering broken at %d: want %s, got %s", i, want, permIDs[i])
		}
	}
}

func TestPartitionWispIDsInTx_AllWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ids := []string{"part-all-wisp-1", "part-all-wisp-2", "part-all-wisp-3"}
	for _, id := range ids {
		createWisp(t, ctx, store, id)
	}

	wispIDs, permIDs := partitionInTx(t, ctx, store, ids)
	if len(permIDs) != 0 {
		t.Errorf("expected no perm IDs, got %v", permIDs)
	}
	if len(wispIDs) != len(ids) {
		t.Errorf("expected %d wisp IDs, got %d (%v)", len(ids), len(wispIDs), wispIDs)
	}
	for i, want := range ids {
		if wispIDs[i] != want {
			t.Errorf("wisp ordering broken at %d: want %s, got %s", i, want, wispIDs[i])
		}
	}
}

func TestPartitionWispIDsInTx_MixedWithUnknown(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	createPerm(t, ctx, store, "part-mix-perm-1")
	createPerm(t, ctx, store, "part-mix-perm-2")
	createWisp(t, ctx, store, "part-mix-wisp-1")
	createWisp(t, ctx, store, "part-mix-wisp-2")

	// Interleave IDs and include an unknown one. Unknowns must fall into
	// the permanent bucket (callers treat them as "not a wisp"), mirroring
	// the old per-ID IsActiveWispInTx behavior.
	input := []string{
		"part-mix-perm-1",
		"part-mix-wisp-1",
		"part-mix-unknown",
		"part-mix-perm-2",
		"part-mix-wisp-2",
	}

	wispIDs, permIDs := partitionInTx(t, ctx, store, input)

	sortedWisp := append([]string(nil), wispIDs...)
	sortedPerm := append([]string(nil), permIDs...)
	sort.Strings(sortedWisp)
	sort.Strings(sortedPerm)

	wantWisp := []string{"part-mix-wisp-1", "part-mix-wisp-2"}
	wantPerm := []string{"part-mix-perm-1", "part-mix-perm-2", "part-mix-unknown"}

	if fmt.Sprint(sortedWisp) != fmt.Sprint(wantWisp) {
		t.Errorf("wisp bucket: want %v, got %v", wantWisp, sortedWisp)
	}
	if fmt.Sprint(sortedPerm) != fmt.Sprint(wantPerm) {
		t.Errorf("perm bucket: want %v, got %v", wantPerm, sortedPerm)
	}
}

func TestPartitionWispIDsInTx_PreservesInputOrderWithinBucket(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create IDs in non-alphabetical order to verify we don't re-sort.
	createPerm(t, ctx, store, "order-perm-c")
	createPerm(t, ctx, store, "order-perm-a")
	createWisp(t, ctx, store, "order-wisp-z")
	createWisp(t, ctx, store, "order-wisp-m")

	input := []string{
		"order-perm-c",
		"order-wisp-z",
		"order-perm-a",
		"order-wisp-m",
	}

	wispIDs, permIDs := partitionInTx(t, ctx, store, input)

	wantWisp := []string{"order-wisp-z", "order-wisp-m"}
	wantPerm := []string{"order-perm-c", "order-perm-a"}

	if fmt.Sprint(wispIDs) != fmt.Sprint(wantWisp) {
		t.Errorf("wisp order: want %v, got %v", wantWisp, wispIDs)
	}
	if fmt.Sprint(permIDs) != fmt.Sprint(wantPerm) {
		t.Errorf("perm order: want %v, got %v", wantPerm, permIDs)
	}
}

// TestPartitionWispIDsInTx_LargeBatch exercises the queryBatchSize chunking
// path (internal batch size is 200). Regression guard: confirms we handle
// multi-batch partitions without losing or duplicating IDs.
func TestPartitionWispIDsInTx_LargeBatch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const total = 450 // > 2 * queryBatchSize (200)
	ids := make([]string, 0, total)
	wantWisp := make(map[string]bool)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("large-batch-%04d", i)
		ids = append(ids, id)
		if i%3 == 0 {
			createWisp(t, ctx, store, id)
			wantWisp[id] = true
		} else {
			createPerm(t, ctx, store, id)
		}
	}

	wispIDs, permIDs := partitionInTx(t, ctx, store, ids)

	if len(wispIDs)+len(permIDs) != total {
		t.Fatalf("bucket total mismatch: wisp=%d perm=%d want total=%d",
			len(wispIDs), len(permIDs), total)
	}
	gotWisp := make(map[string]bool, len(wispIDs))
	for _, id := range wispIDs {
		if gotWisp[id] {
			t.Errorf("duplicate id in wisp bucket: %s", id)
		}
		gotWisp[id] = true
		if !wantWisp[id] {
			t.Errorf("id %s misrouted to wisp bucket", id)
		}
	}
	gotPerm := make(map[string]bool, len(permIDs))
	for _, id := range permIDs {
		if gotPerm[id] {
			t.Errorf("duplicate id in perm bucket: %s", id)
		}
		gotPerm[id] = true
		if wantWisp[id] {
			t.Errorf("wisp id %s misrouted to perm bucket", id)
		}
	}
}

// =============================================================================
// Mixed-bucket bulk hydration regression tests
//
// These go through the public store API (which uses the refactored
// issueops.GetXxxForIssuesInTx functions internally) and assert that routing
// remains correct across wisp and permanent tables after the partitioner
// rewrite.
// =============================================================================

func TestGetCommentsForIssues_MixedWispAndPermanent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	createPerm(t, ctx, store, "mix-cmt-perm")
	createWisp(t, ctx, store, "mix-cmt-wisp")

	if err := store.AddComment(ctx, "mix-cmt-perm", "alice", "on perm"); err != nil {
		t.Fatalf("add comment on perm: %v", err)
	}
	if err := store.AddComment(ctx, "mix-cmt-wisp", "bob", "on wisp"); err != nil {
		t.Fatalf("add comment on wisp: %v", err)
	}

	got, err := store.GetCommentsForIssues(ctx, []string{"mix-cmt-perm", "mix-cmt-wisp", "mix-cmt-unknown"})
	if err != nil {
		t.Fatalf("GetCommentsForIssues: %v", err)
	}

	if cs := got["mix-cmt-perm"]; len(cs) != 1 || cs[0].Text != "on perm" {
		t.Errorf("perm comments: want [on perm], got %+v", cs)
	}
	if cs := got["mix-cmt-wisp"]; len(cs) != 1 || cs[0].Text != "on wisp" {
		t.Errorf("wisp comments: want [on wisp], got %+v", cs)
	}
	if cs, ok := got["mix-cmt-unknown"]; ok && len(cs) != 0 {
		t.Errorf("unknown id: expected no comments, got %+v", cs)
	}
}

func TestGetDependencyRecordsForIssues_MixedWispAndPermanent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Perm-on-perm dependency.
	createPerm(t, ctx, store, "mix-dep-perm-src")
	createPerm(t, ctx, store, "mix-dep-perm-tgt")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mix-dep-perm-src",
		DependsOnID: "mix-dep-perm-tgt",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("add perm dep: %v", err)
	}

	// Wisp-on-perm dependency (dep lives in wisp_dependencies).
	createWisp(t, ctx, store, "mix-dep-wisp-src")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "mix-dep-wisp-src",
		DependsOnID: "mix-dep-perm-tgt",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("add wisp dep: %v", err)
	}

	got, err := store.GetDependencyRecordsForIssues(ctx,
		[]string{"mix-dep-perm-src", "mix-dep-wisp-src"})
	if err != nil {
		t.Fatalf("GetDependencyRecordsForIssues: %v", err)
	}

	if ds := got["mix-dep-perm-src"]; len(ds) != 1 || ds[0].DependsOnID != "mix-dep-perm-tgt" {
		t.Errorf("perm deps: want 1 record targeting mix-dep-perm-tgt, got %+v", ds)
	}
	if ds := got["mix-dep-wisp-src"]; len(ds) != 1 || ds[0].DependsOnID != "mix-dep-perm-tgt" {
		t.Errorf("wisp deps: want 1 record targeting mix-dep-perm-tgt, got %+v", ds)
	}
}

// TestGetLabelsForIssues_ManyIDs exercises the queryBatchSize chunking path
// inside GetLabelsForIssuesInTx — regression guard for partition + label
// fetch crossing the 200-ID boundary.
func TestGetLabelsForIssues_ManyIDs(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const total = 250 // > queryBatchSize (200)
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("many-label-%04d", i)
		ids = append(ids, id)
		if i%5 == 0 {
			createWisp(t, ctx, store, id)
		} else {
			createPerm(t, ctx, store, id)
		}
		if err := store.AddLabel(ctx, id, fmt.Sprintf("lbl-%d", i), "tester"); err != nil {
			t.Fatalf("add label on %s: %v", id, err)
		}
	}

	got, err := store.GetLabelsForIssues(ctx, ids)
	if err != nil {
		t.Fatalf("GetLabelsForIssues: %v", err)
	}

	if len(got) != total {
		t.Errorf("want labels for %d issues, got %d", total, len(got))
	}
	for i, id := range ids {
		want := fmt.Sprintf("lbl-%d", i)
		if labels := got[id]; len(labels) != 1 || labels[0] != want {
			t.Errorf("%s labels: want [%s], got %v", id, want, labels)
		}
	}
}
