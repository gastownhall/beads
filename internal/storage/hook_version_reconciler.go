package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// VersionReconciler returns the inner store's version-marker surface.
//
// It recurses rather than being absent: the accessor set is uniform across the
// chain, so a caller never has to know which decorators a store is wearing.
// What it does NOT do is wrap the result. This decorator's vocabulary is
// on_create / on_update / on_close, and every one of those hands a hook script
// an ISSUE; recording which binary opened the workspace has no issue to name.
// There is a second reason the settings role does not have: this one runs from
// PersistentPreRun on every startup, so a hook fired here would run a user's
// script before every command.
func (h *HookFiringStore) VersionReconciler() (issueops.VersionReconciler, error) {
	return h.inner.VersionReconciler()
}
