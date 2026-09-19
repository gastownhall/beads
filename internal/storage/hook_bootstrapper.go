package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// Bootstrapper returns the inner store's identity-seeding surface.
//
// IT RECURSES UNWRAPPED even though the role WRITES, as hook_sweeper.go does:
// the decision is about the hook VOCABULARY, not about whether the role writes.
// This decorator publishes on_create, on_update and on_close, and every one of
// those hands a hook script an ISSUE. A bootstrap creates no issue — it makes a
// database into a workspace.
//
// There is a second reason here that the sweep does not have: `bd init` writes
// .beads/hooks/ AFTER the identity lands, so a hook fired from this accessor
// would run whatever the previous project in that directory left behind, or
// nothing at all. The accessor still EXISTS on the decorator rather than being
// inherited, because the accessor set is uniform across the chain and there is a
// reflection test that says so.
func (h *HookFiringStore) Bootstrapper() (issueops.Bootstrapper, error) {
	return h.inner.Bootstrapper()
}
