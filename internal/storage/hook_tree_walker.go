package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// TreeWalker returns the inner store's dependency-tree surface.
//
// It recurses, like every other accessor on this decorator, rather than being
// absent: the accessor set is uniform across the chain, so a caller never has to
// know which decorators a store is wearing. What it does NOT do is wrap the
// result — walking a tree fires no completion hooks, because nothing completed.
// That is the same statement hook_cycle_detector.go and hook_counter.go make.
func (h *HookFiringStore) TreeWalker() (issueops.TreeWalker, error) {
	return h.inner.TreeWalker()
}
