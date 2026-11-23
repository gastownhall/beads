package telemetry

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
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

// contentStore answers, with zero values, the storage calls whose arguments
// carry issue content. Every other method is the nil embedded interface.
type contentStore struct {
	storage.DoltStorage
}

func (contentStore) CreateIssue(context.Context, *types.Issue, string) error { return nil }

func (contentStore) UpdateIssue(context.Context, string, map[string]interface{}, string) error {
	return nil
}

func (contentStore) CloseIssue(context.Context, string, string, string, string) error { return nil }

func (contentStore) ReopenIssue(context.Context, string, string, string) error { return nil }

func (contentStore) AddIssueComment(context.Context, string, string, string) (*types.Comment, error) {
	return &types.Comment{}, nil
}

func (contentStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return []*types.Issue{{ID: "bd-1"}}, nil
}

func (contentStore) SearchIssuesWithCounts(context.Context, string, types.IssueFilter) ([]*types.IssueWithCounts, error) {
	return nil, nil
}

func (contentStore) SearchIssueIDs(context.Context, string, types.IssueFilter) ([]string, error) {
	return nil, nil
}

func (contentStore) SearchIssueSummaries(context.Context, string, types.IssueFilter) ([]*types.IssueSummary, error) {
	return nil, nil
}

func (contentStore) AddLabel(context.Context, string, string, string) error { return nil }

func (contentStore) RemoveLabel(context.Context, string, string, string) error { return nil }

func (contentStore) RenameLabel(context.Context, string, string, string) (int, int, []string, error) {
	return 2, 1, []string{"bd-1", "bd-2"}, nil
}

func (contentStore) GetIssuesByLabel(context.Context, string) ([]*types.Issue, error) {
	return nil, nil
}

func (contentStore) RunInTransaction(context.Context, string, func(storage.Transaction) error) error {
	return nil
}

func (contentStore) RunInIssueLifecycleTransaction(context.Context, string, func(storage.IssueLifecycleTransaction) error) error {
	return nil
}

func (contentStore) SetConfig(context.Context, string, string) error { return nil }

func (contentStore) SetLocalMetadata(context.Context, string, string) error { return nil }

func (contentStore) SlotSet(context.Context, string, string, string, string) error { return nil }

func (contentStore) MergeMetadata(context.Context, string, string, json.RawMessage, string) error {
	return nil
}

// TestStorageSpansCarryNoIssueContent pins #8505 on the storage spans: a
// call's identifiers, actor and counts may be attributes, its content may not.
// Search text (bd.query), label names (bd.label, bd.label.old, bd.label.new)
// and commit messages (db.commit_msg, which can quote a `bd batch -m` message
// or a propagated label) were attributes until then. Titles, descriptions,
// notes, comments, reasons and config or metadata values never were, and are
// driven here so they stay out.
func TestStorageSpansCarryNoIssueContent(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	wrapped := WrapStorage(contentStore{}).(*InstrumentedStorage)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	wrapped.tracer = provider.Tracer(storageScopeName)

	const marker = "SECRET-8505"
	content := func(field string) string { return marker + "-" + field }
	ctx := context.Background()
	_ = wrapped.CreateIssue(ctx, &types.Issue{
		ID:          "bd-1",
		Title:       content("title"),
		Description: content("description"),
		Notes:       content("notes"),
		Labels:      []string{content("label")},
	}, "alice")
	_ = wrapped.UpdateIssue(ctx, "bd-1", map[string]interface{}{"title": content("new-title")}, "alice")
	_ = wrapped.CloseIssue(ctx, "bd-1", content("close-reason"), "alice", "session-1")
	_ = wrapped.ReopenIssue(ctx, "bd-1", content("reopen-reason"), "alice")
	_, _ = wrapped.AddIssueComment(ctx, "bd-1", "alice", content("comment"))
	_, _ = wrapped.SearchIssues(ctx, content("query"), types.IssueFilter{})
	_, _ = wrapped.SearchIssuesWithCounts(ctx, content("query"), types.IssueFilter{})
	_, _ = wrapped.SearchIssueIDs(ctx, content("query"), types.IssueFilter{})
	_, _ = wrapped.SearchIssueSummaries(ctx, content("query"), types.IssueFilter{})
	_ = wrapped.AddLabel(ctx, "bd-1", content("label"), "alice")
	_ = wrapped.RemoveLabel(ctx, "bd-1", content("label"), "alice")
	_, _, _, _ = wrapped.RenameLabel(ctx, content("old-label"), content("new-label"), "alice")
	_, _ = wrapped.GetIssuesByLabel(ctx, content("label"))
	_ = wrapped.RunInTransaction(ctx, content("batch-message"), nil)
	_ = wrapped.RunInIssueLifecycleTransaction(ctx, "bd: propagate label '"+content("label")+"'", nil)
	_ = wrapped.SetConfig(ctx, "custom.key", content("config-value"))
	_ = wrapped.SetLocalMetadata(ctx, "tip_x_last_shown", content("metadata-value"))
	_ = wrapped.SlotSet(ctx, "bd-1", "slot", content("slot-value"), "alice")
	_ = wrapped.MergeMetadata(ctx, "bd-1", "key", json.RawMessage(`"`+content("metadata")+`"`), "alice")

	spans := recorder.Ended()
	if len(spans) != 19 {
		t.Fatalf("ended %d spans, want one per call (19)", len(spans))
	}
	attrs := map[string]map[string]string{}
	for _, span := range spans {
		got := map[string]string{}
		for _, kv := range span.Attributes() {
			got[string(kv.Key)] = kv.Value.Emit()
			if strings.Contains(kv.Value.Emit(), marker) {
				t.Errorf("CONTENT LEAK: %s attribute %s = %q", span.Name(), kv.Key, kv.Value.Emit())
			}
		}
		for _, event := range span.Events() {
			for _, kv := range event.Attributes {
				if strings.Contains(kv.Value.Emit(), marker) {
					t.Errorf("CONTENT LEAK: %s event %s attribute %s = %q", span.Name(), event.Name, kv.Key, kv.Value.Emit())
				}
			}
		}
		attrs[span.Name()] = got
	}

	// The identifiers and counts the spans exist to carry are still there.
	for span, want := range map[string]map[string]string{
		"storage.SearchIssues":     {"db.operation": "SearchIssues", "bd.result.count": "1"},
		"storage.AddLabel":         {"db.operation": "AddLabel", "bd.issue.id": "bd-1"},
		"storage.RenameLabel":      {"db.operation": "RenameLabel", "bd.result.count": "2", "bd.merged.count": "1"},
		"storage.RunInTransaction": {"db.operation": "RunInTransaction"},
		"storage.CreateIssue":      {"db.operation": "CreateIssue", "bd.actor": "alice"},
	} {
		for key, value := range want {
			if attrs[span][key] != value {
				t.Errorf("%s attribute %s = %q, want %q (all: %v)", span, key, attrs[span][key], value, attrs[span])
			}
		}
	}
}
