package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// BlockedLister returns the inner store's blocked-listing surface.
//
// It recurses, like every other accessor on this decorator, and does not wrap
// the result: listing the blocked issues fires no completion hook, because
// nothing completed (hook_ready_counter.go makes the same statement).
func (h *HookFiringStore) BlockedLister() (issueops.BlockedLister, error) {
	return h.inner.BlockedLister()
}
