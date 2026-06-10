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
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
)

// These tests drive the REAL proxyServer (ListenAndServe → handleConn →
// handlePooledConn) in pooling mode against a real dolt sql-server that the
// proxy itself manages, validating the production path end to end. Gated on
// dolt + cgo.

// startManagedPooledProxy creates an unstarted DoltServer and a proxyServer
// configured to pool, runs the proxy (which starts dolt) in the background, and
// returns the proxy address, the dolt port (for direct counter reads), and the
// stats. The proxy is stopped on test cleanup.
func startManagedPooledProxy(t *testing.T, poolSize int) (proxyAddr string, doltPort int, stats *Stats) {
	t.Helper()
	bin, err := exec.LookPath("dolt")
	if err != nil {
		t.Skipf("dolt not on PATH: %v", err)
	}
	t.Setenv("HOME", t.TempDir())

	doltPort = freeTCPPort(t)
	rootDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("log_level: warning\nlistener:\n  host: 127.0.0.1\n  port: %d\n", doltPort)
	require.NoError(t, os.WriteFile(cfgPath, []byte(body), 0o600))
	logPath := filepath.Join(t.TempDir(), "server.log")

	srv, err := server.NewDoltServer(bin, rootDir, cfgPath, logPath, 0)
	require.NoError(t, err)

	proxyPort := freeTCPPort(t)
	proxyRoot := t.TempDir()
	stats = &Stats{}
	p := NewProxyServer(ProxyOpts{
		RootDir:     proxyRoot,
		Port:        proxyPort,
		IdleTimeout: 0,
		Server:      srv,
		Stats:       stats,
		PoolSize:    poolSize,
		BackendUser: "root",
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- p.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(30 * time.Second):
			t.Log("proxy did not shut down within 30s")
		}
	})

	// Wait for the proxy port to accept connections.
	proxyAddr = fmt.Sprintf("127.0.0.1:%d", proxyPort)
	require.Eventually(t, func() bool {
		return probePort(Endpoint{Host: "127.0.0.1", Port: proxyPort}, 300*time.Millisecond)
	}, 60*time.Second, 200*time.Millisecond, "proxy never became ready")
	return proxyAddr, doltPort, stats
}

func TestIntegration_ManagedProxyPooling(t *testing.T) {
	addr, _, stats := startManagedPooledProxy(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin := openThroughProxy(t, addr, "")
	_, err := admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS itdb")
	require.NoError(t, err)
	require.NoError(t, admin.Close())

	db := openThroughProxy(t, addr, "itdb")
	defer func() { _ = db.Close() }()

	_, err = db.ExecContext(ctx, "CREATE TABLE t (id INT PRIMARY KEY)")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "INSERT INTO t VALUES (1),(2),(3)")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'it')")
	require.NoError(t, err)

	var n int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM t").Scan(&n))
	require.Equal(t, 3, n)
	t.Logf("stats: %+v", stats.Snapshot())
}

func TestIntegration_TransactionRollback(t *testing.T) {
	addr, _, _ := startManagedPooledProxy(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin := openThroughProxy(t, addr, "")
	_, err := admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS rb")
	require.NoError(t, err)
	require.NoError(t, admin.Close())

	db := openThroughProxy(t, addr, "rb")
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(ctx, "CREATE TABLE t (id INT PRIMARY KEY)")
	require.NoError(t, err)

	// Begin, insert, rollback — the row must not survive, and the backend must
	// be reusable afterward (no leaked open transaction).
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "INSERT INTO t VALUES (99)")
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())

	var n int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM t WHERE id=99").Scan(&n))
	require.Equal(t, 0, n, "rolled-back row must not persist")
}

