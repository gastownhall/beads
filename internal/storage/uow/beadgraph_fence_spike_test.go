package uow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
)

// P0 verification row (ii) for the BDP bead-graph row-level fence
// (engdocs/BDP_GRAPH_CLI_AND_STORAGE_SPEC.md Part B2/B4, ruling 13; the report
// is engdocs/BDP_P0_VERIFICATION_ROWS.md).
//
// Question: on the SQL-server leg, does a pooled connection ever carry the
// session variable @bd_graph_role out of a mutating body? The design claims
// (B2): the clear before COMMIT is load-bearing, and "a connection whose
// transaction is torn down by cancellation is closed by the driver, not
// pooled". This file measures both with the tree's own plumbing:
//
//   - the unit-of-work leg (the serving leg): a real doltSQLProvider over the
//     provider's own DSN builder (buildDSN → util.DoltServerDSN) and opener
//     (openDB), RunTx with its retry/closeAttempt discipline, pinned
//     *sql.Conn + START TRANSACTION (doltserver_tx.go);
//   - the *sql.Tx shape shared by the CLI server leg (DoltStore.withWriteTx,
//     internal/storage/dolt/store.go: BeginTx → fn → Commit or
//     errors.Join(err, tx.Rollback())) and the embedded leg (withConn), over
//     the CLI leg's DSN builder (doltutil.ServerDSN).
//
// Every probe runs on a ONE-connection pool and checks CONNECTION_ID() so the
// statement that follows the body is provably on the same server session.
// The two mid-statement probes (c3, d2) do not guess at timing: a SECOND
// connection watches information_schema.processlist until the body's
// session is executing its SELECT SLEEP(10), and only then is the context
// cancelled — so the cancellation provably lands inside the statement, which
// is the case these probes exist to distinguish from cancellation between
// statements (c1, d1). A probe whose premise never holds fails, loudly.
//
// Gate: a dolt binary on PATH (testutil.RequireDoltBinary, which honours
// BEADS_TEST_SKIP=dolt and fails rather than skips under GITHUB_ACTIONS), so
// the tree's Dolt lane runs these rows like every other real-Dolt test. The
// server is the tree's own launcher, internal/storage/dbproxy/server, under
// t.TempDir() with an isolated DOLT_ROOT_PATH.

const (
	beadGraphSpikeDatabase = "beadgraph_spike"
	beadGraphSpikeInsert   = "INSERT INTO graph_beads_spike (path, revision, last_authority_id, last_epoch) VALUES (?, 'r1', 'a', 1)"
	beadGraphSpikeSleep    = "SELECT SLEEP(10)"
)

func requireBeadGraphSpike(t *testing.T) {
	t.Helper()
	testutil.RequireDoltBinary(t)
}

// readBeadGraphFenceSpikeSQL reads the shared throwaway migration text from the
// schema package's testdata (go test runs with the package dir as cwd).
func readBeadGraphFenceSpikeSQL(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "schema", "testdata", "beadgraph_fence_spike", "9001_beadgraph_fence_spike.up.sql")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spike migration %s: %v", path, err)
	}
	return string(data)
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for a free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// startBeadGraphSpikeServer starts a scratch dolt sql-server through the tree's
// own launcher and returns its endpoint. Everything lives under t.TempDir();
// DOLT_ROOT_PATH is pointed at a scratch dir so the launcher's `dolt config
// --global` never touches the developer's ~/.dolt.
func startBeadGraphSpikeServer(t *testing.T) proxy.Endpoint {
	t.Helper()
	bin, err := exec.LookPath("dolt")
	if err != nil {
		t.Skipf("dolt not on PATH: %v", err)
	}
	t.Setenv("DOLT_ROOT_PATH", filepath.Join(t.TempDir(), "dolt-root"))
	rootDir := filepath.Join(t.TempDir(), "spike_srv")
	port := freeTCPPort(t)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := fmt.Sprintf("log_level: warning\nlistener:\n  host: 127.0.0.1\n  port: %d\n", port)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write server config: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "dolt-server.log")
	srv, err := server.NewDoltServer(bin, rootDir, cfgPath, logPath, 0, "")
	if err != nil {
		t.Fatalf("NewDoltServer: %v", err)
	}
	startCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := srv.Start(startCtx); err != nil {
		t.Fatalf("start scratch dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if err := srv.Stop(stopCtx); err != nil {
			t.Logf("stop scratch dolt sql-server: %v", err)
		}
	})
	return proxy.Endpoint{Host: "127.0.0.1", Port: port}
}

