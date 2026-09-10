package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// CycleDetector returns the inner store's cycle-report surface.
//
// It recurses, like every other accessor on this decorator, rather than being
// absent: the accessor set is uniform across the chain, so a caller never has to
// know which decorators a store is wearing. What it does NOT do is wrap the
// result — detecting a cycle fires no completion hooks, because nothing
// completed. That is the same statement hook_counter.go makes.
func (h *HookFiringStore) CycleDetector() (issueops.CycleDetector, error) {
	return h.inner.CycleDetector()
}
