package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// BlockingAnnotator returns the inner store's blocking-decoration surface.
//
// It recurses, like every other accessor on this decorator, rather than being
// absent: the accessor set is uniform across the chain, so a caller never has
// to know which decorators a store is wearing. What it does NOT do is wrap the
// result — reading a page's blocking annotation fires no completion hooks,
// because nothing completed. That is the same statement hook_issue_reader.go,
// hook_relations.go, hook_edgereader.go and hook_counter.go make.
func (h *HookFiringStore) BlockingAnnotator() (issueops.BlockingAnnotator, error) {
	return h.inner.BlockingAnnotator()
}
