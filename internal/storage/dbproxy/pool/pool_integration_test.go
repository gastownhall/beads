package pool_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/pool"
)

// These tests exercise the pooler against a LIVE Dolt sql-server. They are
// gated on BEADS_POOL_TEST_DOLT_ADDR (e.g. "127.0.0.1:3307"); when it is unset
// the tests skip, so CI without a Dolt server stays green. Each test creates
// and drops its own throwaway database (poolspike_test*) and never touches real
// data.

const testDBPrefix = "poolspike_test"

func doltAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("BEADS_POOL_TEST_DOLT_ADDR")
	if addr == "" {
		t.Skip("BEADS_POOL_TEST_DOLT_ADDR not set; skipping live-Dolt pooler test")
	}
	return addr
}

// adminDB opens a direct (non-pooled) connection to Dolt for test setup/teardown
// and assertions that must bypass the pooler.
func adminDB(t *testing.T, addr, dbName string) *sql.DB {
	t.Helper()
	cfg := gomysql.Config{
		User: "root", Net: "tcp", Addr: addr, DBName: dbName,
		AllowNativePasswords: true,
	}
	cfg.TLSConfig = "false"
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	db.SetMaxOpenConns(2)
	return db
}

func makeTestDB(t *testing.T, addr string) string {
	t.Helper()
	name := fmt.Sprintf("%s_%d", testDBPrefix, time.Now().UnixNano())
	admin := adminDB(t, addr, "")
	defer admin.Close()
	if _, err := admin.Exec("CREATE DATABASE `" + name + "`"); err != nil {
		t.Fatalf("create test db %s: %v", name, err)
	}
	t.Cleanup(func() {
		a := adminDB(t, addr, "")
		defer a.Close()
		_, _ = a.Exec("DROP DATABASE IF EXISTS `" + name + "`")
	})
	// Dolt can lag between CREATE DATABASE and visibility; poll until usable.
	deadline := time.Now().Add(10 * time.Second)
	for {
		a := adminDB(t, addr, name)
		err := a.Ping()
		a.Close()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("test db %s never became usable: %v", name, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return name
}

// poolSocket returns a short unix socket path under the test's temp dir.
func poolSocket(t *testing.T) string {
	t.Helper()
	// Unix socket paths are length-limited (~104 bytes on macOS); keep short.
	dir, err := os.MkdirTemp("", "poolsock")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "p.sock")
}

func startServer(t *testing.T, addr string, size int) (*pool.Server, *pool.Stats) {
	t.Helper()
	sock := poolSocket(t)
	stats := &pool.Stats{}
	srv, err := pool.NewServer(context.Background(), pool.ServerConfig{
		Socket: sock,
		Pool:   pool.Config{Addr: addr, Size: size, Stats: stats, KeepaliveInterval: time.Second},
		Logf:   func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go srv.Serve()
	t.Cleanup(srv.Shutdown)
	// Wait for the socket to be accepting.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pooler socket %s never appeared", sock)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return srv, stats
}

// pooledDB opens a go-sql-driver connection THROUGH the pooler socket, mirroring
// bd's server-mode DSN (notably: interpolateParams is NOT set, so go-sql-driver
// uses server-side prepared statements for parameterized queries).
func pooledDB(t *testing.T, socket, dbName string, maxConns int) *sql.DB {
	t.Helper()
	cfg := gomysql.Config{
		User: "root", Net: "unix", Addr: socket, DBName: dbName,
		ParseTime: true, MultiStatements: true, AllowNativePasswords: true,
	}
	cfg.TLSConfig = "false"
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("open pooled db: %v", err)
	}
	db.SetMaxOpenConns(maxConns)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPool_PreparedStatementRoundTrip(t *testing.T) {
	addr := doltAddr(t)
	dbName := makeTestDB(t, addr)
	srv, _ := startServer(t, addr, 4)
	db := pooledDB(t, srv.Socket(), dbName, 4)

	if _, err := db.Exec("CREATE TABLE t (id int primary key, name varchar(64))"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	// Parameterized INSERT -> COM_STMT_PREPARE/EXECUTE through the pooler.
	if _, err := db.Exec("INSERT INTO t (id, name) VALUES (?, ?)", 1, "alice"); err != nil {
		t.Fatalf("param insert: %v", err)
	}
	// Parameterized SELECT.
	var name string
	if err := db.QueryRow("SELECT name FROM t WHERE id = ?", 1).Scan(&name); err != nil {
		t.Fatalf("param select: %v", err)
	}
	if name != "alice" {
		t.Fatalf("got name %q, want alice", name)
	}
	// Multi-row parameterized query.
	if _, err := db.Exec("INSERT INTO t (id, name) VALUES (?, ?)", 2, "bob"); err != nil {
		t.Fatalf("param insert 2: %v", err)
	}
	rows, err := db.Query("SELECT id, name FROM t WHERE id >= ? ORDER BY id", 1)
	if err != nil {
		t.Fatalf("param multi-row query: %v", err)
	}
	defer rows.Close()
	got := map[int]string{}
	for rows.Next() {
		var id int
		var nm string
		if err := rows.Scan(&id, &nm); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = nm
	}
	if got[1] != "alice" || got[2] != "bob" {
		t.Fatalf("unexpected rows: %v", got)
	}
}

func TestPool_ClientIsolation_NoBleed(t *testing.T) {
	addr := doltAddr(t)
	dbName := makeTestDB(t, addr)
	// K=1: every client is forced onto the SAME backend, so any session bleed
	// (user vars, session vars, default branch) would be observable across
	// distinct clients. Each client below is a SEPARATE *sql.DB that we fully
	// Close, which drops the client TCP/unix connection and triggers the
	// pooler's ConnectionClosed -> reset-on-release path. (Using db.Conn().Close
	// would instead return the conn to go-sql-driver's own pool, which sends its
	// own COM_RESET and would not exercise the pooler's boundary reset.)
	srv, _ := startServer(t, addr, 1)

	// Client A: dirty the session, then drop the connection entirely.
	{
		a := pooledDBNoCleanup(t, srv.Socket(), dbName)
		if _, err := a.Exec("SET @leak = 'boom'"); err != nil {
			t.Fatalf("set user var: %v", err)
		}
		if _, err := a.Exec("SET autocommit = 0"); err != nil {
			t.Fatalf("set autocommit: %v", err)
		}
		// Also switch the active Dolt branch to prove branch state is reset.
		if _, err := a.Exec("CALL DOLT_CHECKOUT('-b', 'pooltest_branch')"); err != nil {
			// Branch may already exist from a prior run; ignore creation errors
			// but try a plain checkout so the session branch is non-main.
			_, _ = a.Exec("CALL DOLT_CHECKOUT('pooltest_branch')")
		}
		_ = a.Close() // drop client conn -> pooler resets the backend
	}

	// Wait for the backend to be released+reset back into the pool (Release
	// happens in the pooler's ConnectionClosed handler, asynchronously w.r.t.
	// the client Close()).
	time.Sleep(200 * time.Millisecond)

	// Client B on the same (only) backend must see clean state.
	b := pooledDBNoCleanup(t, srv.Socket(), dbName)
	defer b.Close()

	var leak sql.NullString
	if err := b.QueryRow("SELECT @leak").Scan(&leak); err != nil {
		t.Fatalf("select user var: %v", err)
	}
	if leak.Valid {
		t.Fatalf("user var leaked across clients: %q", leak.String)
	}
	var autocommit string
	if err := b.QueryRow("SELECT @@autocommit").Scan(&autocommit); err != nil {
		t.Fatalf("select autocommit: %v", err)
	}
	if autocommit != "1" {
		t.Fatalf("autocommit leaked: got %q want 1", autocommit)
	}
	var branch string
	if err := b.QueryRow("SELECT active_branch()").Scan(&branch); err != nil {
		t.Fatalf("select active_branch: %v", err)
	}
	if branch != "main" {
		t.Fatalf("active branch leaked: got %q want main", branch)
	}
}

// pooledDBNoCleanup opens a pooled connection that the caller is responsible
// for Closing (used to deterministically drop client connections mid-test).
func pooledDBNoCleanup(t *testing.T, socket, dbName string) *sql.DB {
	t.Helper()
	cfg := gomysql.Config{
		User: "root", Net: "unix", Addr: socket, DBName: dbName,
		ParseTime: true, MultiStatements: true, AllowNativePasswords: true,
	}
	cfg.TLSConfig = "false"
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("open pooled db: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func TestPool_CommitVisibleAcrossClients(t *testing.T) {
	addr := doltAddr(t)
	dbName := makeTestDB(t, addr)
	srv, _ := startServer(t, addr, 4)
	db := pooledDB(t, srv.Socket(), dbName, 4)

	if _, err := db.Exec("CREATE TABLE c (id int primary key, v int)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Writer client inserts and DOLT_COMMITs through the pool.
	writer, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("writer conn: %v", err)
	}
	if _, err := writer.ExecContext(context.Background(), "INSERT INTO c (id, v) VALUES (1, 42)"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := writer.ExecContext(context.Background(), "CALL DOLT_COMMIT('-A', '-m', 'pool test commit')"); err != nil {
		t.Fatalf("dolt_commit: %v", err)
	}
	_ = writer.Close()

	// A separate pooled reader must see the committed row.
	var v int
	if err := db.QueryRow("SELECT v FROM c WHERE id = 1").Scan(&v); err != nil {
		t.Fatalf("read after commit: %v", err)
	}
	if v != 42 {
		t.Fatalf("got v=%d want 42", v)
	}
}

func TestPool_ChurnCollapsesConnections(t *testing.T) {
	addr := doltAddr(t)
	dbName := makeTestDB(t, addr)
	const k = 4
	srv, _ := startServer(t, addr, k)

	admin := adminDB(t, addr, "")
	defer admin.Close()
	baseline := doltConnectionsCounter(t, admin)

	// Many short-lived clients, each opening its own connection, running one
	// query, and closing. Through a direct connection this would create ~N new
	// Dolt connections; through the pooler it should stay ~k.
	const n = 60
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := gomysql.Config{
				User: "root", Net: "unix", Addr: srv.Socket(), DBName: dbName,
				AllowNativePasswords: true,
			}
			cfg.TLSConfig = "false"
			c, err := sql.Open("mysql", cfg.FormatDSN())
			if err != nil {
				errCh <- err
				return
			}
			defer c.Close()
			c.SetMaxOpenConns(1)
			var one int
			if err := c.QueryRow("SELECT 1").Scan(&one); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("churn client error: %v", err)
		}
	}

	after := doltConnectionsCounter(t, admin)
	delta := after - baseline
	// The pooler holds k persistent backends; churn should add at most a small
	// constant (warmup already happened, so ideally ~0). Allow generous slack
	// for the admin connection and Dolt internals, but assert it is FAR below n.
	if delta > int64(k)+8 {
		t.Fatalf("churn did not collapse: Dolt Connections rose by %d over %d clients (want <= %d)", delta, n, int64(k)+8)
	}
	t.Logf("churn: %d clients -> Dolt Connections delta %d (pool size %d)", n, delta, k)
}

// doltConnectionsCounter reads the global Connections status counter (total
// connections accepted since server start). The delta across a churn workload
// reveals how many NEW backend connections Dolt saw.
func doltConnectionsCounter(t *testing.T, admin *sql.DB) int64 {
	t.Helper()
	var name string
	var val string
	if err := admin.QueryRow("SHOW GLOBAL STATUS LIKE 'Connections'").Scan(&name, &val); err != nil {
		t.Fatalf("show status Connections: %v", err)
	}
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		t.Fatalf("parse Connections %q: %v", val, err)
	}
	return n
}
