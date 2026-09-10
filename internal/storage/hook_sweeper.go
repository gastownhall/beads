package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// Sweeper returns the inner store's bulk-clearance surface.
//
// IT RECURSES UNWRAPPED, and this is the one WRITE role that does. There is no
// on_delete hook — internal/hooks publishes on_create, on_update and on_close,
// and firing on_update for a row that no longer exists would hand a user's
// script an id it cannot read back — and a hook script is handed an ISSUE where
// a sweep's result carries counts.
//
// So the accessor EXISTS on this decorator — declared, never inherited, which
// the reflection test in role_accessor_decorator_test.go asserts — and adds no
// layer. If a delete event ever joins the hook vocabulary, this file is where
// it lands.
func (h *HookFiringStore) Sweeper() (issueops.Sweeper, error) {
	return h.inner.Sweeper()
}
