package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// ReadyCounter returns the inner store's ready-count surface.
//
// It recurses, like every other accessor on this decorator, rather than being
// absent: the accessor set is uniform across the chain, so a caller never has
// to know which decorators a store is wearing. What it does NOT do is wrap the
// result — sizing the ready set fires no completion hooks, because nothing
// completed. That is the same statement hook_counter.go and
// hook_issue_reader.go make, and it is why this file is three lines.
func (h *HookFiringStore) ReadyCounter() (issueops.ReadyCounter, error) {
	return h.inner.ReadyCounter()
}
