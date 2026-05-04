package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// recordingDoltStore is a minimal DoltStorage stub that implements only the
// methods the integration test exercises. The embedded nil interface lets it
// satisfy storage.DoltStorage; calling any unstubbed method would panic, but
// the test only drives CreateIssue and GetStatistics.
type recordingDoltStore struct {
	storage.DoltStorage
	stats types.Statistics
}

func (r *recordingDoltStore) CreateIssue(_ context.Context, _ *types.Issue, _ string) error {
	return nil
}

func (r *recordingDoltStore) GetStatistics(_ context.Context) (*types.Statistics, error) {
	s := r.stats
	return &s, nil
}

// installManualReader replaces the global MeterProvider with one backed by a
// fresh ManualReader, returning the reader so the test can collect emitted
// metrics. Restores noop providers on test exit.
func installManualReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	resetTelemetryState(t)
	return reader
}

// findMetric returns the first metric in rm with the given name, or fails
// the test if no such metric was collected.
func findMetric(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}
	t.Fatalf("metric %q not collected; got %d scope(s)", name, len(rm.ScopeMetrics))
	return metricdata.Metrics{}
}

// hasPrefixAttr returns true if attrs contains bd.prefix=want.
func hasPrefixAttr(attrs attribute.Set, want string) bool {
	v, ok := attrs.Value("bd.prefix")
	return ok && v.AsString() == want
}

func TestInstrumentedStorage_StampsBDPrefixOnOperationsCounter(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_ENABLED", "true")
	resetBaseAttrs(t)
	captureBaseAttrs("ass")
	reader := installManualReader(t)

	store := WrapStorage(&recordingDoltStore{})
	if err := store.CreateIssue(context.Background(), &types.Issue{IssueType: types.IssueType("task")}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("reader.Collect: %v", err)
	}

	m := findMetric(t, rm, "bd.storage.operations")
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("bd.storage.operations Data = %T, want Sum[int64]", m.Data)
	}
	if len(sum.DataPoints) == 0 {
		t.Fatalf("bd.storage.operations had no datapoints")
	}
	for i, dp := range sum.DataPoints {
		if !hasPrefixAttr(dp.Attributes, "ass") {
			t.Errorf("datapoint %d attrs %v: missing bd.prefix=ass", i, dp.Attributes.ToSlice())
		}
	}
}

func TestInstrumentedStorage_StampsBDPrefixOnIssueGauge(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_ENABLED", "true")
	resetBaseAttrs(t)
	captureBaseAttrs("ass")
	reader := installManualReader(t)

	inner := &recordingDoltStore{stats: types.Statistics{
		OpenIssues: 3, InProgressIssues: 1, ClosedIssues: 7, DeferredIssues: 2,
	}}
	store := WrapStorage(inner)
	if _, err := store.GetStatistics(context.Background()); err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("reader.Collect: %v", err)
	}

	m := findMetric(t, rm, "bd.issue.count")
	gauge, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("bd.issue.count Data = %T, want Gauge[int64]", m.Data)
	}
	if len(gauge.DataPoints) == 0 {
		t.Fatalf("bd.issue.count had no datapoints")
	}
	for i, dp := range gauge.DataPoints {
		if !hasPrefixAttr(dp.Attributes, "ass") {
			t.Errorf("datapoint %d attrs %v: missing bd.prefix=ass", i, dp.Attributes.ToSlice())
		}
	}
}

func TestInstrumentedStorage_NoPrefixWhenUnset(t *testing.T) {
	clearAllEnv(t)
	t.Setenv("BD_OTEL_ENABLED", "true")
	resetBaseAttrs(t)
	reader := installManualReader(t)

	store := WrapStorage(&recordingDoltStore{})
	if err := store.CreateIssue(context.Background(), &types.Issue{IssueType: types.IssueType("task")}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("reader.Collect: %v", err)
	}

	m := findMetric(t, rm, "bd.storage.operations")
	sum := m.Data.(metricdata.Sum[int64])
	for i, dp := range sum.DataPoints {
		if _, ok := dp.Attributes.Value("bd.prefix"); ok {
			t.Errorf("datapoint %d had bd.prefix attr but no prefix was captured: %v", i, dp.Attributes.ToSlice())
		}
	}
}
