package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/telemetry"
)

// stubChainStore is a stand-in for a concrete DoltStorage. The embedded
// interface exists only for decorator identity tests; ActiveDatabaseSize is
// implemented explicitly so the sizing-capability test never reaches a nil
// promoted method.
type stubChainStore struct {
	storage.DoltStorage
	databaseSize int64
}

func (s *stubChainStore) ActiveDatabaseSize(context.Context) (int64, error) {
	return s.databaseSize, nil
}

// clearTelemetryEnv is defined once for the package, in
// command_telemetry_test.go; it unsets every BD_OTEL_* / OTEL_* variable
// telemetry.Enabled or the SDK looks at, so each test starts from a known
// baseline.

func TestWireStorageDecorators_NilStorePassesThrough(t *testing.T) {
	if got := wireStorageDecorators(nil, hooks.NewRunner("/nonexistent"), false); got != nil {
		t.Errorf("wireStorageDecorators(nil, ...) = %v; want nil", got)
	}
}

func TestWireStorageDecorators_TelemetryOff_HookOn(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	hf, ok := got.(*storage.HookFiringStore)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *storage.HookFiringStore", got)
	}
	ext, ok := hf.Unwrap().(*externaldeps.Store)
	if !ok {
		t.Fatalf("second decorator: got %T; want *externaldeps.Store", hf.Unwrap())
	}
	if inner := ext.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("external dependency policy should wrap raw store directly when telemetry off; got %T", inner)
	}
}

// Asserts the full HookFiringStore → externaldeps.Store → InstrumentedStorage
// → raw chain that the rest of bd depends on for storage spans + bd.storage.* / bd.issue.count
// metrics. This is the regression test for the original PR-3475 bug, where
// WrapStorage was implemented but never called.
func TestWireStorageDecorators_TelemetryOn_HookOn(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	hf, ok := got.(*storage.HookFiringStore)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *storage.HookFiringStore", got)
	}
	ext, ok := hf.Unwrap().(*externaldeps.Store)
	if !ok {
		t.Fatalf("second decorator: got %T; want *externaldeps.Store", hf.Unwrap())
	}
	inst, ok := ext.Unwrap().(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatalf("middle decorator: got %T; want *telemetry.InstrumentedStorage", ext.Unwrap())
	}
	if inner := inst.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("InstrumentedStorage.Unwrap() should return raw store; got %T", inner)
	}

	if peeled := storage.UnwrapStore(got); peeled.(*stubChainStore) != raw {
		t.Errorf("storage.UnwrapStore should peel both decorator layers; got %T", peeled)
	}
}

func TestDoltBackupSizeUnwrapsStorageDecorators(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{databaseSize: 99}
	wrapped := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	size, available, err := doltBackupSizeForStore(t.Context(), wrapped)
	if err != nil {
		t.Fatalf("doltBackupSizeForStore: %v", err)
	}
	if !available || size != 99 {
		t.Fatalf("doltBackupSizeForStore = (%d, %v), want (99, true)", size, available)
	}
}

func TestGCStoreSizeUnwrapsStorageDecorators(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{databaseSize: 99}
	wrapped := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	if got := storeSizeBytesForStore(t.Context(), wrapped); got != 99 {
		t.Fatalf("storeSizeBytesForStore = %d, want 99", got)
	}
}

func TestWireStorageDecorators_TelemetryOn_HookDisabled(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), true)

	ext, ok := got.(*externaldeps.Store)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *externaldeps.Store", got)
	}
	inst, ok := ext.Unwrap().(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatalf("expected *telemetry.InstrumentedStorage when hooks disabled; got %T", ext.Unwrap())
	}
	if inner := inst.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("InstrumentedStorage.Unwrap() should return raw store; got %T", inner)
	}
}

func TestWireStorageDecorators_TelemetryOff_HookDisabled(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), true)
	ext, ok := got.(*externaldeps.Store)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *externaldeps.Store", got)
	}
	if ext.Unwrap().(*stubChainStore) != raw {
		t.Errorf("with telemetry off and hooks disabled, expected external decorator around raw store; got %T", ext.Unwrap())
	}
}

func TestWireStorageDecorators_NilHookRunner(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, nil, false)
	ext, ok := got.(*externaldeps.Store)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *externaldeps.Store", got)
	}
	if ext.Unwrap().(*stubChainStore) != raw {
		t.Errorf("with telemetry off and nil hookRunner, expected external decorator around raw store; got %T", ext.Unwrap())
	}
}

// versionedHistoryStub records whether the versioned-history capability was
// switched on, and on which store instance.
type versionedHistoryStub struct {
	storage.DoltStorage
	enabled *bool
}

func (s *versionedHistoryStub) SetVersionedHistoryEnabled(enabled bool) { *s.enabled = enabled }

// TestVersionedHistoryConfigAppliesToTheRawStore pins the ORDERING that
// applyVersionedHistoryConfig depends on.
//
// The capability is reached by type assertion, and none of the decorators
// (telemetry, externaldeps, hooks) forwards SetVersionedHistoryEnabled. So
// applying the config after any wrap would assert against a wrapper, fail
// silently, and leave versioned history OFF while the operator's config said
// it was on -- a wrong answer rather than an empty one.
//
// The second half of this test is the part that actually guards the ordering:
// it proves the wrapped chain does NOT satisfy the capability, so if someone
// moves the call below a wrap the first half stops passing.
func TestVersionedHistoryConfigAppliesToTheRawStore(t *testing.T) {
	var enabled bool
	raw := &versionedHistoryStub{enabled: &enabled}

	applyVersionedHistoryConfig(raw, true)
	if !enabled {
		t.Error("applyVersionedHistoryConfig(raw, true) did not enable versioned history on the raw store")
	}

	wrapped := storage.DoltStorage(wireExternalDependencyPolicy(telemetry.WrapStorage(raw)))
	if _, ok := wrapped.(storage.VersionedHistoryConfigurer); ok {
		// Fatal, not Skip. A skip here returns BEFORE the afterWrap
		// assertion below -- the half that actually guards the ordering --
		// so the case would report "not applicable" while checking nothing.
		// If a decorator starts forwarding, the constraint this test exists
		// for has changed and the test must be rewritten, not quietly passed.
		t.Fatal("a decorator now forwards SetVersionedHistoryEnabled; the ordering constraint has changed and this test needs rewriting rather than silently passing")
	}

	var afterWrap bool
	rawForWrapped := &versionedHistoryStub{enabled: &afterWrap}
	applyVersionedHistoryConfig(wireExternalDependencyPolicy(telemetry.WrapStorage(rawForWrapped)), true)
	if afterWrap {
		t.Error("the capability was somehow reached through the decorators; this test's premise is stale")
	}
}

// TestVersionedHistoryConfigOffIsANoop pins that the disabled path touches
// nothing at all -- flag-off must be byte-identical to a build without the
// feature, which is the contract #6135 ships under.
func TestVersionedHistoryConfigOffIsANoop(t *testing.T) {
	enabled := false
	raw := &versionedHistoryStub{enabled: &enabled}

	applyVersionedHistoryConfig(raw, false)

	if enabled {
		t.Error("applyVersionedHistoryConfig(raw, false) enabled versioned history; flag-off must be a no-op")
	}
}
