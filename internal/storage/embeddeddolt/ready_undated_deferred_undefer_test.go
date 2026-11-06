//go:build cgo

package embeddeddolt_test

import (
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestReadyWorkUndatedDeferredParentUndeferRevealsChildren pins the other end
// of the undated icebox's contract (GH#7300, AC-5): `bd undefer` writes the
// parent back to open, and the child it held back becomes ready again. The
// withheld-first half is what fails on an unfixed gate; the reveal half guards
// the fix from over-holding.
func TestReadyWorkUndatedDeferredParentUndeferRevealsChildren(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "udu")
	seedUndatedDeferredPlane(t, te, "udu", false)

	hidden, err := te.store.GetReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if ids := sortedIDs(plainIDs(hidden)); !slices.Equal(ids, []string{"udu-free"}) {
		t.Errorf("read before undefer = %v, want [udu-free] (the icebox holds the child back)", ids)
	}

	if err := te.store.UpdateIssue(t.Context(), "udu-parent", map[string]interface{}{
		"status": string(types.StatusOpen),
	}, "tester"); err != nil {
		t.Fatalf("undefer udu-parent: %v", err)
	}

	shown, err := te.store.GetReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork after undefer: %v", err)
	}
	ids := sortedIDs(plainIDs(shown))
	if want := []string{"udu-child", "udu-free", "udu-parent"}; !slices.Equal(ids, want) {
		t.Errorf("read after undefer = %v, want %v (the child must be ready again; the reopened parent too)", ids, want)
	}
}
