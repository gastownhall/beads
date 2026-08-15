package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// GraphCounter returns the inner store's edge-count surface.
//
// It recurses, like every other accessor on this decorator, rather than being
// absent: the accessor set is uniform across the chain, so a caller never has
// to know which decorators a store is wearing. What it does NOT do is wrap the
// result — counting edges fires no completion hooks, because nothing completed.
// That is the same statement hook_counter.go and hook_tree_walker.go make, and
// it is why this file is three lines.
func (h *HookFiringStore) GraphCounter() (issueops.GraphCounter, error) {
	return h.inner.GraphCounter()
}
