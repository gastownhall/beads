package storage

import (
	"github.com/steveyegge/beads/memoryops"
)

// Memories returns the inner store's persistent-memory surface.
//
// IT RECURSES UNWRAPPED, and that is a decision rather than an omission.
// Remember and Forget are writes, so the reflex is to reach for hook_commenter
// — but this decorator's whole vocabulary is on_create, on_update and on_close,
// and every one of them hands a hook script an ISSUE. There is no on_remember,
// a memory has no id a script could read back, and inventing an event so this
// file could fire one would be a hook-vocabulary proposal wearing a role
// commit's clothes. This is the Sweeper case, not the Commenter case.
//
// So the accessor EXISTS on this decorator — declared, never inherited, which
// the reflection test in role_accessor_decorator_test.go asserts — and adds no
// layer. If a memory event ever joins the hook vocabulary, this file is where
// it lands.
func (h *HookFiringStore) Memories() (memoryops.Memories, error) {
	return h.inner.Memories()
}
