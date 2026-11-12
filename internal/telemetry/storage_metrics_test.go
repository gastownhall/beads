package telemetry

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// metricAttrStore answers the handful of storage calls the metric-attribute
// test drives. Every other method is the nil embedded interface.
type metricAttrStore struct {
	storage.DoltStorage
}

func (metricAttrStore) CreateIssue(context.Context, *types.Issue, string) error { return nil }

func (metricAttrStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if id == "bd-missing" {
		return nil, errors.New("not found")
	}
	return &types.Issue{ID: id}, nil
}

func (metricAttrStore) GetIssueByExternalRef(context.Context, string) (*types.Issue, error) {
	return nil, errors.New("not found")
}

func (metricAttrStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return nil, nil
}

// withManualMeter installs a meter provider backed by a manual reader for the
// test, restoring the previous global provider afterwards.
func withManualMeter(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		_ = provider.Shutdown(context.Background())
	})
	return reader
}

// metricAttrSets returns, per instrument name, the sorted attribute key sets
// of its data points, rendered as "key=value,...".
func metricAttrSets(t *testing.T, reader *sdkmetric.ManualReader) map[string][]string {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	render := func(set attribute.Set) string {
		parts := make([]string, 0, set.Len())
		for _, kv := range set.ToSlice() {
			parts = append(parts, string(kv.Key)+"="+kv.Value.Emit())
		}
		return strings.Join(parts, ",")
	}
	out := map[string][]string{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					out[m.Name] = append(out[m.Name], render(dp.Attributes))
				}
			case metricdata.Histogram[float64]:
				for _, dp := range data.DataPoints {
					out[m.Name] = append(out[m.Name], render(dp.Attributes))
				}
			}
		}
	}
	for name := range out {
		sort.Strings(out[name])
	}
	return out
}

// TestStorageMetricsCarryOnlyTheOperation pins the attribute sets of the
// bd.storage.* metrics: each series is keyed by db.operation alone. Request
// attributes (issue ids, actors, search text, commit messages) belong on the
// span only: on a metric they make one series per value (the SDK caps a
// metric at 2000 sets and folds the rest into otel.metric.overflow) and send
// user content to the metrics receiver. Duration and errors must carry
// db.operation too, or every operation without request attributes would fall
// into one unlabeled series. That includes every storage.<Role>.<Method>
// operation, so a role decorator is driven as well.
func TestStorageMetricsCarryOnlyTheOperation(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	reader := withManualMeter(t)
	s := WrapStorage(metricAttrStore{}).(*InstrumentedStorage)
	ctx := context.Background()

	_ = s.CreateIssue(ctx, &types.Issue{IssueType: types.TypeBug}, "alice@example.com")
	_, _ = s.GetIssue(ctx, "bd-1")
	_, _ = s.GetIssue(ctx, "bd-2")
	_, _ = s.GetIssue(ctx, "bd-missing")
	_, _ = s.GetIssueByExternalRef(ctx, "gh-9")
	_, _ = s.SearchIssues(ctx, "rotate the prod password", types.IssueFilter{})
	_, _ = s.WrapIssueReader(&fakeIssueReader{}).Get(ctx, issueops.GetRequest{ID: "bd-1"})
	_, _ = s.WrapIssueReader(&fakeIssueReader{err: errors.New("not found")}).Get(ctx, issueops.GetRequest{ID: "bd-missing"})

	got := metricAttrSets(t, reader)
	want := map[string][]string{
		"bd.storage.operations": {
			"db.operation=CreateIssue",
			"db.operation=GetIssue",
			"db.operation=GetIssueByExternalRef",
			"db.operation=IssueReader.Get",
			"db.operation=SearchIssues",
		},
		"bd.storage.operation.duration": {
			"db.operation=CreateIssue",
			"db.operation=GetIssue",
			"db.operation=GetIssueByExternalRef",
			"db.operation=IssueReader.Get",
			"db.operation=SearchIssues",
		},
		"bd.storage.errors": {
			"db.operation=GetIssue",
			"db.operation=GetIssueByExternalRef",
			"db.operation=IssueReader.Get",
		},
	}
	for name, sets := range want {
		if strings.Join(got[name], " | ") != strings.Join(sets, " | ") {
			t.Errorf("%s attribute sets = %q, want %q", name, got[name], sets)
		}
	}
}

// TestMillisecondsKeepsTheFraction pins that durations are recorded as
// fractional milliseconds. time.Duration.Milliseconds truncates, so a
// sub-millisecond storage call (most point reads) recorded as 0 ms and a
// 1.9 ms call as 1 ms.
func TestMillisecondsKeepsTheFraction(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want float64
	}{
		{250 * time.Microsecond, 0.25},
		{1900 * time.Microsecond, 1.9},
		{3 * time.Second, 3000},
		{0, 0},
	} {
		if got := Milliseconds(tc.d); got != tc.want {
			t.Errorf("Milliseconds(%v) = %v, want %v", tc.d, got, tc.want)
		}
	}
}

// TestStorageDurationRecordsSubMillisecondCalls pins that a storage call's
// duration keeps its fraction: a 1.5 ms call records 1.5, not 1. The start
// time is moved back instead of sleeping, so the duration is never a whole
// number of milliseconds by accident.
func TestStorageDurationRecordsSubMillisecondCalls(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	reader := withManualMeter(t)
	s := WrapStorage(metricAttrStore{}).(*InstrumentedStorage)
	ctx, span, started := s.op(context.Background(), "GetIssue")
	started.at = started.at.Add(-1500 * time.Microsecond)
	s.done(ctx, span, started, nil)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	var sum float64
	var found bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if h, ok := m.Data.(metricdata.Histogram[float64]); ok && m.Name == "bd.storage.operation.duration" {
				for _, dp := range h.DataPoints {
					sum += dp.Sum
					found = true
				}
			}
		}
	}
	if !found || sum < 1.5 || sum == math.Trunc(sum) {
		t.Fatalf("bd.storage.operation.duration sum = %v ms (found=%v), want a fractional value >= 1.5", sum, found)
	}
}
