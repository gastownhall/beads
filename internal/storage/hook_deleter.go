package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// Deleter returns the inner store's named-row erasure surface.
//
// IT RECURSES UNWRAPPED, for the reason hook_sweeper.go gives: internal/hooks
// publishes on_create, on_update and on_close, and a deletion is none of them.
//
// THE ONE CASE THAT LOOKS LIKE AN EXCEPTION IS NOT. A deletion rewrites its
// neighbors' text, and those rows survive an update — so an on_update hook
// could in principle fire for them. It does not, deliberately: the rewrite is
// a consequence of the deletion rather than an update a user asked for, and a
// `bd delete` of one bead that fired a subprocess per neighbor would be a new
// failure mode. The direct CLI route this role replaces fired no hook for those
// updates either.
//
// So the accessor EXISTS on this decorator — declared, never inherited, which
// the reflection test in role_accessor_decorator_test.go asserts — and adds no
// layer. If a delete event ever joins the hook vocabulary, this file is where
// it lands.
func (h *HookFiringStore) Deleter() (issueops.Deleter, error) {
	return h.inner.Deleter()
}
