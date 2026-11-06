package telemetry

import (
	"context"
	"os"
	"slices"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// InstrumentedStorage forwards the optional prefix-read capability, which
// callers find by type assertion — so a drifting method set would degrade
// them to full scans silently. Pinned from the test build rather than
// storage.go to keep the telemetry package free of a production dependency on
// storage/domain.
var _ domain.ConfigPrefixReader = (*InstrumentedStorage)(nil)

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

// descendantStore is a DoltStorage whose only real method is
// SearchDescendants, recording the root of every walk that reaches it.
type descendantStore struct {
	storage.DoltStorage
	roots []string
	found []*types.Issue
}

func (s *descendantStore) SearchDescendants(_ context.Context, rootID string, _ types.IssueFilter) ([]*types.Issue, error) {
	s.roots = append(s.roots, rootID)
	return s.found, nil
}

// outerDecorator stands in for the decorators bd stacks above this layer (the
// hook and external-dependency ones): it hides what is beneath it from a type
// assertion and hands it to Unwrap.
type outerDecorator struct{ storage.DoltStorage }

func (d outerDecorator) Unwrap() storage.DoltStorage { return d.DoltStorage }

// TestSearchDescendantsIsTracedBehindDecorators pins that the `bd list
// --parent` tree walk keeps a span with telemetry on: storage.SearchDescendants
// offers the walk to this layer before unwrapping past it, and this layer hands
// it on, once, to the inner store's one-transaction capability. Unwrapping
// first would answer the same tree with no span at all.
func TestSearchDescendantsIsTracedBehindDecorators(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	inner := &descendantStore{found: []*types.Issue{{ID: "d-1"}, {ID: "d-2"}}}
	wrapped := WrapStorage(inner).(*InstrumentedStorage)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	wrapped.tracer = provider.Tracer(storageScopeName)

	got, err := storage.SearchDescendants(context.Background(), outerDecorator{wrapped}, "d-root", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchDescendants: %v", err)
	}
	if !slices.Equal(got, inner.found) || !slices.Equal(inner.roots, []string{"d-root"}) {
		t.Fatalf("SearchDescendants = %v after inner walks of %v, want the inner store's answer from one walk of d-root", got, inner.roots)
	}
	spans := recorder.Ended()
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	if !slices.Equal(names, []string{"storage.SearchDescendants"}) {
		t.Fatalf("ended spans = %v, want exactly one storage.SearchDescendants", names)
	}
	var count int64 = -1
	for _, attr := range spans[0].Attributes() {
		if attr.Key == "bd.result.count" {
			count = attr.Value.AsInt64()
		}
	}
	if count != int64(len(inner.found)) {
		t.Fatalf("bd.result.count = %d, want %d", count, len(inner.found))
	}
}
