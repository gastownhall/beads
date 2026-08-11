package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestCounter_StampsBaseAttrsAndExtras(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	resetBaseAttrs(t)
	captureBaseAttrs("myproject")
	reader := installManualReader(t)

	c := NewCounter(otel.Meter("test"), "bd.test.counter")
	c.Add(context.Background(), 1, attribute.String("type", "serialization"))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("reader.Collect: %v", err)
	}

	sum, ok := findMetric(t, rm, "bd.test.counter").Data.(metricdata.Sum[int64])
	if !ok || len(sum.DataPoints) == 0 {
		t.Fatalf("bd.test.counter had no int64 sum datapoints")
	}
	dp := sum.DataPoints[0]
	if !hasPrefixAttr(dp.Attributes, "myproject") {
		t.Errorf("attrs %v: missing bd.prefix=myproject", dp.Attributes.ToSlice())
	}
	if v, ok := dp.Attributes.Value("type"); !ok || v.AsString() != "serialization" {
		t.Errorf("attrs %v: missing type=serialization", dp.Attributes.ToSlice())
	}
}

func TestHistogram_StampsBaseAttrs(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	resetBaseAttrs(t)
	captureBaseAttrs("myproject")
	reader := installManualReader(t)

	h := NewHistogram(otel.Meter("test"), "bd.test.histogram")
	h.Record(context.Background(), 12.5)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("reader.Collect: %v", err)
	}

	hist, ok := findMetric(t, rm, "bd.test.histogram").Data.(metricdata.Histogram[float64])
	if !ok || len(hist.DataPoints) == 0 {
		t.Fatalf("bd.test.histogram had no float64 histogram datapoints")
	}
	if !hasPrefixAttr(hist.DataPoints[0].Attributes, "myproject") {
		t.Errorf("attrs %v: missing bd.prefix=myproject", hist.DataPoints[0].Attributes.ToSlice())
	}
}

func TestGauge_StampsBaseAttrs(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	resetBaseAttrs(t)
	captureBaseAttrs("myproject")
	reader := installManualReader(t)

	g := NewGauge(otel.Meter("test"), "bd.test.gauge")
	g.Record(context.Background(), 42, attribute.String("status", "open"))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("reader.Collect: %v", err)
	}

	gauge, ok := findMetric(t, rm, "bd.test.gauge").Data.(metricdata.Gauge[int64])
	if !ok || len(gauge.DataPoints) == 0 {
		t.Fatalf("bd.test.gauge had no int64 gauge datapoints")
	}
	if !hasPrefixAttr(gauge.DataPoints[0].Attributes, "myproject") {
		t.Errorf("attrs %v: missing bd.prefix=myproject", gauge.DataPoints[0].Attributes.ToSlice())
	}
}

// A zero-value instrument is what a package gets if its init() never ran or
// the meter refused to build the instrument. Emitting on one must be a no-op,
// not a nil-pointer panic — telemetry never blocks the user.
func TestZeroValueInstrumentsAreNoOps(t *testing.T) {
	ctx := context.Background()
	Counter{}.Add(ctx, 1)
	Histogram{}.Record(ctx, 1)
	Gauge{}.Record(ctx, 1)
}
