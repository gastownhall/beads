//go:build cgo

package proxy

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// These spike tests validate the core hypothesis of the pooling proxy against a
// real dolt sql-server: (1) a go-sql-driver client can run queries and
// transactions through a handshake-terminating, connection-pooling proxy;
// (2) COM_RESET_CONNECTION isolates session state between successive borrowers;
// (3) the dolt-side Connections counter stays flat across many sequential
// client sessions instead of growing 1:1.
//
// They are gated on dolt being installed (skip otherwise) and require cgo
// (servercfg). Run: go test ./internal/storage/dbproxy/proxy -run Spike.

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func spikeDolt(t *testing.T) (srv *server.DoltServer, port int) {
	t.Helper()
	bin, err := exec.LookPath("dolt")
	if err != nil {
		t.Skipf("dolt not on PATH: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	rootDir := t.TempDir()
	port = freeTCPPort(t)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("log_level: warning\nlistener:\n  host: 127.0.0.1\n  port: %d\n", port)
	require.NoError(t, os.WriteFile(cfgPath, []byte(body), 0o600))
	logPath := filepath.Join(t.TempDir(), "server.log")
	srv, err = server.NewDoltServer(bin, rootDir, cfgPath, logPath, 0)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, srv.Start(ctx))
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer scancel()
		_ = srv.Stop(sctx)
	})
	return srv, port
}

// servePooled runs a minimal pooling accept loop in the background and returns
// the address clients should dial. It mirrors what proxyServer.handleConn will
// do in pooling mode.
func servePooled(t *testing.T, pool *backendPool, stats *Stats) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	var connID atomic.Uint32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(client net.Conn) {
				defer func() { _ = client.Close() }()
				id := connID.Add(1)
				res, err := runPooledSession(context.Background(), stats, pool, client, id)
				if err != nil {
					return
				}
				if res.backend == nil {
					return
				}
				if res.reusable {
					pool.put(res.backend)
				} else {
					_ = res.backend.conn.Close()
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func newSpikePool(t *testing.T, srv *server.DoltServer, maxIdle int) (*backendPool, *Stats) {
	t.Helper()
	stats := &Stats{}
	pool := newBackendPool(
		func(ctx context.Context) (net.Conn, error) { return srv.Dial(ctx) },
		"root", "", maxIdle, 0, stats,
	)
	t.Cleanup(pool.drain)
	return pool, stats
}

func openThroughProxy(t *testing.T, addr, db string) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("root@tcp(%s)/%s?parseTime=true&timeout=10s&readTimeout=10s&writeTimeout=10s", addr, db)
	dbConn, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	dbConn.SetMaxOpenConns(1) // one client TCP conn at a time for deterministic pooling
	return dbConn
}

func TestSpike_PooledQueryRoundTrip(t *testing.T) {
	srv, _ := spikeDolt(t)
	pool, stats := newSpikePool(t, srv, 4)
	addr := servePooled(t, pool, stats)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create the database via the proxy (no DB selected yet).
	admin := openThroughProxy(t, addr, "")
	defer func() { _ = admin.Close() }()
	_, err := admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS spikedb")
	require.NoError(t, err)
	_ = admin.Close()

	// Now connect WITH the database (exercises CONNECT_WITH_DB handshake path).
	conn := openThroughProxy(t, addr, "spikedb")
	defer func() { _ = conn.Close() }()

	var one int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT 1").Scan(&one))
	require.Equal(t, 1, one)

	_, err = conn.ExecContext(ctx, "CREATE TABLE t (id INT PRIMARY KEY, v VARCHAR(32))")
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "INSERT INTO t VALUES (1, 'hello'), (2, 'world')")
	require.NoError(t, err)

	var v string
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT v FROM t WHERE id=2").Scan(&v))
	require.Equal(t, "world", v)

	// Transaction + DOLT_COMMIT round-trip (bd's write pattern).
	_, err = conn.ExecContext(ctx, "INSERT INTO t VALUES (3, 'tx')")
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'spike commit')")
	require.NoError(t, err)

	var cnt int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM t").Scan(&cnt))
	require.Equal(t, 3, cnt)

	t.Logf("stats: %+v", stats.Snapshot())
}

