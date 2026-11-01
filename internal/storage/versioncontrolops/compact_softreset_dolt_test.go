package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
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
	csrWriterMsg   = "csr: writer lands t600"
	csrReplayMsg   = "csr: writer lands t650 mid-replay"
	csrConsumerMsg = "csr: concurrent consumer takes the staging"

	// csrSquashMsg is the base commit Compact writes for oldCommits=3.
	csrSquashMsg = "compact: squash 3 commits into base snapshot"

	// csrFollowUpMsg is the message of the commit Compact adds when the
	// soft reset leaves anything staged on main. It must stay in sync with
	// compact.go: the log assertions below pin it.
	csrFollowUpMsg = "compact: commit changes that reached main during compaction"
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
	// onCherryPick runs just before the first cherry-pick is forwarded.
	onCherryPick func(ctx context.Context) error
	// beforeFollowUp runs after Compact's staged check and just before the
	// follow-up commit is forwarded.
	beforeFollowUp func(ctx context.Context) error
	// failFollowUp, when set, makes the follow-up commit fail before it
	// executes.
	failFollowUp error
	// failStagedCheck makes the staged check's query fail against the real
	// server (see QueryRowContext).
	failStagedCheck bool

	mainResets int // final resets seen
	followUps  int // follow-up commit CALLs seen (forwarded or failed)
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
	if c.onCherryPick != nil && strings.Contains(query, "DOLT_CHERRY_PICK") {
		hook := c.onCherryPick
		c.onCherryPick = nil // first cherry-pick only
		if err := hook(ctx); err != nil {
			return nil, fmt.Errorf("csr hook before cherry-pick: %w", err)
		}
	}
	return c.DBConn.ExecContext(ctx, query, args...)
}

// QueryContext intercepts the drained follow-up commit. It is the only
// DOLT_COMMIT('-m', ...) (single -m) Compact issues — the squashed base
// commits with '-Am' — so match on that alone, not on the message text.
func (c *csrHookConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "DOLT_COMMIT('-m'") {
		c.followUps++
		if c.failFollowUp != nil {
			return nil, c.failFollowUp
		}
		if c.beforeFollowUp != nil {
			if err := c.beforeFollowUp(ctx); err != nil {
				return nil, fmt.Errorf("csr hook before follow-up commit: %w", err)
			}
		}
	}
	return c.DBConn.QueryContext(ctx, query, args...)
}

