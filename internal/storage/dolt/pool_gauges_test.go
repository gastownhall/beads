package dolt

import (
	"context"
	"database/sql"
	"maps"
	"strings"
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

// poolGauges collects the bd.db.pool_* gauges by name; a gauge with no
// observed point is absent.
func poolGauges(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	values := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if !strings.HasPrefix(m.Name, "bd.db.pool_") {
				continue
			}
			g, isGauge := m.Data.(metricdata.Gauge[int64])
			if !isGauge {
				t.Fatalf("%s is %T, want an int64 gauge", m.Name, m.Data)
			}
			for _, dp := range g.DataPoints {
				values[m.Name] += dp.Value
			}
		}
	}
	return values
}

// poolMaxOpen collects bd.db.pool_max_open; ok is false when no point was
// observed.
func poolMaxOpen(t *testing.T, reader *sdkmetric.ManualReader) (value int64, ok bool) {
	t.Helper()
	value, ok = poolGauges(t, reader)["bd.db.pool_max_open"]
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

	// With no store open the gauges report the pools closed since the last
	// open, at their stats when they closed, so a short command whose stores
	// close before telemetry.Shutdown still exports them rather than zeros.
	untrackSecond()
	if got, _ := poolMaxOpen(t, reader); got != 8 {
		t.Fatalf("pool_max_open after both stores closed = %d, want 8 (their stats at Close)", got)
	}

	// The next open drops the closed pools again.
	untrackThird := pools.track(openUnconnectedDB(t, 7))
	if got, _ := poolMaxOpen(t, reader); got != 7 {
		t.Fatalf("pool_max_open after a new open = %d, want 7", got)
	}
	untrackThird()
	if got, _ := poolMaxOpen(t, reader); got != 7 {
		t.Fatalf("pool_max_open after the third store closed = %d, want 7 (the open dropped the earlier closed pools)", got)
	}
}

// TestRegisterPoolGaugesTracksEachStoreUntilClose drives the real open/Close
// path: each store's registerPoolGauges adds its own pool to the one
// process-wide set, and its Close removes that pool and no other.
func TestRegisterPoolGaugesTracksEachStoreUntilClose(t *testing.T) {
	tracked := func(db *sql.DB) bool {
		processPools.mu.Lock()
		defer processPools.mu.Unlock()
		_, ok := processPools.dbs[db]
		return ok
	}
	first, second := &DoltStore{db: openUnconnectedDB(t, 3)}, &DoltStore{db: openUnconnectedDB(t, 5)}
	firstDB, secondDB := first.db, second.db
	first.registerPoolGauges()
	second.registerPoolGauges()
	if !tracked(firstDB) || !tracked(secondDB) {
		t.Fatal("both opens must be tracked")
	}
	_ = first.Close()
	if tracked(firstDB) || !tracked(secondDB) {
		t.Fatal("Close must untrack only its own pool")
	}
	_ = first.Close()
	_ = second.Close()
	if tracked(secondDB) {
		t.Fatal("second Close must untrack")
	}
}

// TestPoolGaugesKeepEachStatAtClose pins what a short command's final export
// reads. Close must take the pool's stats before it closes the pool, because
// sql.DB.Close drops the idle connections, and each stat must reach its own
// gauge. The pool holds two connections and has one idle, so the four gauges
// all differ and a swapped pair shows.
func TestPoolGaugesKeepEachStatAtClose(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	pools := &poolSet{}
	if err := pools.register(provider.Meter("test")); err != nil {
		t.Fatalf("register: %v", err)
	}

	db, _ := openMockDB(t)
	db.SetMaxOpenConns(5)
	ctx := context.Background()
	conns := make([]*sql.Conn, 3)
	for i := range conns {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("db.Conn: %v", err)
		}
		conns[i] = conn
	}
	// Return one connection to the idle list and hold the other two.
	if err := conns[0].Close(); err != nil {
		t.Fatalf("release a connection: %v", err)
	}
	t.Cleanup(func() {
		_ = conns[1].Close()
		_ = conns[2].Close()
	})

	store := &DoltStore{db: db}
	store.untrackPool = pools.track(db)
	want := map[string]int64{
		"bd.db.pool_open":     3,
		"bd.db.pool_in_use":   2,
		"bd.db.pool_idle":     1,
		"bd.db.pool_max_open": 5,
	}
	if got := poolGauges(t, reader); !maps.Equal(got, want) {
		t.Fatalf("pool gauges with the store open = %v, want %v", got, want)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := poolGauges(t, reader); !maps.Equal(got, want) {
		t.Fatalf("pool gauges after Close = %v, want %v (the pool's stats at Close)", got, want)
	}
}
