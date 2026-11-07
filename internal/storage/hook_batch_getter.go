package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// BatchGetter returns the inner store's batch-read surface.
//
// It recurses, like every other accessor on this decorator, rather than being
// absent: the accessor set is uniform across the chain, so a caller never has
// to know which decorators a store is wearing. What it does NOT do is wrap the
// result — reading issues by id fires no completion hooks, because nothing
// completed. That is the same statement hook_graph_counter.go makes, and it is
// why this file is three lines.
func (h *HookFiringStore) BatchGetter() (issueops.BatchGetter, error) {
	return h.inner.BatchGetter()
}
