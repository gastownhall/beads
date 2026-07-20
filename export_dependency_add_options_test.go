package beads

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Compile-time proof that the exported alias is accepted by the Transaction
// interface's AddDependencyWithOptions. Embedders' bulk graph writers need to
// name this type to set SkipCycleCheck per edge and run one whole-graph
// Transaction.CycleThroughEdges pass before commit (bd-6dnrw.8); before this
// alias it lived only in internal/storage, so the designed batch path was
// uncallable from outside the module (a 67-node/100-edge molecule batch blew
// a 120s deadline on per-edge cycle queries, gascity 2026-07-17).
var _ = func(tx Transaction, dep *types.Dependency) error {
	return tx.AddDependencyWithOptions(context.Background(), dep, "actor", DependencyAddOptions{SkipCycleCheck: true})
}

func TestDependencyAddOptionsIsExported(t *testing.T) {
	if !(DependencyAddOptions{SkipCycleCheck: true}).SkipCycleCheck {
		t.Fatal("SkipCycleCheck must round-trip through the exported alias")
	}
}
