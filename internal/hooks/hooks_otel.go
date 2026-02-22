package hooks

import (
	"bytes"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// addHookOutputEvents adds stdout/stderr from a hook execution as span events.
// Each buffer is only recorded if non-empty; output is truncated to maxOutputBytes.
func addHookOutputEvents(span trace.Span, stdout, stderr *bytes.Buffer) {
	if n := stdout.Len(); n > 0 {
		span.AddEvent("hook.stdout", trace.WithAttributes(
			attribute.String("output", truncateOutput(stdout.String())),
			attribute.Int("bytes", n),
		))
	}
	if n := stderr.Len(); n > 0 {
		span.AddEvent("hook.stderr", trace.WithAttributes(
			attribute.String("output", truncateOutput(stderr.String())),
			attribute.Int("bytes", n),
		))
	}
}
