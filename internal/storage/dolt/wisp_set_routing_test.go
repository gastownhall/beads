package dolt

import (
	"database/sql"
	"reflect"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestWispIDSetInTx_HardGate covers the D2 "mixed-ID routing" hard gate
// from be-nu4.2.1. It seeds one permanent issue and one active wisp, each
// tagged with a distinct label, and verifies that the refactored helpers
// GetLabelsForIssuesInTx and GetIssuesByIDsInTx route each ID to the correct
// underlying table when given a mixed input slice.
//
// Regression direction: a bug in the wisp-set construction or partitioning
// would cause a wisp ID to be queried against `labels`/`issues` (returning
// empty) or a permanent ID to be queried against `wisp_labels`/`wisps`
// (also returning empty). The test asserts full round-trip label + issue
// hydration across both tables.
func TestWispIDSetInTx_HardGate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Seed: one permanent issue tagged "foo".
	perm := &types.Issue{
		ID:        "wispset-perm-1",
		Title:     "perm issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, perm, "tester"); err != nil {
		t.Fatalf("create perm: %v", err)
	}
	if err := store.AddLabel(ctx, perm.ID, "foo", "tester"); err != nil {
		t.Fatalf("add label to perm: %v", err)
	}

	// Seed: one active wisp tagged "bar".
	wisp := &types.Issue{
		Title:     "wisp issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	if err := store.AddLabel(ctx, wisp.ID, "bar", "tester"); err != nil {
		t.Fatalf("add label to wisp: %v", err)
	}
	if !store.isActiveWisp(ctx, wisp.ID) {
		t.Fatalf("expected %q to be active wisp", wisp.ID)
	}

	// Run assertions inside one read tx so the WispIDSetInTx result is
	// visible alongside the partitioned reads.
	ids := []string{perm.ID, wisp.ID}
	if err := store.withReadTx(ctx, func(tx *sql.Tx) error {
		// WispIDSetInTx should contain only the wisp ID, not the perm.
		set, err := issueops.WispIDSetInTx(ctx, tx, ids)
		if err != nil {
			t.Fatalf("WispIDSetInTx: %v", err)
		}
		if _, ok := set[wisp.ID]; !ok {
			t.Errorf("WispIDSetInTx missing wisp %q, set=%v", wisp.ID, setKeys(set))
		}
		if _, ok := set[perm.ID]; ok {
			t.Errorf("WispIDSetInTx contains permanent %q (should not), set=%v", perm.ID, setKeys(set))
		}

		// GetLabelsForIssuesInTx: each ID gets its own label, not the
		// other's; nil wispSet exercises the internal build path.
		labelMap, err := issueops.GetLabelsForIssuesInTx(ctx, tx, ids, nil)
		if err != nil {
			t.Fatalf("GetLabelsForIssuesInTx (nil set): %v", err)
		}
		if got, want := labelMap[perm.ID], []string{"foo"}; !reflect.DeepEqual(got, want) {
			t.Errorf("perm labels: got %v, want %v", got, want)
		}
		if got, want := labelMap[wisp.ID], []string{"bar"}; !reflect.DeepEqual(got, want) {
			t.Errorf("wisp labels: got %v, want %v", got, want)
		}

		// Same call with caller-provided set must produce identical results.
		labelMap2, err := issueops.GetLabelsForIssuesInTx(ctx, tx, ids, set)
		if err != nil {
			t.Fatalf("GetLabelsForIssuesInTx (caller set): %v", err)
		}
		if !reflect.DeepEqual(labelMap2, labelMap) {
			t.Errorf("label map differs when caller provides set: %v vs %v", labelMap2, labelMap)
		}

		// GetIssuesByIDsInTx: both rows come back fully hydrated, labels
		// attached to the matching IDs.
		issues, err := issueops.GetIssuesByIDsInTx(ctx, tx, ids, nil)
		if err != nil {
			t.Fatalf("GetIssuesByIDsInTx (nil set): %v", err)
		}
		if len(issues) != 2 {
			t.Fatalf("GetIssuesByIDsInTx returned %d issues, want 2", len(issues))
		}
		issueByID := map[string]*types.Issue{}
		for _, iss := range issues {
			issueByID[iss.ID] = iss
		}
		gotPerm := issueByID[perm.ID]
		gotWisp := issueByID[wisp.ID]
		if gotPerm == nil || gotWisp == nil {
			t.Fatalf("GetIssuesByIDsInTx missing ids: got %v", issueByID)
		}
		if !reflect.DeepEqual(gotPerm.Labels, []string{"foo"}) {
			t.Errorf("perm issue labels: got %v, want [foo]", gotPerm.Labels)
		}
		if !reflect.DeepEqual(gotWisp.Labels, []string{"bar"}) {
			t.Errorf("wisp issue labels: got %v, want [bar]", gotWisp.Labels)
		}
		if gotWisp.Ephemeral != true {
			t.Errorf("wisp issue Ephemeral=%v, want true", gotWisp.Ephemeral)
		}
		return nil
	}); err != nil {
		t.Fatalf("read tx: %v", err)
	}
}

// TestWispIDSetInTx_Empty verifies the helpers handle empty inputs without
// issuing a wisp-set query (GetLabelsForIssuesInTx / GetIssuesByIDsInTx both
// short-circuit on empty input).
func TestWispIDSetInTx_Empty(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.withReadTx(ctx, func(tx *sql.Tx) error {
		labelMap, err := issueops.GetLabelsForIssuesInTx(ctx, tx, nil, nil)
		if err != nil {
			t.Fatalf("GetLabelsForIssuesInTx empty: %v", err)
		}
		if len(labelMap) != 0 {
			t.Errorf("expected empty map, got %v", labelMap)
		}
		issues, err := issueops.GetIssuesByIDsInTx(ctx, tx, nil, nil)
		if err != nil {
			t.Fatalf("GetIssuesByIDsInTx empty: %v", err)
		}
		if len(issues) != 0 {
			t.Errorf("expected no issues, got %v", issues)
		}

		// And the wisp set query itself should return cleanly on an empty
		// wisps table.
		set, err := issueops.WispIDSetInTx(ctx, tx, nil)
		if err != nil {
			t.Fatalf("WispIDSetInTx empty: %v", err)
		}
		if len(set) != 0 {
			t.Errorf("expected empty wisp set, got %v", set)
		}
		return nil
	}); err != nil {
		t.Fatalf("read tx: %v", err)
	}
}

func setKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
