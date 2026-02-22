// Package telemetry provides OpenTelemetry integration for beads.
//
// Telemetry is opt-in: set BD_OTEL_METRICS_URL or BD_OTEL_STDOUT=true to activate.
// No overhead when neither variable is set.
//
// # Configuration
//
//	BD_OTEL_METRICS_URL=http://localhost:8428/opentelemetry/api/v1/push
//	    Push metrics to VictoriaMetrics (or any OTLP HTTP receiver).
//	    Presence of this variable enables telemetry.
//
//	BD_OTEL_LOGS_URL=http://localhost:9428/insert/opentelemetry/v1/logs
//	    Push logs to VictoriaLogs (reserved for future log export).
//
//	BD_OTEL_STDOUT=true
//	    Write spans and metrics to stderr (dev/debug mode).
//	    Also activates telemetry when set alone.
//
// # Recommended local stack
//
//	VictoriaMetrics :8428  — metrics storage
//	VictoriaLogs    :9428  — log storage
//	Grafana         :9429  — dashboards
//
// See docs/OBSERVABILITY.md for the full reference.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const instrumentationScope = "github.com/steveyegge/beads"

var shutdownFns []func(context.Context) error

// Enabled reports whether telemetry is active.
// True when BD_OTEL_METRICS_URL is set or BD_OTEL_STDOUT=true.
func Enabled() bool {
	return os.Getenv("BD_OTEL_METRICS_URL") != "" ||
		os.Getenv("BD_OTEL_STDOUT") == "true"
}

// Init configures OTel providers.
// When neither BD_OTEL_METRICS_URL nor BD_OTEL_STDOUT is set, installs no-op
// providers and returns immediately (zero overhead path).
//
// Traces are exported only when BD_OTEL_STDOUT=true (stdout, for local debugging).
// Metrics are exported to BD_OTEL_METRICS_URL and/or stdout.
func Init(ctx context.Context, serviceName, version string) error {
	if !Enabled() {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		return nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceVersionKey.String(version),
		),
		resource.WithHost(),
		resource.WithProcess(),
	)
	if err != nil {
		return fmt.Errorf("telemetry: resource: %w", err)
	}

	// Traces: stdout only (local debug). No remote trace backend in the default stack.
	if os.Getenv("BD_OTEL_STDOUT") == "true" {
		tp, err := buildTraceProvider(ctx, res)
		if err != nil {
			return fmt.Errorf("telemetry: trace provider: %w", err)
		}
		otel.SetTracerProvider(tp)
		shutdownFns = append(shutdownFns, tp.Shutdown)
	} else {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
	}

	// Metrics: VictoriaMetrics (HTTP) and/or stdout.
	mp, err := buildMetricProvider(ctx, res)
	if err != nil {
		return fmt.Errorf("telemetry: metric provider: %w", err)
	}
	otel.SetMeterProvider(mp)
	shutdownFns = append(shutdownFns, mp.Shutdown)

	return nil
}

func buildTraceProvider(ctx context.Context, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, err
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exp),
	), nil
}

func buildMetricProvider(ctx context.Context, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	opts := []sdkmetric.Option{sdkmetric.WithResource(res)}

	if os.Getenv("BD_OTEL_STDOUT") == "true" {
		exp, err := stdoutmetric.New()
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(15*time.Second)),
		))
	}

	if url := os.Getenv("BD_OTEL_METRICS_URL"); url != "" {
		exp, err := buildOTLPMetricExporter(ctx, url)
		if err != nil {
			return nil, fmt.Errorf("otlp metric exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(30*time.Second)),
		))
	}

	return sdkmetric.NewMeterProvider(opts...), nil
}

// Tracer returns a tracer with the given instrumentation name (or the global scope).
func Tracer(name string) trace.Tracer {
	if name == "" {
		name = instrumentationScope
	}
	return otel.Tracer(name)
}

// Meter returns a meter with the given instrumentation name (or the global scope).
func Meter(name string) metric.Meter {
	if name == "" {
		name = instrumentationScope
	}
	return otel.Meter(name)
}

// Shutdown flushes all spans/metrics and shuts down OTel providers.
// Should be deferred in PersistentPostRun with a short-lived context.
func Shutdown(ctx context.Context) {
	for _, fn := range shutdownFns {
		_ = fn(ctx)
	}
	shutdownFns = nil
}
