package flatfile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// Oracle for every assertion in this file: the shared SQL reference
// implementation in internal/storage/merge_slot.go (MergeSlotCreateImpl,
// MergeSlotAcquireImpl, MergeSlotReleaseImpl, MergeSlotCheckImpl).

func TestMergeSlotUncreatedIsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.MergeSlotCheck(ctx); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("MergeSlotCheck on uncreated slot: err = %v, want ErrNotFound", err)
	}
	if _, err := s.MergeSlotAcquire(ctx, "agent-a", "a", false); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("MergeSlotAcquire on uncreated slot: err = %v, want ErrNotFound", err)
	}
	if err := s.MergeSlotRelease(ctx, "agent-a", "a"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("MergeSlotRelease on uncreated slot: err = %v, want ErrNotFound", err)
	}
}

func TestMergeSlotCreateIdempotentPreservesHolder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.MergeSlotCreate(ctx, "a"); err != nil {
		t.Fatalf("MergeSlotCreate: %v", err)
	}
	res, err := s.MergeSlotAcquire(ctx, "agent-a", "a", false)
	if err != nil || !res.Acquired {
		t.Fatalf("MergeSlotAcquire = (%+v, %v), want acquired", res, err)
	}
	if _, err := s.MergeSlotAcquire(ctx, "agent-b", "b", true); err != nil {
		t.Fatalf("MergeSlotAcquire wait: %v", err)
	}

	// Re-running create must return the existing slot untouched.
	if _, err := s.MergeSlotCreate(ctx, "a"); err != nil {
		t.Fatalf("second MergeSlotCreate: %v", err)
	}
	status, err := s.MergeSlotCheck(ctx)
	if err != nil {
		t.Fatalf("MergeSlotCheck: %v", err)
	}
	if status.Holder != "agent-a" {
		t.Errorf("holder after re-create = %q, want agent-a", status.Holder)
	}
	if len(status.Waiters) != 1 || status.Waiters[0] != "agent-b" {
		t.Errorf("waiters after re-create = %v, want [agent-b]", status.Waiters)
	}
	if status.Available {
		t.Error("slot reported available while held")
	}
}

func TestMergeSlotDefaultIDUsesPrefix(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetConfig(ctx, "issue_prefix", "gt"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	issue, err := s.MergeSlotCreate(ctx, "a")
	if err != nil {
		t.Fatalf("MergeSlotCreate: %v", err)
	}
	if issue.ID != "gt-merge-slot" {
		t.Errorf("slot ID = %q, want gt-merge-slot", issue.ID)
	}
	status, err := s.MergeSlotCheck(ctx)
	if err != nil {
		t.Fatalf("MergeSlotCheck: %v", err)
	}
	if status.SlotID != "gt-merge-slot" {
		t.Errorf("status.SlotID = %q, want gt-merge-slot", status.SlotID)
	}
}

func TestMergeSlotAcquireWaitQueues(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.MergeSlotCreate(ctx, "a"); err != nil {
		t.Fatalf("MergeSlotCreate: %v", err)
	}
	if _, err := s.MergeSlotAcquire(ctx, "", "a", false); err == nil {
		t.Error("acquire with empty holder: want error, got nil")
	}
	if _, err := s.MergeSlotAcquire(ctx, "agent-a", "a", false); err != nil {
		t.Fatalf("MergeSlotAcquire: %v", err)
	}

	res, err := s.MergeSlotAcquire(ctx, "agent-b", "b", true)
	if err != nil {
		t.Fatalf("MergeSlotAcquire wait: %v", err)
	}
	if res.Acquired {
		t.Error("wait acquire on held slot reported Acquired")
	}
	if !res.Waiting || res.Position != 1 {
		t.Errorf("wait result = Waiting=%v Position=%d, want Waiting=true Position=1", res.Waiting, res.Position)
	}
	if res.Holder != "agent-a" {
		t.Errorf("wait result holder = %q, want agent-a", res.Holder)
	}

	// Re-waiting must not duplicate the queue entry.
	res, err = s.MergeSlotAcquire(ctx, "agent-b", "b", true)
	if err != nil {
		t.Fatalf("second wait acquire: %v", err)
	}
	if !res.Waiting || res.Position != 1 {
		t.Errorf("repeat wait = Waiting=%v Position=%d, want Waiting=true Position=1", res.Waiting, res.Position)
	}
	res, err = s.MergeSlotAcquire(ctx, "agent-c", "c", true)
	if err != nil {
		t.Fatalf("third wait acquire: %v", err)
	}
	if res.Position != 2 {
		t.Errorf("agent-c position = %d, want 2", res.Position)
	}
	status, _ := s.MergeSlotCheck(ctx)
	if len(status.Waiters) != 2 {
		t.Errorf("waiters = %v, want [agent-b agent-c]", status.Waiters)
	}

	// wait=false on a held slot must not queue.
	if _, err := s.MergeSlotAcquire(ctx, "agent-d", "d", false); err != nil {
		t.Fatalf("no-wait acquire: %v", err)
	}
	status, _ = s.MergeSlotCheck(ctx)
	if len(status.Waiters) != 2 {
		t.Errorf("waiters after no-wait acquire = %v, want 2 entries", status.Waiters)
	}
}

func TestMergeSlotReleaseHolderSemantics(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.MergeSlotCreate(ctx, "a"); err != nil {
		t.Fatalf("MergeSlotCreate: %v", err)
	}
	if _, err := s.MergeSlotAcquire(ctx, "agent-a", "a", false); err != nil {
		t.Fatalf("MergeSlotAcquire: %v", err)
	}

	// Non-matching holder errors and leaves the slot held.
	err := s.MergeSlotRelease(ctx, "agent-b", "b")
	if err == nil || !strings.Contains(err.Error(), "slot held by agent-a, not agent-b") {
		t.Errorf("mismatched release err = %v, want 'slot held by agent-a, not agent-b'", err)
	}
	status, _ := s.MergeSlotCheck(ctx)
	if status.Holder != "agent-a" {
		t.Errorf("holder after mismatched release = %q, want agent-a", status.Holder)
	}

	// Empty holder is a force-release.
	if err := s.MergeSlotRelease(ctx, "", "admin"); err != nil {
		t.Fatalf("force release: %v", err)
	}
	status, _ = s.MergeSlotCheck(ctx)
	if status.Holder != "" || !status.Available {
		t.Errorf("slot after force release = %+v, want available", status)
	}

	// Releasing a free slot with a matching (empty) holder is idempotent.
	if err := s.MergeSlotRelease(ctx, "", "admin"); err != nil {
		t.Errorf("repeat force release: %v", err)
	}
}
