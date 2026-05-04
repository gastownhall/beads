package main

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/telemetry"
)

// readBDPrefix returns the project's issue prefix (visible in every issue
// ID, e.g. "myproj-123"), stripped of its trailing "-". This is stamped as
// the bd.prefix resource attribute by telemetry.Init so dashboards can split
// metrics per project.
//
// Returns "" when the store is nil or the lookup fails — buildResource
// omits bd.prefix in that case.
func readBDPrefix(ctx context.Context, store storage.Storage) string {
	if store == nil {
		return ""
	}
	p, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(p, "-")
}

// commandSpanAttrs builds the attribute set for the bd.command.<name> span.
// Pulled out so tests can assert the attribute set without touching global
// OTel state.
func commandSpanAttrs(cmdName, version, actor string, args []string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("bd.command", cmdName),
		attribute.String("bd.version", version),
		attribute.String("bd.args", strings.Join(args, " ")),
		attribute.String("bd.actor", actor),
	}
}

// startCommandTelemetry initializes OTel using the store-backed bd.prefix
// resource attribute, then starts the root bd.command.<name> span. Returns
// the new context (carrying the span) and the span itself for End().
//
// Telemetry init failures are non-fatal — they are logged via debug and
// telemetry falls back to noop providers, matching the rest of bd's
// "telemetry is opt-in, never blocks the user" stance.
//
// Extracted so that the call from main.go's PreRunE is a single testable
// line. The original PR-3475 bug (telemetry.WrapStorage implemented but
// never called) taught us that wiring inside cobra PreRunE is exactly the
// kind of code that decays silently.
func startCommandTelemetry(ctx context.Context, store storage.Storage, cmdName, version, actor string, args []string) (context.Context, oteltrace.Span) {
	if err := telemetry.Init(ctx, "bd", version, readBDPrefix(ctx, store)); err != nil {
		debug.Logf("warning: telemetry init failed: %v", err)
	}
	return telemetry.Tracer("bd").Start(ctx, "bd.command."+cmdName,
		oteltrace.WithAttributes(commandSpanAttrs(cmdName, version, actor, args)...),
	)
}
