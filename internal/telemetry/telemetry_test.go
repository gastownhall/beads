package telemetry

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// clearAllEnv clears every BD_OTEL_* and OTEL_* env var the package looks at,
// using t.Setenv so the harness restores them on test exit.
func clearAllEnv(t *testing.T) {
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

func TestTranslateLegacyEnv_NoLegacyVars(t *testing.T) {
	clearAllEnv(t)
	mappings := translateLegacyEnv()
	if len(mappings) != 0 {
		t.Fatalf("expected no mappings when no legacy vars set, got %v", mappings)
	}
}

func TestTranslateLegacyEnv_MetricsURL(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_METRICS_URL", "http://example.com/metrics")
	mappings := translateLegacyEnv()
	if got := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"); got != "http://example.com/metrics" {
		t.Errorf("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT = %q, want %q", got, "http://example.com/metrics")
	}
	if !containsSubstr(mappings, "BD_OTEL_METRICS_URL") {
		t.Errorf("mappings should mention BD_OTEL_METRICS_URL, got %v", mappings)
	}
}

func TestTranslateLegacyEnv_LegacyOverridesStandard(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_METRICS_URL", "http://legacy.example/metrics")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://standard.example/metrics")
	translateLegacyEnv()
	// Backwards-compat: BD_OTEL_* must win unconditionally so an existing
	// machine-global OTEL_* setting can't silently redirect bd telemetry.
	if got := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"); got != "http://legacy.example/metrics" {
		t.Errorf("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT = %q, want legacy value to win", got)
	}
}

func TestTranslateLegacyEnv_LogsURL(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_LOGS_URL", "http://example.com/logs")
	translateLegacyEnv()
	if got := os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"); got != "http://example.com/logs" {
		t.Errorf("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT = %q, want %q", got, "http://example.com/logs")
	}
}

func TestTranslateLegacyEnv_Stdout(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	translateLegacyEnv()
	if got := os.Getenv("OTEL_TRACES_EXPORTER"); got != "console" {
		t.Errorf("OTEL_TRACES_EXPORTER = %q, want console", got)
	}
	if got := os.Getenv("OTEL_METRICS_EXPORTER"); got != "console" {
		t.Errorf("OTEL_METRICS_EXPORTER = %q, want console", got)
	}
}

func TestEnabled_NoVarsSet(t *testing.T) {
	clearAllEnv(t)
	if Enabled() {
		t.Error("Enabled() should be false when no OTel env vars set")
	}
}

func TestEnabled_LegacyMetricsURL(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_METRICS_URL", "http://example.com/metrics")
	if !Enabled() {
		t.Error("Enabled() should be true when BD_OTEL_METRICS_URL set")
	}
}

func TestEnabled_LegacyStdout(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	if !Enabled() {
		t.Error("Enabled() should be true when BD_OTEL_STDOUT=true")
	}
}

func TestEnabled_BDOTELEnabledTrue(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_ENABLED", "true")
	if !Enabled() {
		t.Error("Enabled() should be true when BD_OTEL_ENABLED=true")
	}
}

func TestEnabled_StandardEndpointAloneDoesNotActivate(t *testing.T) {
	// A machine-global OTEL_* setting (e.g. for some other instrumented tool)
	// must not silently turn bd telemetry on — explicit opt-in is required.
	clearAllEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://example.com/metrics")
	if Enabled() {
		t.Error("Enabled() should be false when only OTEL_* set without BD_OTEL_ENABLED")
	}
}

func TestEnabled_StandardEndpointWithBDOTELEnabled(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_ENABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://example.com/metrics")
	if !Enabled() {
		t.Error("Enabled() should be true when both BD_OTEL_ENABLED and OTEL_* set")
	}
}

func TestEnabled_SDKDisabledOverridesEverything(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_ENABLED", "true")
	t.Setenv("BD_OTEL_METRICS_URL", "http://example.com/metrics")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://example.com/metrics")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	if Enabled() {
		t.Error("Enabled() should be false when OTEL_SDK_DISABLED=true, even if other vars set")
	}
}

func TestBuildResource_DefaultServiceName(t *testing.T) {
	clearAllEnv(t)
	res, err := buildResource(context.Background(), "bd", "1.0.0", "ass")
	if err != nil {
		t.Fatalf("buildResource: %v", err)
	}
	got, ok := lookupAttr(res.Attributes(), "service.name")
	if !ok {
		t.Fatal("service.name missing")
	}
	if got.AsString() != "bd" {
		t.Errorf("service.name = %q, want bd", got.AsString())
	}
}

