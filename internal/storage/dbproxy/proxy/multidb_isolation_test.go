//go:build cgo

package proxy

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// TestSpike_MultiDatabaseIsolation is the decisive check for multi-database
// topologies (e.g. one dolt sql-server hosting one database per rig: hq, vw,
// va, ...). The pool is keyed by (capabilities, database); a backend
// authenticated to database A must NEVER be lent to a client that asked for
// database B, because COM_RESET_CONNECTION restores the session to the
// backend's *handshake* database — silent cross-store reads otherwise.
//
// This only holds if go-sql-driver carries the database in the MySQL handshake
// (CONNECT_WITH_DB), so the proxy can read it from the client's handshake
// response and key the pool by it. If the driver instead connected with an
// empty schema and issued COM_INIT_DB afterwards, every client would key to
// db="" and share one pool — the corruption this test is designed to catch.
//
// The test forces maximal reuse pressure (pool size effectively 1 per key) and
// interleaves clients for two databases, asserting each reads only its own.
func TestSpike_MultiDatabaseIsolation(t *testing.T) {
	srv, _ := spikeDolt(t)
	pool, stats := newSpikePool(t, srv, 1) // 1 idle per key
	addr := servePooled(t, pool, stats)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Provision two databases, each with a sentinel row identifying itself.
	admin := openThroughProxy(t, addr, "")
	for _, db := range []string{"rig_a", "rig_b"} {
		_, err := admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+db)
		require.NoError(t, err)
	}
	require.NoError(t, admin.Close())
	for _, tc := range []struct{ db, who string }{{"rig_a", "A"}, {"rig_b", "B"}} {
		c := openThroughProxy(t, addr, tc.db)
		_, err := c.ExecContext(ctx, "CREATE TABLE marker (who VARCHAR(8))")
		require.NoError(t, err)
		_, err = c.ExecContext(ctx, "INSERT INTO marker VALUES (?)", tc.who)
		require.NoError(t, err)
		require.NoError(t, c.Close())
	}

	readWho := func(db string) string {
		c := openThroughProxy(t, addr, db)
		defer func() { _ = c.Close() }()
		var who string
		// Unqualified table name → resolves against the session's current
		// database, i.e. exactly what a leaked backend would get wrong.
		require.NoError(t, c.QueryRowContext(ctx, "SELECT who FROM marker").Scan(&who))
		return who
	}

	// Interleave many times with single-connection clients so each open must
	// reuse a pooled backend. A keying bug (shared db="") would surface as a
	// client reading the other rig's marker.
	for i := 0; i < 20; i++ {
		require.Equal(t, "A", readWho("rig_a"), "rig_a read the wrong store on iter %d", i)
		require.Equal(t, "B", readWho("rig_b"), "rig_b read the wrong store on iter %d", i)
	}

	// Also assert the two databases keep distinct backends (no accidental
	// collapse): with reuse, hits should dominate and both keys stay warm.
	snap := stats.Snapshot()
	t.Logf("multi-db stats: %+v", snap)
	require.GreaterOrEqual(t, snap.PoolHits, int64(30), "expected heavy reuse across both db keys")
}

// TestSpike_HandshakeCarriesDatabase nails the underlying assumption directly:
// a client opened with a database in its DSN must surface that database to the
// proxy via the handshake (so the pool key's db is non-empty). We observe it
// indirectly: after a rig_a client runs, the pool holds an idle backend that,
// when reused by another rig_a client, already resolves unqualified names to
// rig_a without any explicit USE.
func TestSpike_HandshakeCarriesDatabase(t *testing.T) {
	srv, _ := spikeDolt(t)
	pool, stats := newSpikePool(t, srv, 2)
	addr := servePooled(t, pool, stats)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin := openThroughProxy(t, addr, "")
	_, err := admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS rig_h")
	require.NoError(t, err)
	require.NoError(t, admin.Close())

	c1 := openThroughProxy(t, addr, "rig_h")
	var cur sql.NullString
	require.NoError(t, c1.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&cur))
	require.True(t, cur.Valid && cur.String == "rig_h",
		"session current database must be the handshake db, got %v", cur)
	require.NoError(t, c1.Close())

	require.Eventually(t, func() bool { return pool.idleCount() >= 1 }, 5*time.Second, 20*time.Millisecond)

	// Reused backend must still report rig_h (reset restores the handshake db).
	c2 := openThroughProxy(t, addr, "rig_h")
	defer func() { _ = c2.Close() }()
	require.NoError(t, c2.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&cur))
	require.True(t, cur.Valid && cur.String == "rig_h", "reused backend lost its db, got %v", cur)
	require.GreaterOrEqual(t, stats.Snapshot().PoolHits, int64(1))
}
