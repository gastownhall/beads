package dolt

import (
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// setKeys is defined in wisp_set_routing_test.go (same package).

// TestWispIDSetInTx_Scoped_ReturnsOnlyInputIDs is the "lock the fix in"
// regression test for be-rgm (maphew on PR #3453). It seeds N wisps but
// passes only a single ID through WispIDSetInTx; the returned set must
// contain exactly that one ID. A revert to the unscoped
// `SELECT id FROM wisps` fallback would cause the returned set to
// include all N seeded wisps, failing this test.
func TestWispIDSetInTx_Scoped_ReturnsOnlyInputIDs(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Seed five active wisps. Only one will be passed to WispIDSetInTx.
	wispIDs := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		iss := &types.Issue{
			Title:     fmt.Sprintf("wisp %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create wisp %d: %v", i, err)
		}
		wispIDs = append(wispIDs, iss.ID)
	}

	target := wispIDs[2]

	if err := store.withReadTx(ctx, func(tx *sql.Tx) error {
		set, err := issueops.WispIDSetInTx(ctx, tx, []string{target})
		if err != nil {
			return fmt.Errorf("WispIDSetInTx: %w", err)
		}
		if len(set) != 1 {
			t.Errorf("scoped set size: got %d (%v), want 1", len(set), setKeys(set))
		}
		if _, ok := set[target]; !ok {
			t.Errorf("scoped set missing target %q, got %v", target, setKeys(set))
		}
		for _, other := range wispIDs {
			if other == target {
				continue
			}
			if _, leaked := set[other]; leaked {
				t.Errorf("scoped set leaked unrequested wisp %q (regression: reverted to unscoped SELECT?), got %v",
					other, setKeys(set))
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read tx: %v", err)
	}
}

// TestWispIDSetInTx_Scoped_AcrossBatchBoundary verifies the scoped helper
// handles input larger than queryBatchSize (200) by iterating batched
// IN-clauses. Seeds 250 wisps, passes all 250, expects every ID back.
func TestWispIDSetInTx_Scoped_AcrossBatchBoundary(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const n = 250
	wispIDs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		iss := &types.Issue{
			Title:     fmt.Sprintf("batch-wisp %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create wisp %d: %v", i, err)
		}
		wispIDs = append(wispIDs, iss.ID)
	}

	if err := store.withReadTx(ctx, func(tx *sql.Tx) error {
		set, err := issueops.WispIDSetInTx(ctx, tx, wispIDs)
		if err != nil {
			return fmt.Errorf("WispIDSetInTx: %w", err)
		}
		if len(set) != n {
			t.Errorf("batched set size: got %d, want %d (keys=%v)", len(set), n, setKeys(set))
		}
		for _, id := range wispIDs {
			if _, ok := set[id]; !ok {
				t.Errorf("batched set missing %q", id)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read tx: %v", err)
	}
}

// TestWispIDSetInTx_Scoped_UnknownIDsReturnEmpty verifies the helper
// returns a clean empty set when every input ID is absent from the
// wisps table (not a wisp and not an error condition).
func TestWispIDSetInTx_Scoped_UnknownIDsReturnEmpty(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Seed one wisp to prove the query would find something if it was unscoped.
	bait := &types.Issue{
		Title:     "bait wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, bait, "tester"); err != nil {
		t.Fatalf("create bait: %v", err)
	}

	if err := store.withReadTx(ctx, func(tx *sql.Tx) error {
		set, err := issueops.WispIDSetInTx(ctx, tx, []string{"nonexistent-a", "nonexistent-b"})
		if err != nil {
			return fmt.Errorf("WispIDSetInTx: %w", err)
		}
		if len(set) != 0 {
			t.Errorf("unknown-ids set: got %v, want empty", setKeys(set))
		}
		if _, leaked := set[bait.ID]; leaked {
			t.Errorf("scoped set leaked bait wisp %q (regression: reverted to unscoped SELECT?)", bait.ID)
		}
		return nil
	}); err != nil {
		t.Fatalf("read tx: %v", err)
	}
}

// TestGetLabelsForIssuesInTx_SmallInputLargeWispTable exercises the
// end-to-end hydration path maphew flagged: a small input ID batch
// against a large wisps table. The scoped WispIDSetInTx internal build
// (nil wispSet) must still return the correct labels for the one
// requested wisp, without being disturbed by the N-1 other wisps that
// happen to share the table.
func TestGetLabelsForIssuesInTx_SmallInputLargeWispTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Seed one permanent issue with a label.
	perm := &types.Issue{
		ID:        "smallN-perm-1",
		Title:     "perm",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, perm, "tester"); err != nil {
		t.Fatalf("create perm: %v", err)
	}
	if err := store.AddLabel(ctx, perm.ID, "perm-label", "tester"); err != nil {
		t.Fatalf("add perm label: %v", err)
	}

	// Seed one target wisp with its own label.
	target := &types.Issue{
		Title:     "target wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, target, "tester"); err != nil {
		t.Fatalf("create target wisp: %v", err)
	}
	if err := store.AddLabel(ctx, target.ID, "target-label", "tester"); err != nil {
		t.Fatalf("add target label: %v", err)
	}

	// Seed a batch of noise wisps with labels that must NOT leak into
	// the result — "large wisp table" relative to the small input.
	const noiseCount = 20
	noiseIDs := make([]string, 0, noiseCount)
	for i := 0; i < noiseCount; i++ {
		iss := &types.Issue{
			Title:     fmt.Sprintf("noise wisp %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create noise %d: %v", i, err)
		}
		if err := store.AddLabel(ctx, iss.ID, fmt.Sprintf("noise-%d", i), "tester"); err != nil {
			t.Fatalf("add noise label %d: %v", i, err)
		}
		noiseIDs = append(noiseIDs, iss.ID)
	}

	input := []string{perm.ID, target.ID}

	if err := store.withReadTx(ctx, func(tx *sql.Tx) error {
		// Nil wispSet exercises the internal scoped build on input IDs.
		labelMap, err := issueops.GetLabelsForIssuesInTx(ctx, tx, input, nil)
		if err != nil {
			return fmt.Errorf("GetLabelsForIssuesInTx: %w", err)
		}
		if got, want := labelMap[perm.ID], []string{"perm-label"}; !reflect.DeepEqual(got, want) {
			t.Errorf("perm labels: got %v, want %v", got, want)
		}
		if got, want := labelMap[target.ID], []string{"target-label"}; !reflect.DeepEqual(got, want) {
			t.Errorf("target labels: got %v, want %v", got, want)
		}
		// Noise IDs were never requested — the map should not contain them.
		for _, n := range noiseIDs {
			if _, leaked := labelMap[n]; leaked {
				t.Errorf("result leaked noise wisp %q (regression: nil-set build was unscoped?)", n)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read tx: %v", err)
	}
}

// TestGetIssuesByIDsInTx_SmallInputLargeWispTable is the issue-hydration
// mirror of TestGetLabelsForIssuesInTx_SmallInputLargeWispTable. It
// verifies the fast-path caller still produces correct hydrated issues
// when the wisp set was built with a scoped internal query.
func TestGetIssuesByIDsInTx_SmallInputLargeWispTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	perm := &types.Issue{
		ID:        "smallN-issue-perm",
		Title:     "perm",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, perm, "tester"); err != nil {
		t.Fatalf("create perm: %v", err)
	}

	target := &types.Issue{
		Title:     "target wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, target, "tester"); err != nil {
		t.Fatalf("create target wisp: %v", err)
	}

	const noiseCount = 20
	for i := 0; i < noiseCount; i++ {
		iss := &types.Issue{
			Title:     fmt.Sprintf("noise wisp %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create noise %d: %v", i, err)
		}
	}

	input := []string{perm.ID, target.ID}

	if err := store.withReadTx(ctx, func(tx *sql.Tx) error {
		issues, err := issueops.GetIssuesByIDsInTx(ctx, tx, input, nil)
		if err != nil {
			return fmt.Errorf("GetIssuesByIDsInTx: %w", err)
		}
		if len(issues) != 2 {
			t.Fatalf("got %d issues, want 2: %v", len(issues), issues)
		}
		gotIDs := make([]string, 0, len(issues))
		for _, iss := range issues {
			gotIDs = append(gotIDs, iss.ID)
		}
		sort.Strings(gotIDs)
		want := []string{perm.ID, target.ID}
		sort.Strings(want)
		if !reflect.DeepEqual(gotIDs, want) {
			t.Errorf("issue IDs: got %v, want %v", gotIDs, want)
		}
		return nil
	}); err != nil {
		t.Fatalf("read tx: %v", err)
	}
}
