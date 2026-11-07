package dolt

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// openUnconnectedDB returns a *sql.DB that never dials: sql.Open only parses
// the DSN, and Stats() reads the pool's own bookkeeping.
func openUnconnectedDB(t *testing.T, maxOpen int) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", "root@tcp(127.0.0.1:1)/x")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(maxOpen)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// poolMaxOpen collects bd.db.pool_max_open; ok is false when no point was
// observed.
func poolMaxOpen(t *testing.T, reader *sdkmetric.ManualReader) (value int64, ok bool) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "bd.db.pool_max_open" {
				continue
			}
			g, isGauge := m.Data.(metricdata.Gauge[int64])
			if !isGauge {
				t.Fatalf("bd.db.pool_max_open is %T, want an int64 gauge", m.Data)
			}
			for _, dp := range g.DataPoints {
				value += dp.Value
				ok = true
			}
		}
	}
	return value, ok
}

// TestPoolGaugesReportEveryOpenStore pins that the sql.DB pool gauges follow
// the stores that are open now. They used to be registered per store with an
// inline callback; the SDK keeps only the first callback of an instrument, so
// a process that opened a second store (bd serve, tests, multi-repo commands)
// reported the first store's pool forever, even after it closed.
func TestPoolGaugesReportEveryOpenStore(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	pools := &poolSet{}
	if err := pools.register(provider.Meter("test")); err != nil {
		t.Fatalf("register: %v", err)
	}

	untrackFirst := pools.track(openUnconnectedDB(t, 3))
	if got, _ := poolMaxOpen(t, reader); got != 3 {
		t.Fatalf("pool_max_open with one store = %d, want 3", got)
	}

	untrackSecond := pools.track(openUnconnectedDB(t, 5))
	if got, _ := poolMaxOpen(t, reader); got != 8 {
		t.Fatalf("pool_max_open with two stores = %d, want 8 (both pools)", got)
	}

	untrackFirst()
	untrackFirst() // idempotent: Close may run more than once
	if got, _ := poolMaxOpen(t, reader); got != 5 {
		t.Fatalf("pool_max_open after closing the first store = %d, want 5", got)
	}

	untrackSecond()
	if got, _ := poolMaxOpen(t, reader); got != 0 {
		t.Fatalf("pool_max_open with no store open = %d, want 0", got)
	}
}
