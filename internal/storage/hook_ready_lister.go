package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// ReadyLister returns the inner store's ready-listing surface.
//
// It recurses rather than wrapping, for the reason hook_ready_counter.go gives:
// listing ready work fires no completion hooks, because nothing completed. The
// accessor still exists on this decorator so the accessor set is uniform across
// the chain.
func (h *HookFiringStore) ReadyLister() (issueops.ReadyLister, error) {
	return h.inner.ReadyLister()
}
