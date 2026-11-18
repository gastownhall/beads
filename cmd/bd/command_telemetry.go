package main

import (
	"context"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/telemetry"
)

// commandTelemetryArgs renders the bd.args attribute of the bd.command.<name>
// span: the command path below the root ("create", "comments add"), then the
// long name of every flag set on the command line, sorted ("--description
// --quiet"). Positional arguments and flag values are never included: they are
// where issue titles, descriptions, notes, labels, comments, search text and
// credentials arrive (#8505).
//
// It reads the command cobra parsed rather than os.Args, so nothing is
// re-parsed: a value that looks like a flag (`--title --password=x`), a
// shorthand cluster (`-qd text`), or an argument after `--` is never mistaken
// for a flag name, and a command that disables flag parsing (bd mail) reports
// its path alone.
func commandTelemetryArgs(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	var parts []string
	for c := cmd; c.HasParent(); c = c.Parent() {
		parts = append(parts, c.Name())
	}
	slices.Reverse(parts)
	var flags []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		flags = append(flags, "--"+f.Name)
	})
	// Visit is lexicographic unless a flag set turns SortFlags off; sort
	// anyway so the attribute never depends on that.
	slices.Sort(flags)
	return strings.Join(append(parts, flags...), " ")
}

// commandSpanAttrs builds the initial attribute set for the bd.command.<name>
// span. Pulled out so tests can assert the attribute set without touching
// global OTel state. The bd.actor attribute is stamped later via SetAttributes
// in main.go once getActorWithGit() resolves.
func commandSpanAttrs(cmd *cobra.Command, version string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("bd.command", cmd.Name()),
		attribute.String("bd.version", version),
		attribute.String("bd.args", commandTelemetryArgs(cmd)),
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

// startCommandSpan starts the root bd.command.<name> span for the command
// cobra is about to run. Returns the new context (carrying the span) and the
// span itself for End().
//
// Assumes initTelemetry has already run for this invocation; does not call
// telemetry.Init itself so callers can gate the usage-metrics bootstrap
// block (e.g. skipping span creation for the internal send-metrics
// subcommand) between the two.
func startCommandSpan(ctx context.Context, cmd *cobra.Command, version string) (context.Context, oteltrace.Span) {
	return telemetry.Tracer("bd").Start(ctx, "bd.command."+cmd.Name(),
		oteltrace.WithAttributes(commandSpanAttrs(cmd, version)...),
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
func startCommandTelemetry(ctx context.Context, cmd *cobra.Command, version string) (context.Context, oteltrace.Span) {
	initTelemetry(ctx, version)
	return startCommandSpan(ctx, cmd, version)
}
