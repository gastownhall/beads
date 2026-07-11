package telemetry

import (
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// fakeDoltStore is a stub DoltStorage used to assert wrapper identity and
// type chain. The embedded interface is nil; tests must not call any of its
// methods (interface-promoted calls would panic).
type fakeDoltStore struct {
	storage.DoltStorage
}

// clearTelemetryEnv unsets every BD_OTEL_* variable Enabled inspects so each
// test starts from a known baseline.
func clearTelemetryEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BD_OTEL_METRICS_URL",
		"BD_OTEL_LOGS_URL",
		"BD_OTEL_STDOUT",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

func TestWrapStorage_DisabledReturnsOriginal(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &fakeDoltStore{}
	got := WrapStorage(raw)
	if got.(*fakeDoltStore) != raw {
		t.Errorf("WrapStorage with telemetry disabled wrapped the store; want input unchanged")
	}
}

func TestWrapStorage_EnabledReturnsInstrumented(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &fakeDoltStore{}
	got := WrapStorage(raw)
	wrapped, ok := got.(*InstrumentedStorage)
	if !ok {
		t.Fatalf("WrapStorage with telemetry enabled returned %T; want *InstrumentedStorage", got)
	}
	if wrapped.Unwrap().(*fakeDoltStore) != raw {
		t.Errorf("Unwrap() did not return the original store")
	}
}

// Verifies that storage.UnwrapStore peels the InstrumentedStorage decorator,
// so optional-interface type assertions in cmd/bd reach the concrete store
// even when telemetry is on.
func TestWrapStorage_PeelsViaUnwrapStore(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &fakeDoltStore{}
	wrapped := WrapStorage(raw)
	got := storage.UnwrapStore(wrapped)
	if got.(*fakeDoltStore) != raw {
		t.Errorf("UnwrapStore did not peel InstrumentedStorage; got %T want %T", got, raw)
	}
}