func TestIntegration_ConcurrentClientsDistinctBackends(t *testing.T) {
	addr, _, stats := startManagedPooledProxy(t, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	admin := openThroughProxy(t, addr, "")
	_, err := admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS conc")
	require.NoError(t, err)
	require.NoError(t, admin.Close())

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			db := openThroughProxy(t, addr, "conc")
			defer func() { _ = db.Close() }()
			// Hold a session var, sleep, then verify it is still ours — proves
			// concurrent clients did NOT share one backend mid-session.
			conn, err := db.Conn(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = conn.Close() }()
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET @who = %d", id)); err != nil {
				errs <- err
				return
			}
			time.Sleep(150 * time.Millisecond)
			var who int
			if err := conn.QueryRowContext(ctx, "SELECT @who").Scan(&who); err != nil {
				errs <- err
				return
			}
			if who != id {
				errs <- fmt.Errorf("worker %d saw @who=%d — backend shared mid-session", id, who)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	t.Logf("stats after %d concurrent workers: %+v", workers, stats.Snapshot())
}

// BenchmarkConnectionChurn measures the dolt-side Connections counter delta for
// N sequential client sessions with pooling on vs off. Run with:
//
//	go test ./internal/storage/dbproxy/proxy -run NONE -bench ConnectionChurn -benchtime 1x
func BenchmarkConnectionChurn(b *testing.B) {
	for _, tc := range []struct {
		name     string
		poolSize int
	}{
		{"pooling_off", 0},
		{"pooling_on", 4},
	} {
		b.Run(tc.name, func(b *testing.B) {
			benchChurn(b, tc.poolSize)
		})
	}
}

func benchChurn(b *testing.B, poolSize int) {
	b.Helper()
	bin, err := exec.LookPath("dolt")
	if err != nil {
		b.Skipf("dolt not on PATH: %v", err)
	}
	b.Setenv("HOME", b.TempDir())
	doltPort := benchFreePort(b)
	rootDir := b.TempDir()
	cfgPath := filepath.Join(b.TempDir(), "config.yaml")
	body := fmt.Sprintf("log_level: warning\nlistener:\n  host: 127.0.0.1\n  port: %d\n", doltPort)
	require.NoError(b, os.WriteFile(cfgPath, []byte(body), 0o600))
	srv, err := server.NewDoltServer(bin, rootDir, cfgPath, filepath.Join(b.TempDir(), "s.log"), 0)
	require.NoError(b, err)

	proxyPort := benchFreePort(b)
	stats := &Stats{}
	p := NewProxyServer(ProxyOpts{
		RootDir: b.TempDir(), Port: proxyPort, Server: srv, Stats: stats,
		PoolSize: poolSize, BackendUser: "root",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.ListenAndServe(ctx) }()
	addr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	require.Eventually(b, func() bool {
		return probePort(Endpoint{Host: "127.0.0.1", Port: proxyPort}, 300*time.Millisecond)
	}, 60*time.Second, 200*time.Millisecond)

	bctx := context.Background()
	direct, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/?timeout=10s", doltPort))
	require.NoError(b, err)
	direct.SetMaxOpenConns(1)
	defer func() { _ = direct.Close() }()
	readConns := func() int64 {
		var name string
		var v int64
		require.NoError(b, direct.QueryRowContext(bctx, "SHOW STATUS LIKE 'Connections'").Scan(&name, &v))
		return v
	}

	const N = 50
	// warm
	warm, _ := sql.Open("mysql", fmt.Sprintf("root@tcp(%s)/?timeout=10s", addr))
	_ = warm.PingContext(bctx)
	_, _ = warm.ExecContext(bctx, "SELECT 1")
	_ = warm.Close()
	time.Sleep(200 * time.Millisecond)

	before := readConns()
	b.ResetTimer()
	for i := 0; i < N; i++ {
		c, _ := sql.Open("mysql", fmt.Sprintf("root@tcp(%s)/?timeout=10s", addr))
		var one int
		require.NoError(b, c.QueryRowContext(bctx, "SELECT 1").Scan(&one))
		_ = c.Close()
		time.Sleep(10 * time.Millisecond)
	}
	b.StopTimer()
	after := readConns()
	b.ReportMetric(float64(after-before), "dolt_conns/"+fmt.Sprint(N)+"sessions")
	b.Logf("poolSize=%d Connections delta over %d sessions: %d (stats: %+v)", poolSize, N, after-before, stats.Snapshot())
}

func benchFreePort(b *testing.B) int {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(b, err)
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
