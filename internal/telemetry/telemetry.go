// Package telemetry provides OpenTelemetry integration for beads.
//
// Telemetry is explicit opt-in: nothing is exported unless BD_OTEL_ENABLED=true
// (or a legacy BD_OTEL_* variable) is set. No overhead when off — bd will not
// auto-activate from a machine-global OTEL_* variable that was set for some
// other instrumented tool.
//
// # Configuration
//
//	BD_OTEL_ENABLED=true
//	    Master switch. Activates telemetry; without it the standard OTEL_*
//	    variables below are ignored.
//
// Once activated, beads honours the standard OpenTelemetry SDK environment
// variables:
//
//	OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://localhost:8428/opentelemetry/api/v1/push
//	    Push metrics to an OTLP HTTP receiver (e.g. VictoriaMetrics).
//
//	OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://localhost:9428/insert/opentelemetry/v1/logs
//	    Push logs to an OTLP HTTP receiver (reserved for future log export).
//
//	OTEL_TRACES_EXPORTER=console
//	OTEL_METRICS_EXPORTER=console
//	    Write spans / metrics to stderr (dev/debug mode).
//
//	OTEL_SERVICE_NAME=bd
//	OTEL_RESOURCE_ATTRIBUTES=key=value,...
//	    Override or extend resource attributes (service.name, service.version,
//	    db.namespace, deployment.environment, ...).
//
//	OTEL_SDK_DISABLED=true
//	    Force telemetry off even when BD_OTEL_ENABLED=true is set.
//
// # Legacy environment variables
//
// The earlier BD_OTEL_* variables remain honoured for backwards compatibility
// and take precedence over their OTEL_* equivalents. A deprecation warning is
// logged when any are detected:
//
//	BD_OTEL_METRICS_URL → OTEL_EXPORTER_OTLP_METRICS_ENDPOINT
//	BD_OTEL_LOGS_URL    → OTEL_EXPORTER_OTLP_LOGS_ENDPOINT
//	BD_OTEL_STDOUT=true → OTEL_TRACES_EXPORTER=console + OTEL_METRICS_EXPORTER=console
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
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const instrumentationScope = "github.com/steveyegge/beads"

var shutdownFns []func(context.Context) error

// Enabled reports whether telemetry is active.
//
// Active when explicitly opted in (BD_OTEL_ENABLED=true) or when a legacy
// BD_OTEL_* variable is set, and not disabled via OTEL_SDK_DISABLED=true.
// Standard OTEL_* variables on their own do not activate telemetry — they
// configure it once an opt-in is in effect, so a machine-global OTEL_* setting
// for some other instrumented tool can't silently turn bd telemetry on.
func Enabled() bool {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return false
	}
	return strings.EqualFold(os.Getenv("BD_OTEL_ENABLED"), "true") || hasLegacySelector()
}

func hasLegacySelector() bool {
	return os.Getenv("BD_OTEL_METRICS_URL") != "" ||
		os.Getenv("BD_OTEL_LOGS_URL") != "" ||
		strings.EqualFold(os.Getenv("BD_OTEL_STDOUT"), "true")
}

// translateLegacyEnv copies any BD_OTEL_* environment variable into its
// standard OTEL_* equivalent, unconditionally — a legacy value wins over a
// pre-existing standard value so an unrelated machine-global OTEL_* setting
// cannot silently redirect bd telemetry. Returns one human-readable mapping
// string for each legacy variable found.
func translateLegacyEnv() []string {
	var mappings []string
	if u := os.Getenv("BD_OTEL_METRICS_URL"); u != "" {
		_ = os.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", u)
		mappings = append(mappings, "BD_OTEL_METRICS_URL → OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	}
	if u := os.Getenv("BD_OTEL_LOGS_URL"); u != "" {
		_ = os.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", u)
		mappings = append(mappings, "BD_OTEL_LOGS_URL → OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
	}
	if strings.EqualFold(os.Getenv("BD_OTEL_STDOUT"), "true") {
		_ = os.Setenv("OTEL_TRACES_EXPORTER", "console")
		_ = os.Setenv("OTEL_METRICS_EXPORTER", "console")
		mappings = append(mappings, "BD_OTEL_STDOUT=true → OTEL_TRACES_EXPORTER=console + OTEL_METRICS_EXPORTER=console")
	}
	return mappings
}

// buildResource assembles the OTel Resource describing this bd process.
//
// Defaults are merged with the host/process detectors and finally with the
// FromEnv detector, so OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES can
// override anything set by the caller. dbName, when non-empty, is stamped as
// db.namespace to differentiate metrics emitted from bd processes bound to
// distinct dolt databases.
func buildResource(ctx context.Context, serviceName, version, dbName string) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceNameKey.String(serviceName),
		semconv.ServiceVersionKey.String(version),
	}
	if dbName != "" {
		attrs = append(attrs, attribute.String("db.namespace", dbName))
	}
	return resource.New(ctx,
		resource.WithAttributes(attrs...),
		resource.WithHost(),
		resource.WithProcess(),
		resource.WithFromEnv(),
	)
}

// Init configures OTel providers.
// When no OpenTelemetry SDK environment variable selects an exporter or
// endpoint, installs no-op providers and returns immediately (zero overhead).
//
// Traces are exported only when OTEL_TRACES_EXPORTER=console (stdout, for
// local debugging); there is no remote trace backend.
// Metrics are exported to OTEL_EXPORTER_OTLP_METRICS_ENDPOINT and/or stdout.
//
// dbName, when non-empty, is stamped as the db.namespace resource attribute
// so metrics from bd processes bound to different dolt databases can be
// distinguished by dashboard queries.
func Init(ctx context.Context, serviceName, version, dbName string) error {
	if mappings := translateLegacyEnv(); len(mappings) > 0 {
		fmt.Fprintf(os.Stderr,
			"warning: BD_OTEL_* environment variables are deprecated. Replace with BD_OTEL_ENABLED=true plus the standard OpenTelemetry SDK variables. Translated for this run: %s\n",
			strings.Join(mappings, "; "))
	}

	if !Enabled() {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		return nil
	}

	res, err := buildResource(ctx, serviceName, version, dbName)
	if err != nil {
		return fmt.Errorf("telemetry: resource: %w", err)
	}

	if strings.EqualFold(os.Getenv("OTEL_TRACES_EXPORTER"), "console") {
		tp, err := buildTraceProvider(ctx, res)
		if err != nil {
			return fmt.Errorf("telemetry: trace provider: %w", err)
		}
		otel.SetTracerProvider(tp)
		shutdownFns = append(shutdownFns, tp.Shutdown)
	} else {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
	}

	mp, err := buildMetricProvider(ctx, res)
	if err != nil {
		return fmt.Errorf("telemetry: metric provider: %w", err)
	}
	otel.SetMeterProvider(mp)
	shutdownFns = append(shutdownFns, mp.Shutdown)

	return nil
}

func buildTraceProvider(_ context.Context, res *resource.Resource) (*sdktrace.TracerProvider, error) {
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

	if strings.EqualFold(os.Getenv("OTEL_METRICS_EXPORTER"), "console") {
		exp, err := stdoutmetric.New()
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(15*time.Second)),
		))
	}

	if otlpMetricsEndpointSet() {
		exp, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("otlp metric exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(30*time.Second)),
		))
	}

	return sdkmetric.NewMeterProvider(opts...), nil
}

func otlpMetricsEndpointSet() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""
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
