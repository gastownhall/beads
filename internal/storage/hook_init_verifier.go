package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// InitVerifier returns the inner store's identity-read surface.
//
// Reads fire no hooks, so this recurses and hands back the inner surface
// unwrapped, as hook_issue_reader.go and hook_counter.go do: there is no
// completion to report.
func (h *HookFiringStore) InitVerifier() (issueops.InitVerifier, error) {
	return h.inner.InitVerifier()
}
