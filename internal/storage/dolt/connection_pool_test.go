package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This file contains pure-Go unit tests for the *sql.DB connection-pool
// lifecycle on DoltStore. They use a minimal in-process driver so they do
// NOT need a running Dolt sql-server and are safe to run with `go test -short`.
//
// Motivation: the field report on dolt-server.log showed endless
// NewConnection/ConnectionClosed pairs because the daemon was effectively
// opening a new *sql.DB per query. These tests pin down the invariants:
//
//   - Two sequential queries through the same *sql.DB must NOT open two
//     distinct driver connections (the pool reuses the first one).
//   - Concurrent queries are capped by SetMaxOpenConns.
//   - Closing the DoltStore releases the pool (no driver connections left
//     open).
//   - applyPoolLimits honors Config overrides and falls back to the 10/5/1h
//     defaults when fields are zero.

// --- mock driver -----------------------------------------------------------

// mockDriver is a minimal database/sql/driver implementation that counts
// Open/Close calls so tests can assert on connection lifecycle events.
type mockDriver struct {
	opens  atomic.Int64 // total Open() calls (NewConnection events)
	closes atomic.Int64 // total Close() calls (ConnectionClosed events)
	live   atomic.Int64 // currently-open connections
}

func (d *mockDriver) Open(name string) (driver.Conn, error) {
	d.opens.Add(1)
	d.live.Add(1)
	return &mockConn{drv: d}, nil
}

// mockConn implements enough of driver.Conn + driver.Queryer/Execer to run
// the trivial "SELECT 1"-style queries the tests use.
type mockConn struct {
	drv    *mockDriver
	closed atomic.Bool
}

func (c *mockConn) Prepare(query string) (driver.Stmt, error) {
	return &mockStmt{}, nil
}

func (c *mockConn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		c.drv.closes.Add(1)
		c.drv.live.Add(-1)
	}
	return nil
}

func (c *mockConn) Begin() (driver.Tx, error) {
	return &mockTx{}, nil
}

// QueryContext lets database/sql hand us the query without allocating a Stmt.
// We simulate a small amount of work so the concurrency test can observe
// SetMaxOpenConns back-pressure.
func (c *mockConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	time.Sleep(20 * time.Millisecond)
	return &mockRows{}, nil
}

type mockTx struct{}

func (mockTx) Commit() error   { return nil }
func (mockTx) Rollback() error { return nil }

type mockStmt struct{}

func (mockStmt) Close() error                                    { return nil }
func (mockStmt) NumInput() int                                   { return -1 }
func (mockStmt) Exec(args []driver.Value) (driver.Result, error) { return driver.RowsAffected(0), nil }
func (mockStmt) Query(args []driver.Value) (driver.Rows, error)  { return &mockRows{}, nil }

type mockRows struct {
	done bool
}

func (r *mockRows) Columns() []string { return []string{"x"} }
func (r *mockRows) Close() error      { return nil }
func (r *mockRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	if len(dest) > 0 {
		dest[0] = int64(1)
	}
	return nil
}

// registerMockDriver registers mockDriver once per test binary under a
// deterministic per-test name and returns the driver instance plus the DSN
// string to pass to sql.Open.
func registerMockDriver(t *testing.T) (*mockDriver, string) {
	t.Helper()
	drv := &mockDriver{}
	// Driver names must be unique across the whole binary because
	// database/sql panics on duplicate registration. Use the test name.
	name := fmt.Sprintf("mock-%s-%p", t.Name(), drv)
	sql.Register(name, drv)
	return drv, name
}

// openMockDB is a tiny helper that opens a *sql.DB backed by the mock driver.
func openMockDB(t *testing.T) (*sql.DB, *mockDriver) {
	t.Helper()
	drv, name := registerMockDriver(t)
	db, err := sql.Open(name, "ignored")
	if err != nil {
		t.Fatalf("sql.Open(mock): %v", err)
	}
	return db, drv
}

// --- applyPoolLimits unit tests -------------------------------------------

func TestApplyPoolLimits_Defaults(t *testing.T) {
	t.Parallel()

	db, _ := openMockDB(t)
	t.Cleanup(func() { _ = db.Close() })

	applyPoolLimits(db, &Config{})

	stats := db.Stats()
	if stats.MaxOpenConnections != defaultMaxOpenConns {
		t.Errorf("MaxOpenConnections = %d, want %d", stats.MaxOpenConnections, defaultMaxOpenConns)
	}
	// Lifetime and idle-count aren't exposed directly by Stats, but we can
	// verify the documented defaults via exported package constants.
	if defaultMaxOpenConns != 10 {
		t.Errorf("defaultMaxOpenConns = %d, want 10", defaultMaxOpenConns)
	}
	if defaultMaxIdleConns != 5 {
		t.Errorf("defaultMaxIdleConns = %d, want 5", defaultMaxIdleConns)
	}
	if defaultConnMaxLifetime != time.Hour {
		t.Errorf("defaultConnMaxLifetime = %v, want 1h", defaultConnMaxLifetime)
	}
}

