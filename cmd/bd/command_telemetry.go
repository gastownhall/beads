package main

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/telemetry"
)

// commandSpanAttrs builds the initial attribute set for the bd.command.<name>
// span. Pulled out so tests can assert the attribute set without touching
// global OTel state. The bd.actor attribute is stamped later via SetAttributes
// in main.go once getActorWithGit() resolves.
//
// secretFlags names the flag tokens whose values must never reach the
// bd.args attribute (see secretFlagTokens); scrubArgsForTelemetry also
// applies its DSN/userinfo scrub to every arg, so a nil map still redacts
// positional connection strings.
func commandSpanAttrs(cmdName, version string, args []string, secretFlags map[string]bool) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("bd.command", cmdName),
		attribute.String("bd.version", version),
		attribute.String("bd.args", scrubArgsForTelemetry(args, secretFlags)),
	}
}

// initTelemetry initializes OTel providers for this invocation.
//
// Telemetry init failures are non-fatal — they are logged via debug and
// telemetry falls back to noop providers, matching the rest of bd's
// "telemetry is opt-in, never blocks the user" stance.
//
// Extracted so the call from main.go's PreRunE is a single testable line.
// Must run before any DB access so SQL spans nest under the command span;
// in main.go it runs ahead of the usage-metrics bootstrap block (a separate
// subsystem, unrelated to OTel), so it is kept independent of
// startCommandSpan rather than folded into a single combined call.
func initTelemetry(ctx context.Context, version string) {
	if err := telemetry.Init(ctx, "bd", version); err != nil {
		debug.Logf("warning: telemetry init failed: %v", err)
	}
}

// startCommandSpan starts the root bd.command.<name> span. Returns the new
// context (carrying the span) and the span itself for End().
//
// Assumes initTelemetry has already run for this invocation; does not call
// telemetry.Init itself so callers can gate the usage-metrics bootstrap
// block (e.g. skipping span creation for the internal send-metrics
// subcommand) between the two.
func startCommandSpan(ctx context.Context, cmdName, version string, args []string, secretFlags map[string]bool) (context.Context, oteltrace.Span) {
	return telemetry.Tracer("bd").Start(ctx, "bd.command."+cmdName,
		oteltrace.WithAttributes(commandSpanAttrs(cmdName, version, args, secretFlags)...),
	)
}

// startCommandTelemetry composes initTelemetry + startCommandSpan into a
// single call for callers that don't need to interleave other setup between
// the two steps (e.g. tests exercising the combined wiring contract).
//
// The original PR-3475 bug (telemetry.WrapStorage implemented but never
// called) taught us that wiring inside cobra PreRunE is exactly the kind of
// code that decays silently — this and its two constituent halves exist so
// that wiring is a single testable line wherever it's used.
func startCommandTelemetry(ctx context.Context, cmdName, version string, args []string, secretFlags map[string]bool) (context.Context, oteltrace.Span) {
	initTelemetry(ctx, version)
	return startCommandSpan(ctx, cmdName, version, args, secretFlags)
}
