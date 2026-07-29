package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/telemetry"
)

// recomputingStore stands in for a concrete store (dolt.DoltStore /
// embeddeddolt.EmbeddedDoltStore) that implements the optional post-merge
// is_blocked repair. The embedded DoltStorage is nil on purpose: nothing here
// may call a method the fake does not define itself.
type recomputingStore struct {
	storage.DoltStorage
	calls []string
}

func (r *recomputingStore) RecomputeBlockedAfterMerge(_ context.Context, fromCommit string) error {
	r.calls = append(r.calls, fromCommit)
	return nil
}

// plainRecomputeStore is a concrete store WITHOUT the optional method, the
// case the else branch at the call site reports instead of silently skipping.
type plainRecomputeStore struct {
	storage.DoltStorage
}

// assertRecomputeReachesRaw runs the shared body: the decorated chain must not
// satisfy the optional interface directly (that is the pre-fix path, and if a
// decorator ever started promoting the method this test would otherwise pass
// vacuously), yet the helper must find it and route the call to raw.
func assertRecomputeReachesRaw(t *testing.T, decorated storage.DoltStorage, raw *recomputingStore) {
	t.Helper()

	if _, direct := decorated.(blockedAfterMergeRecomputer); direct {
		t.Fatalf("decorated store %T satisfies blockedAfterMergeRecomputer directly; "+
			"this test no longer exercises the unwrap path", decorated)
	}

	rs, ok := blockedAfterMergeRecomputerFor(decorated)
	if !ok {
		t.Fatalf("blockedAfterMergeRecomputerFor(%T) = _, false; want the concrete store", decorated)
	}
	if err := rs.RecomputeBlockedAfterMerge(context.Background(), "deadbeef"); err != nil {
		t.Fatalf("RecomputeBlockedAfterMerge: %v", err)
	}
	if len(raw.calls) != 1 || raw.calls[0] != "deadbeef" {
		t.Errorf("concrete store recorded %v; want one call with fromCommit \"deadbeef\"", raw.calls)
	}
}

// The regression test for wy-163oy: cmd/bd/vc.go asserted the optional
// interface on the store straight from getStore(), which is the
// wireStorageDecorators chain, so the post-merge is_blocked recompute was
// silently skipped on every rig carrying a hook layer.
func TestBlockedAfterMergeRecomputerFor_PeelsHookAndTelemetryLayers(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")

	raw := &recomputingStore{}
	decorated := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	// Prove the chain really is two layers deep, so this is not a
	// hooks-only peel wearing a telemetry label.
	hf, ok := decorated.(*storage.HookFiringStore)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *storage.HookFiringStore", decorated)
	}
	if _, ok := hf.Unwrap().(*telemetry.InstrumentedStorage); !ok {
		t.Fatalf("middle decorator: got %T; want *telemetry.InstrumentedStorage", hf.Unwrap())
	}

	assertRecomputeReachesRaw(t, decorated, raw)
}

// The near-universal production shape: hooks on, telemetry off (main.go
// builds a hook runner whenever dbPath != "").
func TestBlockedAfterMergeRecomputerFor_PeelsHookLayerOnly(t *testing.T) {
	clearTelemetryEnv(t)

	raw := &recomputingStore{}
	decorated := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	if _, ok := decorated.(*storage.HookFiringStore); !ok {
		t.Fatalf("outer decorator: got %T; want *storage.HookFiringStore", decorated)
	}

	assertRecomputeReachesRaw(t, decorated, raw)
}

// An undecorated concrete store must keep working unchanged.
func TestBlockedAfterMergeRecomputerFor_UndecoratedStore(t *testing.T) {
	clearTelemetryEnv(t)

	raw := &recomputingStore{}
	decorated := wireStorageDecorators(raw, nil, true)
	if decorated.(*recomputingStore) != raw {
		t.Fatalf("expected the raw store back with hooks and telemetry off; got %T", decorated)
	}

	rs, ok := blockedAfterMergeRecomputerFor(decorated)
	if !ok {
		t.Fatalf("blockedAfterMergeRecomputerFor(raw) = _, false; want the concrete store")
	}
	if err := rs.RecomputeBlockedAfterMerge(context.Background(), ""); err != nil {
		t.Fatalf("RecomputeBlockedAfterMerge: %v", err)
	}
	if len(raw.calls) != 1 {
		t.Errorf("concrete store recorded %v; want one call", raw.calls)
	}
}

// A backend without the method reports false rather than a false positive —
// the call site turns that into a warning instead of a silent skip.
func TestBlockedAfterMergeRecomputerFor_UnsupportedBackend(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")

	decorated := wireStorageDecorators(&plainRecomputeStore{}, hooks.NewRunner("/nonexistent"), false)
	if rs, ok := blockedAfterMergeRecomputerFor(decorated); ok {
		t.Errorf("blockedAfterMergeRecomputerFor(%T) = %T, true; want false", decorated, rs)
	}
}

func TestBlockedAfterMergeRecomputerFor_NilStore(t *testing.T) {
	if rs, ok := blockedAfterMergeRecomputerFor(nil); ok {
		t.Errorf("blockedAfterMergeRecomputerFor(nil) = %T, true; want false", rs)
	}
}
