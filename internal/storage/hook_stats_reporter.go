package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// StatsReporter returns the inner store's summary-statistics surface.
//
// It recurses, like every other accessor on this decorator, rather than being
// absent: the accessor set is uniform across the chain, so a caller never has
// to know which decorators a store is wearing. What it does NOT do is wrap the
// result — reporting fires no completion hooks, because nothing completed.
// That is the same statement hook_counter.go and hook_issue_reader.go make.
func (h *HookFiringStore) StatsReporter() (issueops.StatsReporter, error) {
	return h.inner.StatsReporter()
}
