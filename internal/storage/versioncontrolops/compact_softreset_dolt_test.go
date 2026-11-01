package versioncontrolops

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil"
)

// This file pins the contract of Compact's final reset onto main (GH#7789):
// a write that reaches main while a compaction runs — a row main holds in
// its working set without a Dolt commit yet, or a commit another session
// lands in the window between the finished rebuild and the reset — must
// survive it. DOLT_RESET('--hard', 'compact-tmp') replaced main's working
// and staged roots with the rebuilt tree and silently destroyed both, after
// the writer had been told it succeeded. The soft reset keeps those roots:
// pending (unstaged) rows stay pending and out of HEAD, and whatever the
// reset leaves staged is committed by one follow-up commit, so main is
// never left dirty. A commit that lands in the window is folded into that
// follow-up: its rows survive, its own hash and message do not (compaction
// rewrites ancestry; the accepted trade-off of the approved design).
//
// Every test here runs against its own real Dolt server started by
// testutil.StartIsolatedDoltContainerHandle, drives Compact over a pinned
// session (a DBConn, as every caller does) beside a second pinned session
// playing the concurrent writer, and injects the racing write synchronously
// inside a connection hook — no sleeps, no goroutine timing.

// csrMsg* name the commits of the history every test in this file builds:
// init (dolt's own) <- schema/seed <- old t4 <- boundary t5 <- recent u11
// <- recent cfg+t6 (head). Compact squashes the first three into one base
// and replays the two recent commits.
const (
	csrSchemaMsg   = "csr: schema and seed"
	csrOldMsg      = "csr: old t4"
	csrBoundaryMsg = "csr: boundary t5"
	csrRecent1Msg  = "csr: recent u11"
	csrRecent2Msg  = "csr: recent cfg and t6"

	// csrSquashMsg is the base commit Compact writes for oldCommits=3.
	csrSquashMsg = "compact: squash 3 commits into base snapshot"
)

// csrEnv is a fresh database on its own Dolt server carrying that history,
// with pinned sessions: setup built the history (and holds main's
// uncommitted working-set writes, the stand-in for bd's batch window),
// writer plays a second bd writing to the same store, and Compact runs on
// its own hooked session.
type csrEnv struct {
	t   *testing.T
	ctx context.Context
	db  *sql.DB

	setup  *sql.Conn
	writer *sql.Conn

	initial        string   // dolt's Initialize data repository commit
	boundary       string   // csrBoundaryMsg
	recent         []string // oldest first, the order Compact replays them
	preCompactHead string   // csrRecent2Msg
}

