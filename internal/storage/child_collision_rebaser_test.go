package storage

import (
	"context"
	"testing"
)

// ChildCollisionRebaser is an OPTIONAL capability, deliberately kept off
// RemoteStore so adding it does not widen the public beads.RemoteStore alias for
// out-of-tree implementers. The cost of that choice is that the method is no
// longer part of DoltStorage, so it does NOT promote through a store decorator:
// a direct type assertion on a decorated store misses it, and every caller must
// reach the capability through UnwrapStore. These tests pin that contract, since
// getting it wrong degrades silently — `bd dolt rebase` would report the backend
// cannot rebase, on a backend that can.

// rebaseCapableStore stands in for a concrete store. Embedding the DoltStorage
// interface satisfies the method set at compile time without stubbing all of it;
// only RebaseRemote is ever called here.
type rebaseCapableStore struct {
	DoltStorage
	calls int
}

func (s *rebaseCapableStore) RebaseRemote(_ context.Context, _ string, localDominates bool) (*RebaseReport, error) {
	s.calls++
	direction := "remote-dominates"
	if localDominates {
		direction = "local-dominates"
	}
	return &RebaseReport{Direction: direction, CountersSet: map[string]int{}}, nil
}

// plainStore has no rebase capability at all.
type plainStore struct{ DoltStorage }

func TestChildCollisionRebaserReachedThroughDecorator(t *testing.T) {
	inner := &rebaseCapableStore{}
	dec := NewHookFiringStore(inner, nil)

	// Setup check: the decorator must NOT satisfy the capability directly.
	// RebaseRemote is not on DoltStorage, so the embedded interface does not
	// promote it. If this ever starts passing, the capability has leaked back
	// onto the engine interface and the UnwrapStore below is masking it.
	if _, ok := any(dec).(ChildCollisionRebaser); ok {
		t.Fatal("decorator satisfies ChildCollisionRebaser directly — the capability is on DoltStorage again, widening the public surface")
	}

	rebaser, ok := UnwrapStore(dec).(ChildCollisionRebaser)
	if !ok {
		t.Fatal("expected a decorated rebase-capable store to be reached via UnwrapStore")
	}
	report, err := rebaser.RebaseRemote(context.Background(), "origin", true)
	if err != nil {
		t.Fatalf("RebaseRemote through the unwrapped decorator: %v", err)
	}
	if report.Direction != "local-dominates" {
		t.Errorf("report.Direction = %q, want %q — the call did not reach the inner store intact", report.Direction, "local-dominates")
	}
	if inner.calls != 1 {
		t.Errorf("inner store called %d times, want 1", inner.calls)
	}
}

func TestChildCollisionRebaserAbsentOnIncapableStore(t *testing.T) {
	if _, ok := UnwrapStore(&plainStore{}).(ChildCollisionRebaser); ok {
		t.Fatal("a store without RebaseRemote must not report the capability")
	}
}