// QueryRowContext intercepts the staged check: issueops.HasStagedChanges
// reads through QueryRowContext, which on the wrapped *sql.Conn bypasses
// QueryContext above. The substituted query fails against the real server,
// so the error Compact reports is a truthful one, not a fabricated string.
func (c *csrHookConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if c.failStagedCheck && strings.Contains(query, "FROM dolt_status WHERE staged = 1") {
		return c.DBConn.QueryRowContext(ctx, "SELECT COUNT(*) FROM csr_missing_staged_probe")
	}
	return c.DBConn.QueryRowContext(ctx, query, args...)
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
// they are still pending. wantTAtHead is t's row count at HEAD (the history
// plus whatever the test's racing write got committed into HEAD).
func (e *csrEnv) assertPendingSurvive(wantTAtHead int) {
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
	if got := e.queryInt(verify, "SELECT COUNT(*) FROM t AS OF 'HEAD'"); got != wantTAtHead {
		e.t.Errorf("t at HEAD has %d rows, want %d", got, wantTAtHead)
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

		e.assertPendingSurvive(6)
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

		e.assertPendingSurvive(6)
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

// writerLandsCommit is the racing writer: on its own session it commits a t
// row to main and returns the hash of the commit it landed.
func (e *csrEnv) writerLandsCommit(id int, val, msg string) string {
	e.t.Helper()
	e.exec(e.writer, "INSERT INTO t (id, val) VALUES (?, ?)", id, val)
	e.exec(e.writer, "CALL DOLT_ADD('t')")
	e.exec(e.writer, "CALL DOLT_COMMIT('-m', ?)", msg)
	return e.queryString(e.writer, "SELECT commit_hash FROM dolt_log LIMIT 1")
}

// TestCompactKeepsCommitLandingBeforeMainMoves (AC-2): a commit another
// session lands on main after the rebuild, immediately before the final
// reset, must end up in main's HEAD through exactly one follow-up commit —
// folded, without its own hash or message — while unrelated working-only
// changes stay pending.
func TestCompactKeepsCommitLandingBeforeMainMoves(t *testing.T) {
	t.Run("row committed immediately before the reset", func(t *testing.T) {
		e := newCsrEnv(t)
		e.pendingWorkingSet()

		var landed string
		hooked := &csrHookConn{
			DBConn: e.compactConn(),
			onMainReset: func(ctx context.Context) error {
				landed = e.writerLandsCommit(600, "landed", csrWriterMsg)
				return nil
			},
		}
		if err := e.compact(hooked); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		if hooked.mainResets != 1 {
			t.Fatalf("Compact issued %d final resets, want 1", hooked.mainResets)
		}

		verify := e.verifyConn()
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM t WHERE id = 600"); got != 1 {
			t.Errorf("row 600 in t on main: present=%t, want present=true (the reset discarded a committed write)", got == 1)
		}
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); got != 1 {
			t.Errorf("row 600 in t at HEAD: %d, want 1 (a write that landed during compaction must reach HEAD through the follow-up commit)", got)
		}

		// History: the follow-up sits directly above the last replayed
		// commit, and the landed commit's own message is gone (folded).
		commits := e.log(verify)
		wantMsgs := []string{csrFollowUpMsg, csrRecent2Msg, csrRecent1Msg, csrSquashMsg}
		if len(commits) != len(wantMsgs)+1 {
			t.Fatalf("dolt_log has %d commits, want %d: %+v", len(commits), len(wantMsgs)+1, commits)
		}
		for i, want := range wantMsgs {
			if commits[i].message != want {
				t.Errorf("dolt_log[%d] = %q, want %q", i, commits[i].message, want)
			}
		}

		// Nothing else was staged, so HEAD's tables equal the tree the
		// writer landed.
		for _, table := range []string{"t", "u", "config"} {
			atHead := e.queryInt(verify, "SELECT COUNT(*) FROM `"+table+"` AS OF 'HEAD'")
			atLanded := e.queryInt(verify, "SELECT COUNT(*) FROM `"+table+"` AS OF '"+landed+"'")
			if atHead != atLanded {
				t.Errorf("%s at HEAD has %d rows, want the landed tree's %d", table, atHead, atLanded)
			}
		}

		// The unrelated working-only changes are still pending.
		e.assertPendingSurvive(7)
	})

	// Already-staged changes fold into the follow-up too (the design's
	// accepted boundary), while an unstaged edit to the same table stays
	// pending.
	t.Run("pre-staged row folds, unstaged edit in the same table stays pending", func(t *testing.T) {
		e := newCsrEnv(t)
		e.exec(e.setup, "INSERT INTO t (id, val) VALUES (750, 'pre-staged')")
		e.exec(e.setup, "CALL DOLT_ADD('t')")
		e.exec(e.setup, "INSERT INTO t (id, val) VALUES (800, 'unstaged-edit')")

		hooked := &csrHookConn{DBConn: e.compactConn()}
		if err := e.compact(hooked); err != nil {
			t.Fatalf("Compact: %v", err)
		}

		verify := e.verifyConn()
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 750"); got != 1 {
			t.Errorf("pre-staged row 750 at HEAD: %d, want 1 (already-staged changes fold into the follow-up)", got)
		}
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM t WHERE id = 800"); got != 1 {
			t.Errorf("unstaged row 800 on main: present=%t, want present=true", got == 1)
		}
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 800"); got != 0 {
			t.Errorf("unstaged row 800 at HEAD: %d, want 0 (unstaged edits stay pending)", got)
		}
		if staged := e.status(verify)["t"]; staged {
			t.Errorf("dolt_status[t] staged=%t, want unstaged (the follow-up committed the staged root)", staged)
		}
		if commits := e.log(verify); len(commits) == 0 || commits[0].message != csrFollowUpMsg {
			t.Errorf("dolt_log[0] = %+v, want the follow-up commit %q on top", commits, csrFollowUpMsg)
		}
	})

	// A second writer can take the staged set between Compact's check and
	// its follow-up commit; the nothing-to-commit that follows must be
	// tolerated, and the row still reaches HEAD through the consumer's
	// commit.
	t.Run("another writer consumes the staging between check and commit", func(t *testing.T) {
		e := newCsrEnv(t)
		e.pendingWorkingSet()

		hooked := &csrHookConn{
			DBConn: e.compactConn(),
			onMainReset: func(ctx context.Context) error {
				e.writerLandsCommit(600, "landed", csrWriterMsg)
				return nil
			},
			beforeFollowUp: func(ctx context.Context) error {
				e.exec(e.writer, "CALL DOLT_COMMIT('-m', ?)", csrConsumerMsg)
				return nil
			},
		}
		if err := e.compact(hooked); err != nil {
			t.Fatalf("Compact: %v (a nothing-to-commit follow-up must be tolerated)", err)
		}
		if hooked.followUps != 1 {
			t.Fatalf("Compact issued %d follow-up commits, want 1", hooked.followUps)
		}

		verify := e.verifyConn()
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); got != 1 {
			t.Errorf("row 600 in t at HEAD: %d, want 1 (the consumer's commit keeps it)", got)
		}
		if commits := e.log(verify); len(commits) == 0 || commits[0].message != csrConsumerMsg {
			t.Errorf("dolt_log[0] = %+v, want the consumer's %q on top", commits, csrConsumerMsg)
		}
		for _, c := range e.log(verify) {
			if c.message == csrFollowUpMsg {
				t.Errorf("dolt_log contains the follow-up %q; want none (nothing was left to commit)", c.message)
			}
		}
		e.assertPendingSurvive(7)
	})

	// The same preservation for a commit that lands while Compact is still
	// replaying recent commits onto the temp branch.
	t.Run("commit injected during the replay survives, folded", func(t *testing.T) {
		e := newCsrEnv(t)
		e.pendingWorkingSet()

		hooked := &csrHookConn{
			DBConn: e.compactConn(),
			onCherryPick: func(ctx context.Context) error {
				e.writerLandsCommit(650, "mid-replay", csrReplayMsg)
				return nil
			},
		}
		if err := e.compact(hooked); err != nil {
			t.Fatalf("Compact: %v", err)
		}

		verify := e.verifyConn()
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 650"); got != 1 {
			t.Errorf("row 650 in t at HEAD: %d, want 1 (a commit landing mid-replay is folded into the follow-up)", got)
		}
		commits := e.log(verify)
		if len(commits) == 0 || commits[0].message != csrFollowUpMsg {
			t.Errorf("dolt_log[0] = %+v, want the follow-up commit %q on top", commits, csrFollowUpMsg)
		}
		for _, c := range commits {
			if c.message == csrReplayMsg {
				t.Errorf("dolt_log keeps the landed commit's own message %q; want it folded away (identity loss is the accepted trade-off)", c.message)
			}
		}
		e.assertPendingSurvive(7)
	})
}

