package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// WorkspaceConfig returns the inner store's workspace-settings surface.
//
// It recurses like every other accessor on this decorator rather than being
// absent, and does NOT wrap the result. This decorator's vocabulary is
// on_create / on_update / on_close, and every one of those hands the hook script
// an ISSUE; a settings write has no issue to name, so there is nothing this
// layer could fire that a hook script could read. The legacy config path
// (HookFiringStore inherits SetConfig and DeleteConfig unchanged) fires nothing
// either, so wrapping here would not restore a hook — it would invent one.
func (h *HookFiringStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return h.inner.WorkspaceConfig()
}