func TestSpike_SessionIsolation(t *testing.T) {
	srv, _ := spikeDolt(t)
	pool, stats := newSpikePool(t, srv, 1) // force a single shared backend
	addr := servePooled(t, pool, stats)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create a database so temp tables have somewhere to live, then drain the
	// admin backend so the isolation check below runs against a single shared
	// pooled connection keyed by db="isodb".
	admin := openThroughProxy(t, addr, "")
	_, err := admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS isodb")
	require.NoError(t, err)
	require.NoError(t, admin.Close())

	// Borrower #1: set a session var and an autocommit override, then a temp
	// table — all session-scoped state that must NOT survive to borrower #2.
	c1 := openThroughProxy(t, addr, "isodb")
	conn1, err := c1.Conn(ctx)
	require.NoError(t, err)
	_, err = conn1.ExecContext(ctx, "SET @leak = 42")
	require.NoError(t, err)
	_, err = conn1.ExecContext(ctx, "SET autocommit = 0")
	require.NoError(t, err)
	_, err = conn1.ExecContext(ctx, "CREATE TEMPORARY TABLE leaks (x INT)")
	require.NoError(t, err)
	var leak sql.NullInt64
	require.NoError(t, conn1.QueryRowContext(ctx, "SELECT @leak").Scan(&leak))
	require.True(t, leak.Valid && leak.Int64 == 42, "borrower #1 should see its own var")
	require.NoError(t, conn1.Close())
	require.NoError(t, c1.Close()) // returns backend to pool (reset)

	// Give the pool a moment to reset+return the single backend.
	require.Eventually(t, func() bool { return pool.idleCount() == 1 }, 5*time.Second, 20*time.Millisecond)

	// Borrower #2: must reuse the SAME backend and see a clean session.
	c2 := openThroughProxy(t, addr, "isodb")
	defer func() { _ = c2.Close() }()
	conn2, err := c2.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn2.Close() }()

	var leak2 sql.NullInt64
	require.NoError(t, conn2.QueryRowContext(ctx, "SELECT @leak").Scan(&leak2))
	require.False(t, leak2.Valid, "session var @leak leaked across borrowers: %v", leak2)

	var autocommit int
	require.NoError(t, conn2.QueryRowContext(ctx, "SELECT @@autocommit").Scan(&autocommit))
	require.Equal(t, 1, autocommit, "autocommit override leaked across borrowers")

	// The temp table must be gone: querying it should error (unknown table).
	var n int
	err = conn2.QueryRowContext(ctx, "SELECT COUNT(*) FROM leaks").Scan(&n)
	require.Error(t, err, "temp table 'leaks' leaked across borrowers")

	snap := stats.Snapshot()
	t.Logf("stats: %+v", snap)
	require.GreaterOrEqual(t, snap.PoolResets, int64(1), "expected at least one COM_RESET_CONNECTION")
	require.GreaterOrEqual(t, snap.PoolHits, int64(1), "borrower #2 should have reused the pooled backend")
}

func TestSpike_ConnectionCounterFlat(t *testing.T) {
	srv, port := spikeDolt(t)
	pool, stats := newSpikePool(t, srv, 2)
	addr := servePooled(t, pool, stats)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A persistent direct (non-proxied) connection to read the global counter
	// with constant overhead.
	directDSN := fmt.Sprintf("root@tcp(127.0.0.1:%d)/?timeout=10s", port)
	direct, err := sql.Open("mysql", directDSN)
	require.NoError(t, err)
	direct.SetMaxOpenConns(1)
	defer func() { _ = direct.Close() }()

	readConnections := func() int64 {
		var name string
		var val int64
		require.NoError(t, direct.QueryRowContext(ctx, "SHOW STATUS LIKE 'Connections'").Scan(&name, &val))
		return val
	}

	// Warm the pool once.
	warm := openThroughProxy(t, addr, "")
	require.NoError(t, warm.PingContext(ctx))
	_, _ = warm.ExecContext(ctx, "SELECT 1")
	_ = warm.Close()
	require.Eventually(t, func() bool { return pool.idleCount() >= 1 }, 5*time.Second, 20*time.Millisecond)

	before := readConnections()

	const N = 40
	for i := 0; i < N; i++ {
		c := openThroughProxy(t, addr, "")
		var one int
		require.NoError(t, c.QueryRowContext(ctx, "SELECT 1").Scan(&one))
		require.NoError(t, c.Close())
		require.Eventually(t, func() bool { return pool.idleCount() >= 1 }, 5*time.Second, 10*time.Millisecond)
	}

	after := readConnections()
	delta := after - before
	snap := stats.Snapshot()
	t.Logf("Connections delta over %d sequential pooled sessions: %d (pool stats: %+v)", N, delta, snap)

	// With pooling, the dolt-side Connections counter must stay essentially
	// flat (well under N). Allow a small slack for incidental reconnects.
	require.Less(t, delta, int64(N/2), "Connections grew ~1:1 with sessions — pooling not effective")
	require.GreaterOrEqual(t, snap.PoolHits, int64(N-2), "most sessions should be pool hits")
}
