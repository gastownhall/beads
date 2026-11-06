//go:build cgo

package embeddeddolt_test

import (
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestReadyWorkUndatedDeferredParentCountsAndTotal pins the combined-probe
// path (probeReadyWorkInTx) for GH#7300: counts pages and totals size the
// ready set from the same deferred-parent rule the plain read uses, so an
// undated icebox must hide both its durable and its ephemeral child from the
// page, from the in-band total, and from CountReadyWork. No row in this
// database carries a defer_until, so only the status can trip the gate.
func TestReadyWorkUndatedDeferredParentCountsAndTotal(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "udc")
	// withWispChild: the durable parent also holds an ephemeral child, the
	// durable-parent/ephemeral-child combination deferredParentEdges routes.
	seedUndatedDeferredPlane(t, te, "udc", true)
	ctx := t.Context()
	// An unrelated free wisp: it keeps the wisp family present in the answer,
	// so the exclusion is proven per family rather than only per row.
	freeWisp := &types.Issue{ID: "udc-fwisp", Title: "unrelated free wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := te.store.CreateIssue(ctx, freeWisp, "tester"); err != nil {
		t.Fatalf("CreateIssue udc-fwisp: %v", err)
	}

	// The expected ready set is written by hand, not read back from another
	// production method: the free durable issue and the free wisp, nothing
	// from the deferred parent's subtree.
	filter := types.WorkFilter{IncludeEphemeral: true}
	page, total, err := te.store.GetReadyWorkWithCountsAndTotal(ctx, filter)
	if err != nil {
		t.Fatalf("GetReadyWorkWithCountsAndTotal: %v", err)
	}
	got := sortedIDs(readyIDs(page))
	if want := []string{"udc-free", "udc-fwisp"}; !slices.Equal(got, want) {
		t.Errorf("page = %v, want %v (both children of the undated deferred parent must be withheld)", got, want)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (the page's independently expected ready-set size)", total)
	}

	count, err := te.store.CountReadyWork(ctx, filter)
	if err != nil {
		t.Fatalf("CountReadyWork: %v", err)
	}
	if count != 2 {
		t.Errorf("CountReadyWork = %d, want 2 (the same ready set the total sizes)", count)
	}
}
