package hooks

import (
	"bytes"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// addHookOutputEvents records that a hook wrote to stdout or stderr as span
// events carrying the byte count only, each added only when its stream is
// non-empty. The text itself is not recorded: a hook receives the issue as JSON
// on stdin and commonly echoes or logs it, so its output is issue content
// (#8505).
func addHookOutputEvents(span trace.Span, stdout, stderr *bytes.Buffer) {
	if n := stdout.Len(); n > 0 {
		span.AddEvent("hook.stdout", trace.WithAttributes(attribute.Int("bytes", n)))
	}
	if n := stderr.Len(); n > 0 {
		span.AddEvent("hook.stderr", trace.WithAttributes(attribute.Int("bytes", n)))
	}
}