func TestBuildResource_OTELServiceNameOverridesDefault(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("OTEL_SERVICE_NAME", "bd-assistant")
	res, err := buildResource(context.Background(), "bd", "1.0.0", "ass")
	if err != nil {
		t.Fatalf("buildResource: %v", err)
	}
	got, ok := lookupAttr(res.Attributes(), "service.name")
	if !ok {
		t.Fatal("service.name missing")
	}
	if got.AsString() != "bd-assistant" {
		t.Errorf("service.name = %q, want bd-assistant (env should override default)", got.AsString())
	}
}

func TestBuildResource_BDPrefixStamped(t *testing.T) {
	clearAllEnv(t)
	res, err := buildResource(context.Background(), "bd", "1.0.0", "ass")
	if err != nil {
		t.Fatalf("buildResource: %v", err)
	}
	got, ok := lookupAttr(res.Attributes(), "bd.prefix")
	if !ok {
		t.Fatal("bd.prefix missing")
	}
	if got.AsString() != "ass" {
		t.Errorf("bd.prefix = %q, want ass", got.AsString())
	}
}

func TestBuildResource_OTELResourceAttributesMerged(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment=workstation,team=infra")
	res, err := buildResource(context.Background(), "bd", "1.0.0", "ass")
	if err != nil {
		t.Fatalf("buildResource: %v", err)
	}
	got, ok := lookupAttr(res.Attributes(), "deployment.environment")
	if !ok {
		t.Fatal("deployment.environment missing — WithFromEnv should pick it up")
	}
	if got.AsString() != "workstation" {
		t.Errorf("deployment.environment = %q, want workstation", got.AsString())
	}
	got, ok = lookupAttr(res.Attributes(), "team")
	if !ok {
		t.Fatal("team missing")
	}
	if got.AsString() != "infra" {
		t.Errorf("team = %q, want infra", got.AsString())
	}
}

func TestBuildResource_BDPrefixOmittedWhenEmpty(t *testing.T) {
	clearAllEnv(t)
	res, err := buildResource(context.Background(), "bd", "1.0.0", "")
	if err != nil {
		t.Fatalf("buildResource: %v", err)
	}
	if _, ok := lookupAttr(res.Attributes(), "bd.prefix"); ok {
		t.Error("bd.prefix should be omitted when prefix is empty")
	}
}

// resetTelemetryState restores noop providers and clears registered shutdown
// hooks after a test that called Init, so global OTel state doesn't leak
// between tests.
func resetTelemetryState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		Shutdown(context.Background())
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
	})
}

func TestInit_NoOpWhenDisabled(t *testing.T) {
	clearAllEnv(t)
	resetTelemetryState(t)
	if err := Init(context.Background(), "bd", "v0.0.0", "ass"); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

func TestInit_LegacyEnvActivatesAndWarns(t *testing.T) {
	clearAllEnv(t)
	resetTelemetryState(t)
	t.Setenv("BD_OTEL_METRICS_URL", "http://example.invalid/metrics")

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	if err := Init(context.Background(), "bd", "v0.0.0", "ass"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "BD_OTEL_*") {
		t.Errorf("expected BD_OTEL_* deprecation warning on stderr, got %q", string(out))
	}
	if got := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"); got != "http://example.invalid/metrics" {
		t.Errorf("legacy env should have populated OTEL_EXPORTER_OTLP_METRICS_ENDPOINT, got %q", got)
	}
}

func TestInit_StdoutExporters(t *testing.T) {
	clearAllEnv(t)
	resetTelemetryState(t)
	t.Setenv("BD_OTEL_ENABLED", "true")
	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	t.Setenv("OTEL_METRICS_EXPORTER", "console")

	// Console exporters write to stdout on shutdown; redirect to keep test
	// output pristine.
	origStdout := os.Stdout
	_, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() {
		_ = w.Close()
		os.Stdout = origStdout
	})

	if err := Init(context.Background(), "bd", "v0.0.0", "ass"); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

func TestOTLPMetricsEndpointSet(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"none", nil, false},
		{"metrics-specific", map[string]string{"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "http://example.invalid"}, true},
		{"generic", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://example.invalid"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearAllEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := otlpMetricsEndpointSet(); got != tc.want {
				t.Errorf("otlpMetricsEndpointSet() = %v, want %v", got, tc.want)
			}
		})
	}
}

func lookupAttr(kvs []attribute.KeyValue, key string) (attribute.Value, bool) {
	for _, kv := range kvs {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func containsSubstr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