// assertQuietHistory pins AC-3's history: the initial commit, the squashed
// base and the replayed recent commits, and nothing else — in particular no
// follow-up commit.
func (e *csrEnv) assertQuietHistory() {
	e.t.Helper()
	verify := e.verifyConn()
	commits := e.log(verify)
	wantMsgs := []string{csrRecent2Msg, csrRecent1Msg, csrSquashMsg}
	if len(commits) != len(wantMsgs)+1 {
		e.t.Fatalf("dolt_log has %d commits, want %d: %+v", len(commits), len(wantMsgs)+1, commits)
	}
	for i, want := range wantMsgs {
		if commits[i].message != want {
			e.t.Errorf("dolt_log[%d] = %q, want %q", i, commits[i].message, want)
		}
	}
	for _, c := range commits {
		if c.message == csrFollowUpMsg {
			e.t.Errorf("dolt_log contains the follow-up commit %q; want none on a quiet run", c.message)
		}
	}

	// The pre-compaction HEAD tree is preserved: every committed table has
	// exactly the rows it had at the head Compact started from.
	for _, table := range []string{"t", "u", "config"} {
		atHead := e.queryInt(verify, "SELECT COUNT(*) FROM `"+table+"` AS OF 'HEAD'")
		before := e.queryInt(verify, "SELECT COUNT(*) FROM `"+table+"` AS OF '"+e.preCompactHead+"'")
		if atHead != before {
			e.t.Errorf("%s at HEAD has %d rows, want the pre-compaction %d", table, atHead, before)
		}
	}
	if st := e.status(verify); len(st) != 0 {
		e.t.Errorf("dolt_status = %v, want empty", st)
	}
}