// openUOWPool opens a pool exactly as the unit-of-work provider does: the
// provider's own DSN (buildDSN over util.DoltServerDSN) and opener (openDB),
// then bounded to one connection through the provider's PoolLimits seam.
func openUOWPool(t *testing.T, ep proxy.Endpoint, database string) (*sql.DB, *doltSQLProvider) {
	t.Helper()
	db, err := openDB(context.Background(), buildDSN(ep, database, "root", "", ""))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, serverEndpoint: "tcp:" + ep.Address()}
	p.SetPoolLimits(PoolLimits{MaxOpenConns: 1, MaxIdleConns: 1})
	return db, p
}

// openCLILegPool opens a one-connection pool over the CLI server leg's DSN
// builder (doltutil.ServerDSN, the one DoltStore.buildServerDSN uses).
func openCLILegPool(t *testing.T, ep proxy.Endpoint, database string) *sql.DB {
	t.Helper()
	dsn := doltutil.ServerDSN{Host: ep.Host, Port: ep.Port, User: "root", Database: database}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping CLI-leg pool: %v", err)
	}
	return db
}

// installBeadGraphSpikeFence creates the database, applies the throwaway
// migration text in ONE multi-statement Exec over the provider DSN (the
// execMigrationBody shape) and commits it, so the fence is HEAD state exactly
// as a migration pass would leave it.
func installBeadGraphSpikeFence(t *testing.T, ep proxy.Endpoint) {
	t.Helper()
	ctx := context.Background()
	admin, err := openDB(ctx, buildDSN(ep, "", "root", "", ""))
	if err != nil {
		t.Fatalf("openDB (admin): %v", err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+beadGraphSpikeDatabase); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	db, err := openDB(ctx, buildDSN(ep, beadGraphSpikeDatabase, "root", "", ""))
	if err != nil {
		t.Fatalf("openDB (install): %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, readBeadGraphFenceSpikeSQL(t)); err != nil {
		t.Fatalf("apply the spike migration text over the provider DSN: %v", err)
	}
	if err := schema.DrainCall(ctx, db, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatalf("DOLT_ADD: %v", err)
	}
	if err := schema.DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', 'spike: install the row-level fence')"); err != nil {
		t.Fatalf("DOLT_COMMIT: %v", err)
	}
}

type spikeQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func spikeConnectionID(t *testing.T, q spikeQuerier) int64 {
	t.Helper()
	var id int64
	if err := q.QueryRowContext(context.Background(), "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		t.Fatalf("SELECT CONNECTION_ID(): %v", err)
	}
	return id
}

func spikeRoleVar(t *testing.T, q spikeQuerier) sql.NullInt64 {
	t.Helper()
	var role sql.NullInt64
	if err := q.QueryRowContext(context.Background(), "SELECT @bd_graph_role").Scan(&role); err != nil {
		t.Fatalf("SELECT @bd_graph_role: %v", err)
	}
	return role
}

func spikeRawInsert(q spikeQuerier, path string) error {
	_, err := q.ExecContext(context.Background(), beadGraphSpikeInsert, path)
	return err
}

func spikeRowCount(t *testing.T, q spikeQuerier, path string) int {
	t.Helper()
	var n int
	if err := q.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM graph_beads_spike WHERE path = ?", path).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", path, err)
	}
	return n
}

// requireFenced asserts the trigger refused the statement: MySQL errno 1644
// (SIGNAL), SQLSTATE 45000, the B4 message.
func requireFenced(t *testing.T, err error, what string) {
	t.Helper()
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1644 {
		t.Fatalf("%s: err = %v, want MySQL errno 1644 (the SIGNAL from the fence trigger)", what, err)
	}
	t.Logf("%s: fenced (errno %d, sqlstate %s): %s", what, me.Number, string(me.SQLState[:]), me.Message)
}

