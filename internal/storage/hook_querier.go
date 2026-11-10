package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// Querier returns the inner store's boolean-query surface.
//
// It recurses, like every other accessor on this decorator, rather than being
// absent: the accessor set is uniform across the chain, so a caller never has
// to know which decorators a store is wearing. What it does NOT do is wrap the
// result — a query fires no completion hooks, because nothing completed. That
// is the same statement hook_issue_reader.go and hook_counter.go make.
func (h *HookFiringStore) Querier() (issueops.Querier, error) {
	return h.inner.Querier()
}
