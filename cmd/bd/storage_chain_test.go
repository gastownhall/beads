package main

import (
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/telemetry"
)

// stubChainStore is a stand-in for a concrete DoltStorage. It only carries
// identity for type-assertion; method invocations would panic on the embedded
// nil — the chain composition tests must not trigger any of them.
type stubChainStore struct {
	storage.DoltStorage
}

// clearTelemetryEnv unsets every BD_OTEL_* / OTEL_* variable telemetry.Enabled
// inspects, so each test starts from a known baseline.
func clearTelemetryEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BD_OTEL_ENABLED",
		"BD_OTEL_METRICS_URL",
		"BD_OTEL_LOGS_URL",
		"BD_OTEL_STDOUT",
		"OTEL_SDK_DISABLED",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

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
	if inner := hf.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("HookFiringStore.Unwrap() should return raw store directly when telemetry off; got %T", inner)
	}
}

// Asserts the full HookFiringStore → InstrumentedStorage → raw chain that the
// rest of bd depends on for storage spans + bd.storage.* / bd.issue.count
// metrics. This is the regression test for the original PR-3475 bug, where
// WrapStorage was implemented but never called.
func TestWireStorageDecorators_TelemetryOn_HookOn(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_ENABLED", "true")
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	hf, ok := got.(*storage.HookFiringStore)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *storage.HookFiringStore", got)
	}
	inst, ok := hf.Unwrap().(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatalf("middle decorator: got %T; want *telemetry.InstrumentedStorage", hf.Unwrap())
	}
	if inner := inst.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("InstrumentedStorage.Unwrap() should return raw store; got %T", inner)
	}

	if peeled := storage.UnwrapStore(got); peeled.(*stubChainStore) != raw {
		t.Errorf("storage.UnwrapStore should peel both decorator layers; got %T", peeled)
	}
}

func TestWireStorageDecorators_TelemetryOn_HookDisabled(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_ENABLED", "true")
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), true)

	inst, ok := got.(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatalf("expected *telemetry.InstrumentedStorage when hooks disabled; got %T", got)
	}
	if inner := inst.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("InstrumentedStorage.Unwrap() should return raw store; got %T", inner)
	}
}

func TestWireStorageDecorators_TelemetryOff_HookDisabled(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), true)
	if got.(*stubChainStore) != raw {
		t.Errorf("with telemetry off and hooks disabled, expected raw store back; got %T", got)
	}
}

func TestWireStorageDecorators_NilHookRunner(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, nil, false)
	if got.(*stubChainStore) != raw {
		t.Errorf("with telemetry off and nil hookRunner, expected raw store back; got %T", got)
	}
}