func requireUnfenced(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: err = %v, want the write to pass (this is the hazard being documented)", what, err)
	}
	t.Logf("%s: UNFENCED — the raw write passed", what)
}

func waitForPoolState(t *testing.T, db *sql.DB, want func(sql.DBStats) bool, what string) sql.DBStats {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := db.Stats()
		if want(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("pool never reached %s: open=%d inuse=%d idle=%d", what, st.OpenConnections, st.InUse, st.Idle)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func uowRunner(uw UnitOfWork) spikeQuerier {
	return uw.(*baseUOW).tx.Runner()
}

// cancelWhenExecuting watches, from a second connection, for the session
// connID to be executing a statement whose text contains marker, then calls
// cancel. The returned channel reports nil once the statement was observed
// and cancelled, or an error if it was not observed within the deadline —
// which is longer than the statement's own SLEEP, so a premise that never
// holds is reported rather than silently exercising the between-statement
// case. Observation is through information_schema.processlist (ID, INFO),
// which Dolt serves for every live session.
func cancelWhenExecuting(observer *sql.DB, connID int64, marker string, cancel context.CancelFunc) <-chan error {
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(15 * time.Second)
		var last error
		for {
			var info sql.NullString
			err := observer.QueryRowContext(context.Background(), "SELECT INFO FROM information_schema.processlist WHERE ID = ?", connID).Scan(&info)
			switch {
			case err == nil && info.Valid && strings.Contains(info.String, marker):
				cancel()
				done <- nil
				return
			case err != nil && !errors.Is(err, sql.ErrNoRows):
				last = err
			}
			if time.Now().After(deadline) {
				done <- fmt.Errorf("session %d was never observed executing %q in information_schema.processlist (last error: %v)", connID, marker, last)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return done
}

func TestSpikeBeadGraphFencePooledConnectionHygiene(t *testing.T) {
	requireBeadGraphSpike(t)
	ep := startBeadGraphSpikeServer(t)
	installBeadGraphSpikeFence(t, ep)
	ctx := context.Background()

	t.Run("a: UOW body sets, writes, clears before COMMIT — the pooled session stays fenced", func(t *testing.T) {
		db, p := openUOWPool(t, ep, beadGraphSpikeDatabase)
		var bodyConn int64
		err := RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
			r := uowRunner(uw)
			if _, err := r.ExecContext(ctx, "SET @bd_graph_role = 1"); err != nil {
				return "", err
			}
			if _, err := r.ExecContext(ctx, beadGraphSpikeInsert, "b/a-inrole"); err != nil {
				return "", fmt.Errorf("in-role INSERT: %w", err)
			}
			bodyConn = spikeConnectionID(t, r)
			if _, err := r.ExecContext(ctx, "SET @bd_graph_role = NULL"); err != nil {
				return "", err
			}
			return "spike: in-role write (a)", nil
		})
		if err != nil {
			t.Fatalf("RunTx: %v", err)
		}
		if after := spikeConnectionID(t, db); after != bodyConn {
			t.Fatalf("follow-up statement ran on session %d, body ran on %d — the one-connection pool did not reuse the session", after, bodyConn)
		}
		if n := spikeRowCount(t, db, "b/a-inrole"); n != 1 {
			t.Fatalf("committed in-role row count = %d, want 1", n)
		}
		requireFenced(t, spikeRawInsert(db, "b/a-raw"), "raw INSERT on the same pooled session after a clearing commit")
		if role := spikeRoleVar(t, db); role.Valid {
			t.Fatalf("@bd_graph_role = %d on the pooled session, want NULL", role.Int64)
		}
	})

	t.Run("b: UOW body commits WITHOUT clearing — the pooled session is unfenced (hazard)", func(t *testing.T) {
		db, p := openUOWPool(t, ep, beadGraphSpikeDatabase)
		var bodyConn int64
		err := RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
			r := uowRunner(uw)
			if _, err := r.ExecContext(ctx, "SET @bd_graph_role = 1"); err != nil {
				return "", err
			}
			if _, err := r.ExecContext(ctx, beadGraphSpikeInsert, "b/b-inrole"); err != nil {
				return "", fmt.Errorf("in-role INSERT: %w", err)
			}
			bodyConn = spikeConnectionID(t, r)
			return "spike: in-role write (b), no clear", nil
		})
		if err != nil {
			t.Fatalf("RunTx: %v", err)
		}
		if after := spikeConnectionID(t, db); after != bodyConn {
			t.Fatalf("follow-up statement ran on session %d, body ran on %d", after, bodyConn)
		}
		requireUnfenced(t, spikeRawInsert(db, "b/b-raw"), "raw INSERT on the same pooled session after a commit that did not clear")
		if role := spikeRoleVar(t, db); !role.Valid || role.Int64 != 1 {
			t.Fatalf("@bd_graph_role = %+v on the pooled session, want 1 (the leaked variable)", role)
		}
	})

	t.Run("c1: UOW body cancelled between statements — closeAttempt rolls back on WithoutCancel and POOLS the session with the variable set (hazard)", func(t *testing.T) {
		db, p := openUOWPool(t, ep, beadGraphSpikeDatabase)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var bodyConn int64
		err := RunTx(cctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
			r := uowRunner(uw)
			if _, err := r.ExecContext(ctx, "SET @bd_graph_role = 1"); err != nil {
				return "", err
			}
			if _, err := r.ExecContext(ctx, beadGraphSpikeInsert, "b/c1-inrole"); err != nil {
				return "", fmt.Errorf("in-role INSERT: %w", err)
			}
			bodyConn = spikeConnectionID(t, r)
			// The caller's context dies mid-body (client hang-up, deadline).
			cancel()
			// A deferred clear written on the body's own context never reaches
			// the server: go-sql-driver refuses to send on an already-cancelled
			// context (connection.go watchCancel).
			if _, err := r.ExecContext(ctx, "SET @bd_graph_role = NULL"); err != nil {
				return "", fmt.Errorf("deferred clear on the cancelled context: %w", err)
			}
			return "", errors.New("the clear unexpectedly reached the server")
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunTx err = %v, want context.Canceled from the aborted body", err)
		}
		// closeAttempt (tx.go) rolled back on context.WithoutCancel(ctx) and
		// releaseConn returned the session to the pool: it was not discarded.
		st := waitForPoolState(t, db, func(s sql.DBStats) bool { return s.InUse == 0 }, "released")
		if st.OpenConnections != 1 {
			t.Fatalf("open connections after the cancelled body = %d, want 1 (pooled, not closed)", st.OpenConnections)
		}
		if after := spikeConnectionID(t, db); after != bodyConn {
			t.Fatalf("follow-up statement ran on session %d, body ran on %d — the session was replaced, not pooled", after, bodyConn)
		}
		if n := spikeRowCount(t, db, "b/c1-inrole"); n != 0 {
			t.Fatalf("rolled-back in-role row count = %d, want 0 (ROLLBACK did run)", n)
		}
		requireUnfenced(t, spikeRawInsert(db, "b/c1-raw"), "raw INSERT on the pooled session after a cancelled body")
		if role := spikeRoleVar(t, db); !role.Valid || role.Int64 != 1 {
			t.Fatalf("@bd_graph_role = %+v on the pooled session, want 1 (ROLLBACK does not reset user variables)", role)
		}
	})

	t.Run("c2: the remedy — a deferred clear on context.WithoutCancel reaches the server and the pooled session stays fenced", func(t *testing.T) {
		db, p := openUOWPool(t, ep, beadGraphSpikeDatabase)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var bodyConn int64
		err := RunTx(cctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
			r := uowRunner(uw)
			if _, err := r.ExecContext(ctx, "SET @bd_graph_role = 1"); err != nil {
				return "", err
			}
			if _, err := r.ExecContext(ctx, beadGraphSpikeInsert, "b/c2-inrole"); err != nil {
				return "", fmt.Errorf("in-role INSERT: %w", err)
			}
			bodyConn = spikeConnectionID(t, r)
			cancel()
			clearCtx, clearCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer clearCancel()
			if _, err := r.ExecContext(clearCtx, "SET @bd_graph_role = NULL"); err != nil {
				return "", fmt.Errorf("deferred clear on WithoutCancel: %w", err)
			}
			return "", fmt.Errorf("body aborted: %w", ctx.Err())
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunTx err = %v, want context.Canceled from the aborted body", err)
		}
		waitForPoolState(t, db, func(s sql.DBStats) bool { return s.InUse == 0 }, "released")
		if after := spikeConnectionID(t, db); after != bodyConn {
			t.Fatalf("follow-up statement ran on session %d, body ran on %d", after, bodyConn)
		}
		requireFenced(t, spikeRawInsert(db, "b/c2-raw"), "raw INSERT on the pooled session after a cancelled body whose clear ran on WithoutCancel")
		if role := spikeRoleVar(t, db); role.Valid {
			t.Fatalf("@bd_graph_role = %d on the pooled session, want NULL", role.Int64)
		}
	})

	t.Run("c3: UOW body cancelled MID-statement — the driver closes the socket, closeAttempt poisons the session, the pool discards it", func(t *testing.T) {
		db, p := openUOWPool(t, ep, beadGraphSpikeDatabase)
		observer := openCLILegPool(t, ep, beadGraphSpikeDatabase)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var observed <-chan error
		err := RunTx(cctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
			r := uowRunner(uw)
			if _, err := r.ExecContext(ctx, "SET @bd_graph_role = 1"); err != nil {
				return "", err
			}
			// Cancel only once the second connection SEES this session
			// executing the SLEEP: the cancellation lands inside the
			// statement by observation, not by a timer.
			observed = cancelWhenExecuting(observer, spikeConnectionID(t, r), beadGraphSpikeSleep, cancel)
			var x int
			qerr := r.QueryRowContext(ctx, beadGraphSpikeSleep).Scan(&x)
			return "", fmt.Errorf("body aborted mid-statement: %w", qerr)
		})
		if oerr := <-observed; oerr != nil {
			t.Fatalf("premise not established: %v", oerr)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunTx err = %v, want context.Canceled from the interrupted statement", err)
		}
		st := waitForPoolState(t, db, func(s sql.DBStats) bool { return s.OpenConnections == 0 }, "discarded")
		t.Logf("pool after mid-statement cancellation: open=%d (the poisoned session was dropped)", st.OpenConnections)
		requireFenced(t, spikeRawInsert(db, "b/c3-raw"), "raw INSERT on a fresh session after a mid-statement cancellation")
		if role := spikeRoleVar(t, db); role.Valid {
			t.Fatalf("@bd_graph_role = %d on the fresh session, want NULL", role.Int64)
		}
	})

	t.Run("d1: *sql.Tx (CLI/embedded shape) cancelled between statements is rolled back and POOLED — database/sql keeps the connection because the mysql driver implements SessionResetter and Validator (hazard)", func(t *testing.T) {
		db := openCLILegPool(t, ep, beadGraphSpikeDatabase)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		tx, err := db.BeginTx(cctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		if _, err := tx.ExecContext(cctx, "SET @bd_graph_role = 1"); err != nil {
			t.Fatalf("SET role in tx: %v", err)
		}
		if _, err := tx.ExecContext(cctx, beadGraphSpikeInsert, "b/d1-inrole"); err != nil {
			t.Fatalf("in-role INSERT in tx: %v", err)
		}
		txConn := spikeConnectionID(t, tx)
		cancel()
		// Tx.awaitDone (database/sql) and an explicit Rollback both take the
		// keep-connection path: rollback(discardConn=false).
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("Rollback after cancellation: %v", err)
		}
		st := waitForPoolState(t, db, func(s sql.DBStats) bool { return s.InUse == 0 }, "released")
		if st.OpenConnections != 1 {
			t.Fatalf("open connections after the cancelled tx = %d, want 1 (pooled, not closed)", st.OpenConnections)
		}
		if after := spikeConnectionID(t, db); after != txConn {
			t.Fatalf("follow-up statement ran on session %d, tx ran on %d — the session was replaced, not pooled", after, txConn)
		}
		if n := spikeRowCount(t, db, "b/d1-inrole"); n != 0 {
			t.Fatalf("rolled-back in-role row count = %d, want 0 (ROLLBACK did run)", n)
		}
		requireUnfenced(t, spikeRawInsert(db, "b/d1-raw"), "raw INSERT on the pooled session after a cancelled *sql.Tx")
		if role := spikeRoleVar(t, db); !role.Valid || role.Int64 != 1 {
			t.Fatalf("@bd_graph_role = %+v on the pooled session, want 1", role)
		}
	})

	t.Run("d2: *sql.Tx cancelled MID-statement — the driver closes the socket and the pool discards the connection", func(t *testing.T) {
		db := openCLILegPool(t, ep, beadGraphSpikeDatabase)
		observer := openCLILegPool(t, ep, beadGraphSpikeDatabase)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		tx, err := db.BeginTx(cctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		if _, err := tx.ExecContext(cctx, "SET @bd_graph_role = 1"); err != nil {
			t.Fatalf("SET role in tx: %v", err)
		}
		observed := cancelWhenExecuting(observer, spikeConnectionID(t, tx), beadGraphSpikeSleep, cancel)
		var x int
		qerr := tx.QueryRowContext(cctx, beadGraphSpikeSleep).Scan(&x)
		if oerr := <-observed; oerr != nil {
			t.Fatalf("premise not established: %v", oerr)
		}
		if !errors.Is(qerr, context.Canceled) {
			t.Fatalf("interrupted SLEEP: err = %v, want context.Canceled", qerr)
		}
		_ = tx.Rollback()
		st := waitForPoolState(t, db, func(s sql.DBStats) bool { return s.OpenConnections == 0 }, "discarded")
		t.Logf("pool after mid-statement cancellation: open=%d (the closed connection was dropped)", st.OpenConnections)
		requireFenced(t, spikeRawInsert(db, "b/d2-raw"), "raw INSERT on a fresh session after a mid-statement cancellation")
		if role := spikeRoleVar(t, db); role.Valid {
			t.Fatalf("@bd_graph_role = %d on the fresh session, want NULL", role.Int64)
		}
	})

	t.Run("d3: *sql.Tx error path with a live context — the deferred clear runs before Rollback and the pooled session stays fenced", func(t *testing.T) {
		db := openCLILegPool(t, ep, beadGraphSpikeDatabase)
		// DoltStore.withWriteTx: fn error → errors.Join(err, tx.Rollback()).
		withWriteTx := func(fn func(tx *sql.Tx) error) error {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			if err := fn(tx); err != nil {
				return errors.Join(err, tx.Rollback())
			}
			return tx.Commit()
		}
		var txConn int64
		err := withWriteTx(func(tx *sql.Tx) (err error) {
			if _, err := tx.ExecContext(ctx, "SET @bd_graph_role = 1"); err != nil {
				return err
			}
			defer func() {
				if _, cerr := tx.ExecContext(ctx, "SET @bd_graph_role = NULL"); cerr != nil {
					err = errors.Join(err, cerr)
				}
			}()
			if _, err := tx.ExecContext(ctx, beadGraphSpikeInsert, "b/d3-inrole"); err != nil {
				return err
			}
			txConn = spikeConnectionID(t, tx)
			return errors.New("body failed after writing")
		})
		if err == nil {
			t.Fatal("withWriteTx err = nil, want the body failure")
		}
		if after := spikeConnectionID(t, db); after != txConn {
			t.Fatalf("follow-up statement ran on session %d, tx ran on %d", after, txConn)
		}
		requireFenced(t, spikeRawInsert(db, "b/d3-raw"), "raw INSERT on the pooled session after an error-path rollback with a deferred clear")
	})
}
