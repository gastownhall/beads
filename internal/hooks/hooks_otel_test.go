package hooks

import (
	"bytes"
	"context"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestHookOutputEventsCarryOnlyByteCounts pins #8505 for hooks: a hook gets
// the issue as JSON on stdin and often echoes or logs it, so the hook.stdout
// and hook.stderr events say how much the script wrote, never what. A silent
// script adds no events.
func TestHookOutputEventsCarryOnlyByteCounts(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := provider.Tracer("hooks-test")

	const marker = "SECRET-8505"
	stdout := `{"id":"bd-1","title":"` + marker + ` title"}` + "\n"
	stderr := "notified: " + marker + "\n"
	_, span := tracer.Start(context.Background(), "hook.exec")
	addHookOutputEvents(span, bytes.NewBufferString(stdout), bytes.NewBufferString(stderr))
	span.End()
	_, quiet := tracer.Start(context.Background(), "hook.exec.quiet")
	addHookOutputEvents(quiet, &bytes.Buffer{}, &bytes.Buffer{})
	quiet.End()

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended %d spans, want 2", len(spans))
	}
	if events := spans[1].Events(); len(events) != 0 {
		t.Errorf("a hook that wrote nothing added events %v, want none", events)
	}
	want := map[string]int64{"hook.stdout": int64(len(stdout)), "hook.stderr": int64(len(stderr))}
	events := spans[0].Events()
	if len(events) != len(want) {
		t.Fatalf("events = %v, want one per non-empty stream", events)
	}
	for _, event := range events {
		size, ok := want[event.Name]
		if !ok {
			t.Errorf("unexpected event %q", event.Name)
			continue
		}
		if len(event.Attributes) != 1 || event.Attributes[0].Key != "bytes" || event.Attributes[0].Value.AsInt64() != size {
			t.Errorf("%s attributes = %v, want only bytes=%d", event.Name, event.Attributes, size)
		}
		for _, kv := range event.Attributes {
			if strings.Contains(kv.Value.Emit(), marker) {
				t.Errorf("CONTENT LEAK: %s attribute %s = %q", event.Name, kv.Key, kv.Value.Emit())
			}
		}
	}
}
