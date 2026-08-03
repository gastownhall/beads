package main

import (
	"context"
	"os"
	"testing"
)

// clearTelemetryEnv unsets every BD_OTEL_* / OTEL_* variable telemetry.Enabled
// or the SDK looks at, using t.Setenv so the harness restores them on test
// exit. Local to cmd/bd because a sibling helper in internal/telemetry would
// be in a different package; this duplication keeps cmd/bd's tests hermetic
// without exporting a test-only helper.
func clearTelemetryEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BD_OTEL_ENABLED",
		"BD_OTEL_METRICS_URL",
		"BD_OTEL_LOGS_URL",
		"BD_OTEL_STDOUT",
		"OTEL_SDK_DISABLED",
		"OTEL_SERVICE_NAME",
		"OTEL_RESOURCE_ATTRIBUTES",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
		"OTEL_TRACES_EXPORTER",
		"OTEL_METRICS_EXPORTER",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

func TestCommandSpanAttrs(t *testing.T) {
	got := commandSpanAttrs("ready", "1.0.4-dev", []string{"--json", "--include-deferred"}, nil)

	want := map[string]string{
		"bd.command": "ready",
		"bd.version": "1.0.4-dev",
		"bd.args":    "--json --include-deferred",
	}
	if len(got) != len(want) {
		t.Fatalf("commandSpanAttrs returned %d attrs; want %d", len(got), len(want))
	}
	for _, kv := range got {
		expected, ok := want[string(kv.Key)]
		if !ok {
			t.Errorf("unexpected attribute %q in span attrs", kv.Key)
			continue
		}
		if kv.Value.AsString() != expected {
			t.Errorf("attr %q = %q; want %q", kv.Key, kv.Value.AsString(), expected)
		}
	}
}

func TestCommandSpanAttrs_EmptyArgs(t *testing.T) {
	got := commandSpanAttrs("status", "v1", nil, nil)
	for _, kv := range got {
		if string(kv.Key) == "bd.args" && kv.Value.AsString() != "" {
			t.Errorf("bd.args with nil args = %q; want empty string", kv.Value.AsString())
		}
	}
}

// startCommandTelemetry composes Init + Tracer.Start. The bd.command.<name>
// span is the parent for every storage and AI span downstream — silently
// returning nil here would break trace nesting across the whole invocation.
// Whether the span actually records is governed by telemetry.Init (covered
// in internal/telemetry/telemetry_test.go); this test guards the wiring
// contract: the function must always return a non-nil context and span.
func TestStartCommandTelemetry_DisabledStillReturnsSpan(t *testing.T) {
	clearTelemetryEnv(t)
	ctx, span := startCommandTelemetry(context.Background(), "ready", "1.0.0", []string{"--json"}, nil)
	if ctx == nil {
		t.Fatal("startCommandTelemetry returned nil context")
	}
	if span == nil {
		t.Fatal("startCommandTelemetry returned nil span")
	}
	span.End()
}