// TestCompactQuietRunAddsNoCommit (AC-3): on a clean working set with no
// concurrent writer, a successful compaction preserves the pre-compaction
// HEAD tree, leaves exactly the initial commit, the squashed base and the
// replayed recent commits — no follow-up commit — and an empty dolt_status.
// This is the regression guard for the soft reset's quiet path: the
// follow-up commit exists only when the reset actually left something
// staged.
func TestCompactQuietRunAddsNoCommit(t *testing.T) {
	t.Run("clean working set, no concurrent writer", func(t *testing.T) {
		e := newCsrEnv(t)

		hooked := &csrHookConn{DBConn: e.compactConn()}
		if err := e.compact(hooked); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		if hooked.followUps != 0 {
			t.Fatalf("Compact issued %d follow-up commits, want 0 on a quiet run", hooked.followUps)
		}

		e.assertQuietHistory()
	})

	// Dirt that dolt_ignore hides cannot be staged, so it must not earn a
	// follow-up commit either; the row itself stays where it was.
	t.Run("ignored table dirt only", func(t *testing.T) {
		e := newCsrEnv(t)
		e.exec(e.setup, "INSERT INTO ignoredprobe VALUES (1)")

		hooked := &csrHookConn{DBConn: e.compactConn()}
		if err := e.compact(hooked); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		if hooked.followUps != 0 {
			t.Fatalf("Compact issued %d follow-up commits, want 0 when only a dolt_ignore'd table is dirty", hooked.followUps)
		}

		e.assertQuietHistory()
		if got := e.queryInt(e.verifyConn(), "SELECT COUNT(*) FROM ignoredprobe WHERE id = 1"); got != 1 {
			e.t.Errorf("row 1 in ignoredprobe on main: present=%t, want present=true", got == 1)
		}
	})
}

// TestCompactFollowupFailureKeepsChangesStaged (AC-4): when a commit lands
// immediately before the reset and the follow-up commit then fails before
// executing, Compact reports the failed step and the completed head move,
// keeps the row staged on main, and the best-effort cleanup still removes
// compact-tmp.
func TestCompactFollowupFailureKeepsChangesStaged(t *testing.T) {
	t.Run("follow-up commit fails before executing", func(t *testing.T) {
		e := newCsrEnv(t)
		injected := errors.New("csr: injected follow-up failure")
		hooked := &csrHookConn{
			DBConn:       e.compactConn(),
			failFollowUp: injected,
			onMainReset: func(ctx context.Context) error {
				e.writerLandsCommit(600, "landed", csrWriterMsg)
				return nil
			},
		}
		err := e.compact(hooked)
		if err == nil {
			t.Fatal("Compact = nil error, want the follow-up failure reported")
		}
		if hooked.followUps != 1 {
			t.Fatalf("Compact attempted %d follow-up commits, want 1", hooked.followUps)
		}
		for _, want := range []string{
			"commit changes that reached main during compaction", // the failed step
			"main was already reset to the compacted history",    // the completed head move
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Compact error %q does not report %q", err.Error(), want)
			}
		}
		if !errors.Is(err, injected) {
			t.Errorf("errors.Is(Compact error, injected) = false (%v)", err)
		}

		verify := e.verifyConn()
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM t WHERE id = 600"); got != 1 {
			t.Errorf("row 600 in t on main: present=%t, want present=true", got == 1)
		}
		if staged := e.status(verify)["t"]; !staged {
			t.Errorf("dolt_status[t] staged=%t, want staged (the row must survive for its own commit)", staged)
		}
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); got != 0 {
			t.Errorf("row 600 in t at HEAD: %d, want 0 (the follow-up never ran)", got)
		}
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM dolt_branches WHERE name = 'compact-tmp'"); got != 0 {
			t.Errorf("compact-tmp branches left: %d, want 0 (best-effort cleanup must remove it)", got)
		}
	})

	// The staged check itself failing must not attempt the commit — staging
	// was never established — and must report the same completed head move.
	t.Run("staged check fails truthfully", func(t *testing.T) {
		e := newCsrEnv(t)
		hooked := &csrHookConn{
			DBConn:          e.compactConn(),
			failStagedCheck: true,
			onMainReset: func(ctx context.Context) error {
				e.writerLandsCommit(600, "landed", csrWriterMsg)
				return nil
			},
		}
		err := e.compact(hooked)
		if err == nil {
			t.Fatal("Compact = nil error, want the staged-check failure reported")
		}
		if hooked.followUps != 0 {
			t.Fatalf("Compact attempted %d follow-up commits after the check failed, want 0", hooked.followUps)
		}
		for _, want := range []string{
			"check staged changes",                            // the failed step
			"main was already reset to the compacted history", // the completed head move
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Compact error %q does not report %q", err.Error(), want)
			}
		}

		verify := e.verifyConn()
		if staged := e.status(verify)["t"]; !staged {
			t.Errorf("dolt_status[t] staged=%t, want staged (the landed row must survive the failed check)", staged)
		}
		if got := e.queryInt(verify, "SELECT COUNT(*) FROM dolt_branches WHERE name = 'compact-tmp'"); got != 0 {
			t.Errorf("compact-tmp branches left: %d, want 0 (best-effort cleanup must remove it)", got)
		}
	})
}