func newCsrEnv(t *testing.T) *csrEnv {
	t.Helper()

	ctr := testutil.StartIsolatedDoltContainerHandle(t)
	port, err := strconv.Atoi(ctr.Port)
	if err != nil {
		t.Fatalf("parse isolated Dolt port %q: %v", ctr.Port, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	root := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Timeout: 10 * time.Second}
	admin, err := sql.Open("mysql", root.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	dbName := "csr_" + regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(t.Name()), "_")
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	if err := admin.Close(); err != nil {
		t.Fatalf("close admin connection: %v", err)
	}

	withDB := root
	withDB.Database = dbName
	db, err := sql.Open("mysql", withDB.String())
	if err != nil {
		t.Fatalf("open database connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	e := &csrEnv{t: t, ctx: ctx, db: db}
	e.setup = e.conn("setup")
	e.writer = e.conn("writer")
	e.buildHistory()
	return e
}

// conn pins a new session from the pool; every statement these tests issue
// that touches branch or working-set state runs on one.
func (e *csrEnv) conn(name string) *sql.Conn {
	e.t.Helper()
	conn, err := e.db.Conn(e.ctx)
	if err != nil {
		e.t.Fatalf("pin %s session: %v", name, err)
	}
	e.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// compactConn is Compact's pinned session; tests wrap it in a csrHookConn.
func (e *csrEnv) compactConn() *sql.Conn {
	e.t.Helper()
	return e.conn("compact")
}

// verifyConn is a fresh session reading main's published state after
// Compact returned.
func (e *csrEnv) verifyConn() *sql.Conn {
	e.t.Helper()
	return e.conn("verify")
}

func (e *csrEnv) exec(conn DBConn, query string, args ...any) {
	e.t.Helper()
	if _, err := conn.ExecContext(e.ctx, query, args...); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

func (e *csrEnv) queryInt(conn DBConn, query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := conn.QueryRowContext(e.ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("query %q: %v", query, err)
	}
	return n
}

func (e *csrEnv) queryString(conn DBConn, query string, args ...any) string {
	e.t.Helper()
	var s string
	if err := conn.QueryRowContext(e.ctx, query, args...).Scan(&s); err != nil {
		e.t.Fatalf("query %q: %v", query, err)
	}
	return s
}

// csrCommit is one dolt_log row.
type csrCommit struct {
	hash    string
	message string
}

// log returns main's history newest-first.
func (e *csrEnv) log(conn DBConn) []csrCommit {
	e.t.Helper()
	rows, err := conn.QueryContext(e.ctx, "SELECT commit_hash, message FROM dolt_log")
	if err != nil {
		e.t.Fatalf("read dolt_log: %v", err)
	}
	defer rows.Close()
	var commits []csrCommit
	for rows.Next() {
		var c csrCommit
		if err := rows.Scan(&c.hash, &c.message); err != nil {
			e.t.Fatalf("scan dolt_log: %v", err)
		}
		commits = append(commits, c)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatalf("iterate dolt_log: %v", err)
	}
	return commits
}

// status maps table name -> staged, exactly as dolt_status reports main.
func (e *csrEnv) status(conn DBConn) map[string]bool {
	e.t.Helper()
	rows, err := conn.QueryContext(e.ctx, "SELECT table_name, staged FROM dolt_status")
	if err != nil {
		e.t.Fatalf("read dolt_status: %v", err)
	}
	defer rows.Close()
	st := map[string]bool{}
	for rows.Next() {
		var table string
		var staged bool
		if err := rows.Scan(&table, &staged); err != nil {
			e.t.Fatalf("scan dolt_status: %v", err)
		}
		st[table] = staged
	}
	if err := rows.Err(); err != nil {
		e.t.Fatalf("iterate dolt_status: %v", err)
	}
	return st
}

func (e *csrEnv) buildHistory() {
	e.t.Helper()
	for _, q := range []string{
		"CREATE TABLE t (id INT PRIMARY KEY, val VARCHAR(64))",
		"CREATE TABLE u (id INT PRIMARY KEY, val VARCHAR(64))",
		"CREATE TABLE config (k VARCHAR(32) PRIMARY KEY, v VARCHAR(64))",
		"CREATE TABLE ignoredprobe (id INT PRIMARY KEY)",
		"INSERT INTO dolt_ignore VALUES ('ignoredprobe', true)",
		"INSERT INTO t VALUES (1,'one'),(2,'two'),(3,'three')",
		"INSERT INTO u VALUES (10,'ten')",
		"INSERT INTO config VALUES ('ck','cv0')",
		"CALL DOLT_COMMIT('-Am', '" + csrSchemaMsg + "')",
		"INSERT INTO t VALUES (4,'four')",
		"CALL DOLT_COMMIT('-Am', '" + csrOldMsg + "')",
		"INSERT INTO t VALUES (5,'five')",
		"CALL DOLT_COMMIT('-Am', '" + csrBoundaryMsg + "')",
		"INSERT INTO u VALUES (11,'eleven')",
		"CALL DOLT_COMMIT('-Am', '" + csrRecent1Msg + "')",
		"UPDATE config SET v = 'cv1' WHERE k = 'ck'",
		"INSERT INTO t VALUES (6,'six')",
		"CALL DOLT_COMMIT('-Am', '" + csrRecent2Msg + "')",
	} {
		e.exec(e.setup, q)
	}

	commits := e.log(e.setup)
	if len(commits) != 6 {
		e.t.Fatalf("dolt_log has %d commits, want 6 (dolt init + 5): %+v", len(commits), commits)
	}
	byMsg := map[string]string{}
	for _, c := range commits {
		byMsg[c.message] = c.hash
	}
	e.initial = commits[len(commits)-1].hash
	e.boundary = byMsg[csrBoundaryMsg]
	e.recent = []string{byMsg[csrRecent1Msg], byMsg[csrRecent2Msg]}
	e.preCompactHead = byMsg[csrRecent2Msg]
	if e.initial == "" || e.boundary == "" || e.recent[0] == "" || e.recent[1] == "" {
		e.t.Fatalf("history incomplete: initial=%q boundary=%q recent=%v", e.initial, e.boundary, e.recent)
	}
}

// compact runs Compact on a hooked pinned session, the way callers do.
func (e *csrEnv) compact(hooked *csrHookConn) error {
	e.t.Helper()
	return Compact(e.ctx, hooked, e.initial, e.boundary, 3, e.recent)
}

// csrHookConn wraps Compact's pinned session and is the injection point for
// the racing writer: each hook runs synchronously while Compact sits between
// two statements, standing in for a second bd process writing to main.
type csrHookConn struct {
	DBConn
	// onMainReset runs just before the final DOLT_RESET(..., 'compact-tmp')
	// is forwarded: the window between the finished rebuild and the reset.
	onMainReset func(ctx context.Context) error

	mainResets int // final resets seen
}

func (c *csrHookConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "DOLT_RESET") && strings.Contains(query, "compact-tmp") {
		c.mainResets++
		if c.onMainReset != nil {
			if err := c.onMainReset(ctx); err != nil {
				return nil, fmt.Errorf("csr hook before main reset: %w", err)
			}
		}
	}
	return c.DBConn.ExecContext(ctx, query, args...)
}

// pendingWorkingSet leaves in main's working set, uncommitted, what bd
// leaves between its SQL commit and its DOLT_COMMIT (or a whole batch under
// batched auto-commit): tracked-table rows in u, a config edit, and a row
// in a dolt_ignore'd table.
func (e *csrEnv) pendingWorkingSet() {
	e.t.Helper()
	e.exec(e.setup, "INSERT INTO u (id, val) VALUES (700, 'pending')")
	e.exec(e.setup, "UPDATE config SET v = 'pending-cfg' WHERE k = 'ck'")
	e.exec(e.setup, "INSERT INTO ignoredprobe VALUES (1)")
}

// assertPendingSurvive is AC-1's contract after a successful Compact: the
// values are still on main, they never entered main's HEAD snapshot, and
// they are still pending.
func (e *csrEnv) assertPendingSurvive() {
	e.t.Helper()
	verify := e.verifyConn()

	if got := e.queryInt(verify, "SELECT COUNT(*) FROM u WHERE id = 700"); got != 1 {
		e.t.Errorf("row 700 in u on main: present=%t, want present=true (compaction discarded an uncommitted write)", got == 1)
	}
	if got := e.queryString(verify, "SELECT v FROM config WHERE k = 'ck'"); got != "pending-cfg" {
		e.t.Errorf("config on main = %q, want the pending %q (compaction discarded an uncommitted config edit)", got, "pending-cfg")
	}
	if got := e.queryInt(verify, "SELECT COUNT(*) FROM ignoredprobe WHERE id = 1"); got != 1 {
		e.t.Errorf("row 1 in dolt_ignore'd ignoredprobe on main: present=%t, want present=true", got == 1)
	}

	if got := e.queryInt(verify, "SELECT COUNT(*) FROM u AS OF 'HEAD' WHERE id = 700"); got != 0 {
		e.t.Errorf("row 700 in u at HEAD: %d, want 0 (an uncommitted write must not enter HEAD)", got)
	}
	if got := e.queryString(verify, "SELECT v FROM config AS OF 'HEAD' WHERE k = 'ck'"); got != "cv1" {
		e.t.Errorf("config at HEAD = %q, want the committed %q", got, "cv1")
	}
	// Sanity on the compaction itself: the committed data is all still in HEAD.
	if got := e.queryInt(verify, "SELECT COUNT(*) FROM t AS OF 'HEAD'"); got != 6 {
		e.t.Errorf("t at HEAD has %d rows, want 6", got)
	}

	st := e.status(verify)
	if staged, ok := st["u"]; !ok || staged {
		e.t.Errorf("dolt_status[u] = staged:%t present:%t, want present and unstaged", staged, ok)
	}
	if staged, ok := st["config"]; !ok || staged {
		e.t.Errorf("dolt_status[config] = staged:%t present:%t, want present and unstaged", staged, ok)
	}
	if _, ok := st["ignoredprobe"]; ok {
		e.t.Errorf("dolt_status lists dolt_ignore'd ignoredprobe; want it hidden")
	}
}

// TestCompactKeepsUncommittedChanges (AC-1): rows main holds in its working
// set without a Dolt commit must survive a compaction that succeeds without
// another writer staging them — their values remain on main, they stay out
// of main's HEAD snapshot, and they remain pending.
func TestCompactKeepsUncommittedChanges(t *testing.T) {
	t.Run("pending across the whole compaction", func(t *testing.T) {
		e := newCsrEnv(t)
		e.pendingWorkingSet()

		hooked := &csrHookConn{DBConn: e.compactConn()}
		if err := e.compact(hooked); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		if hooked.mainResets != 1 {
			t.Fatalf("Compact issued %d final resets, want 1", hooked.mainResets)
		}

		e.assertPendingSurvive()
	})

	// The same shape, but the write publishes in the microseconds between
	// the finished rebuild and the final reset.
	t.Run("write lands in the window before the reset", func(t *testing.T) {
		e := newCsrEnv(t)
		e.pendingWorkingSet()

		hooked := &csrHookConn{
			DBConn: e.compactConn(),
			onMainReset: func(ctx context.Context) error {
				e.exec(e.writer, "INSERT INTO u (id, val) VALUES (800, 'landed-unstaged')")
				return nil
			},
		}
		if err := e.compact(hooked); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		if hooked.mainResets != 1 {
			t.Fatalf("Compact issued %d final resets, want 1", hooked.mainResets)
		}

		e.assertPendingSurvive()
		verify := e.verifyConn()
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM u WHERE id = 800"); got != 1 {
			e.t.Errorf("row 800 in u on main: present=%t, want present=true (the reset discarded a write that landed just before it)", got == 1)
		}
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM u AS OF 'HEAD' WHERE id = 800"); got != 0 {
			e.t.Errorf("row 800 in u at HEAD: %d, want 0", got)
		}
		if staged := e.status(verify)["u"]; staged {
			e.t.Errorf("dolt_status[u] staged=%t, want unstaged", staged)
		}
	})
}
