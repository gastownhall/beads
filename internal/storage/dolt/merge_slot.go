package dolt

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// MergeSlotCreate creates the merge slot bead for the current rig.
// Idempotent: returns the existing slot if one already exists.
func (s *DoltStore) MergeSlotCreate(ctx context.Context, actor string) (*types.Issue, error) {
	return storage.MergeSlotCreateImpl(ctx, s, actor)
}

// MergeSlotCheck returns the current status of the merge slot.
func (s *DoltStore) MergeSlotCheck(ctx context.Context) (*storage.MergeSlotStatus, error) {
	return storage.MergeSlotCheckImpl(ctx, s)
}

// MergeSlotAcquire attempts to acquire the merge slot atomically.
// When wait is true and the slot is held, the caller is added to the waiters queue.
func (s *DoltStore) MergeSlotAcquire(ctx context.Context, holder, actor string, wait bool) (*storage.MergeSlotResult, error) {
	return storage.MergeSlotAcquireImpl(ctx, s, holder, actor, wait)
}

// MergeSlotRelease releases the merge slot, clearing the holder.
// If holder is non-empty it is verified against the current holder before releasing.
func (s *DoltStore) MergeSlotRelease(ctx context.Context, holder, actor string) error {
	return storage.MergeSlotReleaseImpl(ctx, s, holder, actor)
}
