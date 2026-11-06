//go:build cgo

package embeddeddolt_test

import (
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestReadyWorkUndatedDeferredParentIncludeDeferred pins the IncludeDeferred
// bypass on the classic stack (issueops/ready_work.go skips the
// deferred-child computation when it is set): an undated icebox holds its
// child out of the default read, and --include-deferred lists the child
// anyway (GH#7300, AC-4).
func TestReadyWorkUndatedDeferredParentIncludeDeferred(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "udi")
	seedUndatedDeferredPlane(t, te, "udi", false)

	// Contrast first: without the bypass the child is withheld — this is the
	// half that fails on an unfixed gate.
	hidden, err := te.store.GetReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if ids := sortedIDs(plainIDs(hidden)); !slices.Equal(ids, []string{"udi-free"}) {
		t.Errorf("default read = %v, want [udi-free] (the icebox holds the child back)", ids)
	}

	// The bypass: the child is listed anyway.
	shown, err := te.store.GetReadyWork(t.Context(), types.WorkFilter{IncludeDeferred: true})
	if err != nil {
		t.Fatalf("GetReadyWork(IncludeDeferred): %v", err)
	}
	ids := sortedIDs(plainIDs(shown))
	if !slices.Contains(ids, "udi-child") {
		t.Errorf("IncludeDeferred read = %v, must still list udi-child", ids)
	}
	if !slices.Contains(ids, "udi-free") {
		t.Errorf("IncludeDeferred read = %v, must still list the unrelated udi-free", ids)
	}
}