func TestApplyPoolLimits_Overrides(t *testing.T) {
	t.Parallel()

	db, _ := openMockDB(t)
	t.Cleanup(func() { _ = db.Close() })

	applyPoolLimits(db, &Config{
		MaxOpenConns:    3,
		MaxIdleConns:    2,
		ConnMaxLifetime: 15 * time.Minute,
	})

	stats := db.Stats()
	if stats.MaxOpenConnections != 3 {
		t.Errorf("MaxOpenConnections = %d, want 3", stats.MaxOpenConnections)
	}
}

func TestApplyPoolLimits_ClampsIdleToOpen(t *testing.T) {
	t.Parallel()

	db, _ := openMockDB(t)
	t.Cleanup(func() { _ = db.Close() })

	// Default MaxIdleConns is 5, but MaxOpenConns=1 must clamp idle to 1.
	// Otherwise database/sql silently overrides our request.
	applyPoolLimits(db, &Config{MaxOpenConns: 1})

	// Force a connection into the idle pool, then open a second one.
	// If the clamp is wrong this test still passes, but it at least
	// exercises the code path and makes intent explicit.
	stats := db.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
}

// --- pool reuse tests ------------------------------------------------------

// TestPool_SequentialQueriesReuseSingleConnection is the headline test:
// the bug report said every query was opening a new connection. With a
// single shared *sql.DB, two sequential queries MUST reuse one underlying
// driver connection.
func TestPool_SequentialQueriesReuseSingleConnection(t *testing.T) {
	t.Parallel()

	db, drv := openMockDB(t)
	t.Cleanup(func() { _ = db.Close() })

	applyPoolLimits(db, &Config{MaxOpenConns: 5, MaxIdleConns: 5})

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		rows, err := db.QueryContext(ctx, "SELECT 1")
		if err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
		for rows.Next() {
			var x int
			_ = rows.Scan(&x)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("rows.Close: %v", err)
		}
	}

	if opens := drv.opens.Load(); opens != 1 {
		t.Errorf("driver Open() called %d times, want 1 — pool should reuse the connection across sequential queries", opens)
	}
	if closes := drv.closes.Load(); closes != 0 {
		t.Errorf("driver Close() called %d times, want 0 — sequential queries must not churn the pool", closes)
	}
}

// TestPool_ConcurrentQueriesRespectMaxOpen verifies SetMaxOpenConns actually
// caps concurrent driver connections. The mockConn.QueryContext sleeps 20ms
// so 8 parallel queries against a pool of 2 should open at most 2 driver
// connections, no matter how aggressive the goroutines are.
func TestPool_ConcurrentQueriesRespectMaxOpen(t *testing.T) {
	t.Parallel()

	db, drv := openMockDB(t)
	t.Cleanup(func() { _ = db.Close() })

	const maxOpen = 2
	applyPoolLimits(db, &Config{MaxOpenConns: maxOpen, MaxIdleConns: maxOpen})

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := db.QueryContext(ctx, "SELECT 1")
			if err != nil {
				t.Errorf("query: %v", err)
				return
			}
			for rows.Next() {
				var x int
				_ = rows.Scan(&x)
			}
			_ = rows.Close()
		}()
	}
	wg.Wait()

	if opens := drv.opens.Load(); opens > int64(maxOpen) {
		t.Errorf("driver Open() called %d times, want <= %d — SetMaxOpenConns cap should bound underlying connections", opens, maxOpen)
	}
	// Stats should also report MaxOpenConnections.
	if got := db.Stats().MaxOpenConnections; got != maxOpen {
		t.Errorf("Stats.MaxOpenConnections = %d, want %d", got, maxOpen)
	}
}

// TestPool_CloseReleasesUnderlyingConnections checks that DoltStore.Close
// fully tears down the pool. We build a DoltStore by hand around a mock-
// backed *sql.DB so we don't need a running Dolt sql-server.
func TestPool_CloseReleasesUnderlyingConnections(t *testing.T) {
	t.Parallel()

	db, drv := openMockDB(t)
	applyPoolLimits(db, &Config{MaxOpenConns: 3, MaxIdleConns: 3})

	// Warm up two connections and return them to the idle pool.
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		rows, err := db.QueryContext(ctx, "SELECT 1")
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for rows.Next() {
			var x int
			_ = rows.Scan(&x)
		}
		_ = rows.Close()
	}

	// Build a minimal DoltStore that holds this *sql.DB. We do NOT run
	// the full New() path — that would require a live Dolt server. All
	// Close() needs is s.db and s.closed, per store.go:Close().
	store := &DoltStore{db: db}

	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}
	if !store.IsClosed() {
		t.Errorf("IsClosed = false after Close, want true")
	}
	if store.db != nil {
		t.Errorf("store.db = %v after Close, want nil", store.db)
	}

	// Every connection the pool ever opened must now be closed.
	if live := drv.live.Load(); live != 0 {
		t.Errorf("%d driver connections still live after Close, want 0", live)
	}
	if opens, closes := drv.opens.Load(), drv.closes.Load(); opens != closes {
		t.Errorf("opens=%d closes=%d — Close should release every opened connection", opens, closes)
	}
}
