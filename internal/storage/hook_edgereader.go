package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// EdgeReader returns the inner store's stored-edge surface.
//
// It recurses, like every other accessor on this decorator, rather than being
// absent: the accessor set is uniform across the chain, so a caller never has
// to know which decorators a store is wearing. What it does NOT do is wrap the
// result — reading edges fires no completion hooks, because nothing completed.
// That is the same statement hook_issue_reader.go, hook_relations.go and
// hook_counter.go make.
func (h *HookFiringStore) EdgeReader() (issueops.EdgeReader, error) {
	return h.inner.EdgeReader()
}
