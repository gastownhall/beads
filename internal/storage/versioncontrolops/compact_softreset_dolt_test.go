package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/testutil"
)

// These tests run Compact against a real dolt sql-server. They pin what the
// final step does to main's working set: a write that is in main's working set
// (not yet in a Dolt commit), or a commit that reaches main after the history
// was rebuilt, must still be on main when Compact returns. The old final step,
// DOLT_RESET('--hard', 'compact-tmp'), discarded both.

// compactHookConn is the DBConn Compact runs on. It forwards every statement
// to its pinned connection, after letting a test act at a chosen point, or
// fails a chosen statement without forwarding it.
type compactHookConn struct {
	*sql.Conn

	// beforeExec runs before each ExecContext statement is forwarded.
	beforeExec func(query string)
	// beforeFollowUp runs after the staged check, just before the follow-up
	// commit is forwarded.
	beforeFollowUp func()
	// failFollowupCommit, when set, is returned for a DOLT_COMMIT issued
	// through QueryContext after the final reset, without forwarding it.
	failFollowupCommit error
	// failStagedCheck makes the post-reset dolt_status read fail.
	failStagedCheck bool

	resetDone atomic.Bool
	followUps int // follow-up commit CALLs seen (forwarded or failed)
}

// compactFollowupMsg is the message of the commit Compact adds when the soft
// reset leaves anything staged on main.
const compactFollowupMsg = "compact: commit changes that reached main during compaction"

func isFinalReset(query string) bool {
	return strings.Contains(query, "DOLT_RESET(") && strings.Contains(query, "compact-tmp")
}

func (c *compactHookConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if c.beforeExec != nil {
		c.beforeExec(query)
	}
	if isFinalReset(query) {
		c.resetDone.Store(true)
	}
	return c.Conn.ExecContext(ctx, query, args...)
}

func (c *compactHookConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if c.resetDone.Load() && strings.Contains(query, "DOLT_COMMIT") {
		c.followUps++
		if c.failFollowupCommit != nil {
			return nil, c.failFollowupCommit
		}
		if c.beforeFollowUp != nil {
			c.beforeFollowUp()
		}
	}
	return c.Conn.QueryContext(ctx, query, args...)
}

func (c *compactHookConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if c.resetDone.Load() && c.failStagedCheck && strings.Contains(query, "dolt_status") {
		return c.Conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM compact_injected_missing_table")
	}
	return c.Conn.QueryRowContext(ctx, query, args...)
}

// onceBeforeFinalReset returns a beforeExec hook that runs fn once, just
// before the final reset of main onto compact-tmp is forwarded.
func onceBeforeFinalReset(fn func()) func(string) {
	var done bool
	return func(query string) {
		if !done && isFinalReset(query) {
			done = true
			fn()
		}
	}
}

// compactFixture is one database on a test server with a five-commit history:
// the init commit and "schema", "t 1" (the old commits, ending at boundary),
// then "t 2" and "u 1" (the recent commits Compact replays).
type compactFixture struct {
	t        *testing.T
	ctx      context.Context
	db       *sql.DB
	initial  string
	boundary string
	recent   []string // oldest first, as Compact replays them
	head     string   // main's HEAD before Compact
}

// replayedLog is main's history after a compaction that added no commit,
// newest first.
var replayedLog = []string{"u 1", "t 2", "compact: squash 2 commits into base snapshot", "Initialize data repository"}

func startCompactTestServer(t *testing.T) string {
	t.Helper()
	return testutil.StartIsolatedDoltContainerHandle(t).Port
}

func newCompactFixture(t *testing.T, port string) *compactFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	name := fmt.Sprintf("compact_%d", time.Now().UnixNano())
	root, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/", port))
	if err != nil {
		t.Fatalf("open server: %v", err)
	}
	defer root.Close()
	if _, err := root.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	// parseTime, as every bd DSN sets it (doltutil.ServerDSN,
	// dbproxy/util.DoltServerDSN): Compact reads dolt_log's dates as
	// time.Time.
	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/%s?parseTime=true", port, name))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := &compactFixture{t: t, ctx: ctx, db: db}
	f.exec(db,
		"CREATE TABLE t (id INT PRIMARY KEY, v VARCHAR(32))",
		"CREATE TABLE u (id INT PRIMARY KEY, v VARCHAR(32))",
		"CREATE TABLE config (k VARCHAR(32) PRIMARY KEY, v VARCHAR(32))",
		"INSERT INTO config VALUES ('mode', 'base')",
		"INSERT INTO dolt_ignore VALUES ('scratch_%', true)",
		"CALL DOLT_COMMIT('-Am', 'schema')",
		"INSERT INTO t VALUES (1, 'old')",
		"CALL DOLT_COMMIT('-Am', 't 1')",
		"INSERT INTO t VALUES (2, 'recent')",
		"CALL DOLT_COMMIT('-Am', 't 2')",
		"INSERT INTO u VALUES (1, 'recent')",
		"CALL DOLT_COMMIT('-Am', 'u 1')",
	)

	hashes, messages := f.log()
	if got := strings.Join(messages, " | "); got != "u 1 | t 2 | t 1 | schema | Initialize data repository" {
		t.Fatalf("fixture history = %s", got)
	}
	f.initial = hashes[4]
	f.boundary = hashes[2]
	f.recent = []string{hashes[1], hashes[0]}
	f.head = hashes[0]
	return f
}

func (f *compactFixture) exec(conn DBConn, queries ...string) {
	f.t.Helper()
	for _, q := range queries {
		if _, err := conn.ExecContext(f.ctx, q); err != nil {
			f.t.Fatalf("%s: %v", q, err)
		}
	}
}

// conn pins a session of its own, as a second writer or as Compact's
// connection.
func (f *compactFixture) conn() *sql.Conn {
	f.t.Helper()
	c, err := f.db.Conn(f.ctx)
	if err != nil {
		f.t.Fatalf("pin connection: %v", err)
	}
	f.t.Cleanup(func() { _ = c.Close() })
	return c
}

// compact runs Compact on hook (whose Conn it pins if unset) with the plan
// read when the fixture was built: main's HEAD then is the planned head.
func (f *compactFixture) compact(hook *compactHookConn) error {
	f.t.Helper()
	if hook.Conn == nil {
		hook.Conn = f.conn()
	}
	return Compact(f.ctx, hook, f.initial, f.boundary, f.head, 2, f.recent)
}

// log returns main's commits, newest first.
func (f *compactFixture) log() (hashes, messages []string) {
	f.t.Helper()
	rows, err := f.db.QueryContext(f.ctx, "SELECT commit_hash, message FROM dolt_log")
	if err != nil {
		f.t.Fatalf("read dolt_log: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h, m string
		if err := rows.Scan(&h, &m); err != nil {
			f.t.Fatalf("scan dolt_log: %v", err)
		}
		hashes = append(hashes, h)
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatalf("read dolt_log: %v", err)
	}
	return hashes, messages
}

func (f *compactFixture) wantLog(want []string) {
	f.t.Helper()
	_, got := f.log()
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		f.t.Fatalf("main's history after Compact:\n got  %q\n want %q", got, want)
	}
}

// count runs a COUNT(*) query on the shared pool (main's working set).
func (f *compactFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRowContext(f.ctx, query, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (f *compactFixture) value(query string) string {
	f.t.Helper()
	var v string
	if err := f.db.QueryRowContext(f.ctx, query).Scan(&v); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	return v
}

// status returns dolt_status as "table:staged" entries, sorted.
func (f *compactFixture) status() []string {
	f.t.Helper()
	rows, err := f.db.QueryContext(f.ctx,
		"SELECT table_name, staged FROM dolt_status WHERE table_name NOT LIKE 'scratch_%' ORDER BY table_name, staged")
	if err != nil {
		f.t.Fatalf("read dolt_status: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		var staged bool
		if err := rows.Scan(&name, &staged); err != nil {
			f.t.Fatalf("scan dolt_status: %v", err)
		}
		out = append(out, fmt.Sprintf("%s:%t", name, staged))
	}
	if err := rows.Err(); err != nil {
		f.t.Fatalf("read dolt_status: %v", err)
	}
	return out
}

// wantFollowupLog checks main's history is the replayed history with exactly
// one follow-up commit on top.
func (f *compactFixture) wantFollowupLog() {
	f.t.Helper()
	f.wantLog(append([]string{compactFollowupMsg}, replayedLog...))
}

func (f *compactFixture) wantStatus(want ...string) {
	f.t.Helper()
	if got := f.status(); strings.Join(got, ",") != strings.Join(want, ",") {
		f.t.Fatalf("dolt_status after Compact = %q, want %q", got, want)
	}
}

// sameTree reports whether main's HEAD has no table differences from rev.
func (f *compactFixture) wantHeadTree(rev string) {
	f.t.Helper()
	if n := f.count("SELECT COUNT(*) FROM dolt_diff_summary(?, 'HEAD')", rev); n != 0 {
		f.t.Fatalf("main's HEAD differs from %s in %d tables, want the same tree", rev, n)
	}
}

func (f *compactFixture) wantNoTempBranch() {
	f.t.Helper()
	if n := f.count("SELECT COUNT(*) FROM dolt_branches WHERE name = 'compact-tmp'"); n != 0 {
		f.t.Fatalf("compact-tmp still exists after Compact")
	}
}

// landRow commits t row 600 to main on writer, staging only t, as another bd
// process would, and returns main's new HEAD.
func (f *compactFixture) landRow(writer *sql.Conn) string {
	f.t.Helper()
	f.exec(writer,
		"INSERT INTO t VALUES (600, 'landed')",
		"CALL DOLT_ADD('t')",
		"CALL DOLT_COMMIT('-m', 'landed 600')",
	)
	var h string
	if err := writer.QueryRowContext(f.ctx, "SELECT HASHOF('HEAD')").Scan(&h); err != nil {
		f.t.Fatalf("read landed head: %v", err)
	}
	return h
}

// writePending leaves SQL-committed changes in main's working set that no
// Dolt commit holds: a u row, a config change and an ignored-table row.
func (f *compactFixture) writePending(conn DBConn) {
	f.t.Helper()
	f.exec(conn,
		"INSERT INTO u VALUES (700, 'pending')",
		"UPDATE config SET v = 'pending' WHERE k = 'mode'",
		"CREATE TABLE IF NOT EXISTS scratch_x (id INT PRIMARY KEY)",
		"INSERT INTO scratch_x VALUES (1)",
	)
}

// wantPendingKept checks writePending's changes are still in main's working
// set, absent from HEAD, and unstaged.
func (f *compactFixture) wantPendingKept() {
	f.t.Helper()
	if n := f.count("SELECT COUNT(*) FROM u WHERE id = 700"); n != 1 {
		f.t.Fatalf("pending u row 700 on main: present=false, want present=true")
	}
	if n := f.count("SELECT COUNT(*) FROM u AS OF 'HEAD' WHERE id = 700"); n != 0 {
		f.t.Fatalf("pending u row 700 is in main's HEAD, want it left uncommitted")
	}
	if v := f.value("SELECT v FROM config WHERE k = 'mode'"); v != "pending" {
		f.t.Fatalf("config mode on main = %q, want the pending %q", v, "pending")
	}
	if v := f.value("SELECT v FROM config AS OF 'HEAD' WHERE k = 'mode'"); v != "base" {
		f.t.Fatalf("config mode in main's HEAD = %q, want %q", v, "base")
	}
	if n := f.count("SELECT COUNT(*) FROM scratch_x"); n != 1 {
		f.t.Fatalf("ignored scratch_x row on main: present=false, want present=true")
	}
}

// AC-1: SQL-committed but unstaged changes in main's working set survive
// Compact, stay out of HEAD and stay pending.
func TestCompactKeepsUncommittedChanges(t *testing.T) {
	port := startCompactTestServer(t)

	t.Run("written before compaction", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)

		if err := f.compact(&compactHookConn{}); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		f.wantPendingKept()
		f.wantLog(replayedLog)
		f.wantHeadTree(f.head)
		f.wantStatus("config:false", "u:false")
	})

	t.Run("written just before the final reset", func(t *testing.T) {
		f := newCompactFixture(t, port)
		writer := f.conn()

		hook := &compactHookConn{beforeExec: onceBeforeFinalReset(func() { f.writePending(writer) })}
		if err := f.compact(hook); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		f.wantPendingKept()
		f.wantLog(replayedLog)
		f.wantHeadTree(f.head)
		f.wantStatus("config:false", "u:false")
	})
}

// AC-2: a commit that reaches main after the plan was read, whether before
// Compact starts, during the replay or just before the final reset, is in
// main's HEAD, while unrelated working-only changes stay pending: one that
// landed before the catch-up read main's head is replayed as its own commit,
// one that landed after it through one follow-up commit. The replay check
// compares the replay with the planned head, so such a commit is kept, not
// refused as a divergent replay.
func TestCompactKeepsCommitLandingBeforeMainMoves(t *testing.T) {
	port := startCompactTestServer(t)

	// Lands between the replay check and the final reset: the check has
	// already passed, so only the soft reset keeps this commit.
	t.Run("landing commit", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		writer := f.conn()

		var landed string
		hook := &compactHookConn{beforeExec: onceBeforeFinalReset(func() { landed = f.landRow(writer) })}
		if err := f.compact(hook); err != nil {
			t.Fatalf("Compact: %v", err)
		}

		if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); n != 1 {
			t.Fatalf("landed t row 600 in main's HEAD: present=false, want present=true")
		}
		f.wantFollowupLog()
		f.wantHeadTree(landed)
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoTempBranch()
	})

	// Lands before the replay check, so main's HEAD differs from the replay
	// when the check runs; the catch-up replays it.
	t.Run("landing during replay", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		writer := f.conn()

		var done bool
		var landed string
		hook := &compactHookConn{beforeExec: func(query string) {
			if !done && strings.Contains(query, "DOLT_CHERRY_PICK") {
				done = true
				landed = f.landRow(writer)
			}
		}}
		if err := f.compact(hook); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); n != 1 {
			t.Fatalf("landed t row 600 in main's HEAD: present=false, want present=true")
		}
		f.wantLog(append([]string{"landed 600"}, replayedLog...))
		f.wantHeadTree(landed)
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoTempBranch()
	})

	// Lands between the plan read and Compact's first statement, as a write
	// can between bd compact's log read and its compaction.
	t.Run("landing after the plan was read", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		landed := f.landRow(f.conn())

		if err := f.compact(&compactHookConn{}); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); n != 1 {
			t.Fatalf("landed t row 600 in main's HEAD: present=false, want present=true")
		}
		f.wantLog(append([]string{"landed 600"}, replayedLog...))
		f.wantHeadTree(landed)
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoTempBranch()
	})

	t.Run("another writer commits the staged rows first", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		writer := f.conn()

		hook := &compactHookConn{
			beforeExec: onceBeforeFinalReset(func() { f.landRow(writer) }),
			beforeFollowUp: func() {
				f.exec(writer, "CALL DOLT_COMMIT('-m', 'writer took staging')")
			},
		}
		if err := f.compact(hook); err != nil {
			t.Fatalf("Compact: %v (a nothing-to-commit follow-up must be benign)", err)
		}
		if hook.followUps != 1 {
			t.Fatalf("Compact issued %d follow-up commits, want 1", hook.followUps)
		}
		f.wantLog(append([]string{"writer took staging"}, replayedLog...))
		if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); n != 1 {
			t.Fatalf("landed t row 600 in main's HEAD: present=false, want present=true")
		}
		var one int
		if err := hook.Conn.QueryRowContext(f.ctx, "SELECT 1").Scan(&one); err != nil {
			t.Fatalf("Compact's connection is unusable after a benign nothing-to-commit: %v", err)
		}
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoTempBranch()
	})

	t.Run("pre-staged row joins the follow-up", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.exec(f.db,
			"INSERT INTO u VALUES (800, 'staged')",
			"CALL DOLT_ADD('u')",
			"UPDATE u SET v = 'edited' WHERE id = 1",
		)

		if err := f.compact(&compactHookConn{}); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		if n := f.count("SELECT COUNT(*) FROM u AS OF 'HEAD' WHERE id = 800"); n != 1 {
			t.Fatalf("pre-staged u row 800 in main's HEAD: present=false, want present=true")
		}
		if v := f.value("SELECT v FROM u AS OF 'HEAD' WHERE id = 1"); v != "recent" {
			t.Fatalf("u row 1 in main's HEAD = %q, want the unstaged edit left out", v)
		}
		if v := f.value("SELECT v FROM u WHERE id = 1"); v != "edited" {
			t.Fatalf("u row 1 on main = %q, want the unstaged edit kept", v)
		}
		f.wantStatus("u:false")
	})
}

// AC-3: a quiet compaction keeps main's tree, replaces the old commits with
// one squashed base and adds no follow-up commit.
func TestCompactQuietRunAddsNoCommit(t *testing.T) {
	port := startCompactTestServer(t)

	t.Run("clean working set", func(t *testing.T) {
		f := newCompactFixture(t, port)
		if err := f.compact(&compactHookConn{}); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		f.wantLog(replayedLog)
		f.wantHeadTree(f.head)
		f.wantStatus()
		if n := f.count("SELECT COUNT(*) FROM dolt_status"); n != 0 {
			t.Fatalf("dolt_status has %d rows after a quiet Compact, want none", n)
		}
		f.wantNoTempBranch()
	})

	t.Run("only ignored tables dirty", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.exec(f.db, "CREATE TABLE scratch_x (id INT PRIMARY KEY)", "INSERT INTO scratch_x VALUES (1)")

		if err := f.compact(&compactHookConn{}); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		f.wantLog(replayedLog)
		f.wantHeadTree(f.head)
		f.wantStatus()
		if n := f.count("SELECT COUNT(*) FROM scratch_x"); n != 1 {
			t.Fatalf("ignored scratch_x row on main: present=false, want present=true")
		}
	})
}

// AC-4: when the follow-up commit fails, Compact says which step failed and
// that main already moved, keeps the landed row staged, and removes
// compact-tmp.
func TestCompactFollowupFailureKeepsChangesStaged(t *testing.T) {
	port := startCompactTestServer(t)

	t.Run("follow-up commit fails", func(t *testing.T) {
		f := newCompactFixture(t, port)
		writer := f.conn()
		injected := errors.New("injected follow-up commit failure")

		hook := &compactHookConn{
			beforeExec:         onceBeforeFinalReset(func() { f.landRow(writer) }),
			failFollowupCommit: injected,
		}
		err := f.compact(hook)
		if err == nil {
			t.Fatalf("Compact succeeded, want the follow-up commit's failure")
		}
		if !errors.Is(err, injected) {
			t.Fatalf("Compact error = %v, want it to wrap %v", err, injected)
		}
		if hook.followUps != 1 {
			t.Fatalf("Compact attempted %d follow-up commits, want 1", hook.followUps)
		}
		for _, want := range []string{"commit changes that reached main", "compacted history", "bd dolt commit"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("Compact error = %q, want it to mention %q", err, want)
			}
		}

		f.wantLog(replayedLog)
		f.wantStatus("t:true")
		if n := f.count("SELECT COUNT(*) FROM t WHERE id = 600"); n != 1 {
			t.Fatalf("landed t row 600 on main: present=false, want present=true")
		}
		if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); n != 0 {
			t.Fatalf("landed t row 600 is in main's HEAD, want it left staged (the follow-up never ran)")
		}
		f.wantNoTempBranch()
	})

	t.Run("staged check fails", func(t *testing.T) {
		f := newCompactFixture(t, port)
		writer := f.conn()

		hook := &compactHookConn{
			beforeExec:      onceBeforeFinalReset(func() { f.landRow(writer) }),
			failStagedCheck: true,
		}
		err := f.compact(hook)
		if err == nil {
			t.Fatalf("Compact succeeded, want the staged check's failure")
		}
		if hook.followUps != 0 {
			t.Fatalf("Compact attempted %d follow-up commits after the check failed, want 0", hook.followUps)
		}
		for _, want := range []string{"check main for staged changes", "compacted history", "bd dolt commit"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("Compact error = %q, want it to mention %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "still staged") {
			t.Fatalf("Compact error = %q claims staged changes it never read", err)
		}

		f.wantLog(replayedLog)
		f.wantStatus("t:true")
		f.wantNoTempBranch()
	})
}

// landedSpec is one commit landChain makes on main as another writer: its
// session committer and date, its author, and the statements whose tables
// it stages (none: an empty commit).
type landedSpec struct {
	committer, committerEmail string
	date                      time.Time
	author                    string
	msg                       string
	stage                     string
	stmts                     []string
}

// landChain commits specs to main on writer, in order, staging only each
// spec's table, and returns main's new HEAD.
func (f *compactFixture) landChain(writer *sql.Conn, specs ...landedSpec) string {
	f.t.Helper()
	for _, s := range specs {
		if _, err := writer.ExecContext(f.ctx,
			"SET @@dolt_committer_name = ?, @@dolt_committer_email = ?, @@dolt_committer_date = ?",
			s.committer, s.committerEmail, commitDateArg(s.date)); err != nil {
			f.t.Fatalf("set committer for %q: %v", s.msg, err)
		}
		f.exec(writer, s.stmts...)
		if s.stage != "" {
			if _, err := writer.ExecContext(f.ctx, "CALL DOLT_ADD(?)", s.stage); err != nil {
				f.t.Fatalf("stage %s for %q: %v", s.stage, s.msg, err)
			}
		}
		if _, err := writer.ExecContext(f.ctx, "CALL DOLT_COMMIT('--allow-empty', '-m', ?, '--author', ?)", s.msg, s.author); err != nil {
			f.t.Fatalf("commit %q: %v", s.msg, err)
		}
	}
	if err := clearCommitter(f.ctx, writer); err != nil {
		f.t.Fatalf("clear writer committer: %v", err)
	}
	var h string
	if err := writer.QueryRowContext(f.ctx, "SELECT HASHOF('HEAD')").Scan(&h); err != nil {
		f.t.Fatalf("read landed head: %v", err)
	}
	return h
}

// landedChainSpecs is four commits from three identities whose committer
// dates do not follow their parent order: an insert, an update plus a
// delete, an empty commit and a table no earlier commit touched.
func landedChainSpecs() []landedSpec {
	base := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	return []landedSpec{
		{"Ada", "ada@example.com", base.Add(2 * time.Hour), "Ada <ada@example.com>",
			"landed insert", "t", []string{"INSERT INTO t VALUES (600, 'landed')"}},
		{"Bo (ci)", "bo<ci>@example.com", base.Add(123 * time.Millisecond), "Cy <cy@example.com>",
			"landed update and delete", "t", []string{"UPDATE t SET v = 'changed' WHERE id = 1", "DELETE FROM t WHERE id = 2"}},
		{"Ada", "ada@example.com", base.Add(time.Hour).Add(456 * time.Millisecond), "Ada <ada@example.com>",
			"landed empty", "", nil},
		{"Cy", "cy@example.com", base.Add(3 * time.Hour), "Bo <bo@example.com>",
			"- landed new table", "w", []string{"CREATE TABLE w (id INT PRIMARY KEY)", "INSERT INTO w VALUES (1)"}},
	}
}

// commitsByOrder reads main's full log in ascending commit_order.
func (f *compactFixture) commitsByOrder() []compactCommit {
	f.t.Helper()
	c, err := f.db.Conn(f.ctx)
	if err != nil {
		f.t.Fatalf("pin connection: %v", err)
	}
	defer c.Close()
	commits, err := readCompactLog(f.ctx, c)
	if err != nil {
		f.t.Fatalf("read dolt_log: %v", err)
	}
	sort.Slice(commits, func(i, j int) bool { return commits[i].CommitOrder < commits[j].CommitOrder })
	return commits
}

// wantCaughtUp checks main's history is the replayed history with each of
// landed replayed on top in order, carrying its original's identities,
// dates and message under a new hash, and no follow-up commit.
func (f *compactFixture) wantCaughtUp(landed []compactCommit) {
	f.t.Helper()
	got := f.commitsByOrder()
	if len(got) != len(replayedLog)+len(landed) {
		f.t.Fatalf("main's history after Compact holds %d commits, want %d (the replayed history plus %d caught up)",
			len(got), len(replayedLog)+len(landed), len(landed))
	}
	for i, want := range landed {
		c := got[len(replayedLog)+i]
		if !c.sameMetadata(want) {
			f.t.Fatalf("caught-up commit %d does not preserve the original:\n got  %s\n want %s", i, c.metadata(), want.metadata())
		}
		if c.Hash == want.Hash {
			f.t.Fatalf("caught-up commit %d kept its original hash %s, want it replayed onto the compacted history", i, want.Hash)
		}
	}
	for i, msg := range replayedLog {
		if c := got[len(replayedLog)-1-i]; c.Message != msg {
			f.t.Fatalf("main's history below the caught-up commits: commit %d is %q, want %q", len(replayedLog)-1-i, c.Message, msg)
		}
	}
}

// AC-1: a line of commits that reached main after the plan was read, before
// the catch-up read main's head, is replayed onto the compacted history in
// parent order, each keeping its message, identities and dates; HEAD holds
// the landed tree, nothing is added as a follow-up, working-only changes stay
// pending and compact-tmp is gone.
func TestCompactCatchesUpLandedCommits(t *testing.T) {
	port := startCompactTestServer(t)

	run := func(t *testing.T, during bool) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		writer := f.conn()

		var landed string
		hook := &compactHookConn{}
		if during {
			var done bool
			hook.beforeExec = func(query string) {
				if !done && strings.Contains(query, "DOLT_CHERRY_PICK") {
					done = true
					landed = f.landChain(writer, landedChainSpecs()...)
				}
			}
		} else {
			landed = f.landChain(writer, landedChainSpecs()...)
		}

		if err := f.compact(hook); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		// The originals are off main's history now; the landed head still
		// reads them.
		originals := f.landedOriginals(landed, len(landedChainSpecs()))
		if !originals[1].CommitterDate.Before(originals[0].CommitterDate) {
			t.Fatalf("fixture: landed committer dates follow parent order, want them shuffled")
		}

		f.wantCaughtUp(originals)
		f.wantHeadTree(landed)
		if v := f.value("SELECT v FROM t AS OF 'HEAD' WHERE id = 1"); v != "changed" {
			t.Fatalf("t row 1 in main's HEAD = %q, want the landed update", v)
		}
		if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 2"); n != 0 {
			t.Fatalf("t row 2 in main's HEAD: present=true, want the landed delete")
		}
		if n := f.count("SELECT COUNT(*) FROM w AS OF 'HEAD'"); n != 1 {
			t.Fatalf("w rows in main's HEAD = %d, want the landed table's 1", n)
		}
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoTempBranch()
	}

	t.Run("landing after the plan was read", func(t *testing.T) { run(t, false) })
	t.Run("landing during replay", func(t *testing.T) { run(t, true) })
}

// landedOriginals reads the n newest commits of rev's history, oldest
// first, with both identities.
func (f *compactFixture) landedOriginals(rev string, n int) []compactCommit {
	f.t.Helper()
	rows, err := f.db.QueryContext(f.ctx,
		"SELECT commit_hash, committer, email, date, author, author_email, author_date, message, commit_order FROM dolt_log(?)", rev)
	if err != nil {
		f.t.Fatalf("read dolt_log(%s): %v", rev, err)
	}
	defer rows.Close()
	var commits []compactCommit
	for rows.Next() {
		var c compactCommit
		if err := rows.Scan(&c.Hash, &c.Committer, &c.CommitterEmail, &c.CommitterDate,
			&c.Author, &c.AuthorEmail, &c.AuthorDate, &c.Message, &c.CommitOrder); err != nil {
			f.t.Fatalf("scan dolt_log(%s): %v", rev, err)
		}
		commits = append(commits, c)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatalf("read dolt_log(%s): %v", rev, err)
	}
	sort.Slice(commits, func(i, j int) bool { return commits[i].CommitOrder < commits[j].CommitOrder })
	return commits[len(commits)-n:]
}

// #6645 composed with a concurrent write: the replay check compares the
// replay with the planned head, not main's current HEAD, but that must not
// weaken it. A replay that differs from the planned head still aborts before
// main moves, while a commit that lands on main during the replay stays
// exactly where its writer put it.
func TestCompactRefusesDivergentReplayWhileMainMoves(t *testing.T) {
	port := startCompactTestServer(t)
	f := newCompactFixture(t, port)

	// edit changes config, revert restores it. A plan that squashes up to
	// revert and replays edit again (the shape bd compact's planner refuses)
	// rebuilds a tree with the edit, which revert's tree does not have.
	f.exec(f.db,
		"UPDATE config SET v = 'edited' WHERE k = 'mode'",
		"CALL DOLT_COMMIT('-Am', 'edit')",
		"UPDATE config SET v = 'base' WHERE k = 'mode'",
		"CALL DOLT_COMMIT('-Am', 'revert')",
	)
	hashes, _ := f.log()
	revert, edit := hashes[0], hashes[1]
	f.writePending(f.db)
	writer := f.conn()

	var done bool
	var landed string
	hook := &compactHookConn{Conn: f.conn(), beforeExec: func(query string) {
		if !done && strings.Contains(query, "DOLT_CHERRY_PICK") {
			done = true
			landed = f.landRow(writer)
		}
	}}
	err := Compact(f.ctx, hook, f.initial, revert, revert, 6, []string{edit})
	if err == nil || !strings.Contains(err.Error(), "replayed history differs from main's planned head "+revert[:8]) {
		t.Fatalf("Compact error = %v, want the divergent replay refused against the planned head %s", err, revert[:8])
	}
	if hook.resetDone.Load() {
		t.Fatalf("Compact reset main after the replay check failed")
	}

	var head string
	if err := f.db.QueryRowContext(f.ctx, "SELECT HASHOF('main')").Scan(&head); err != nil {
		t.Fatalf("read main's HEAD: %v", err)
	}
	if head != landed {
		t.Fatalf("main's HEAD = %s after the refused compaction, want the landed commit %s", head, landed)
	}
	if v := f.value("SELECT v FROM config AS OF 'HEAD' WHERE k = 'mode'"); v != "base" {
		t.Fatalf("config mode in main's HEAD = %q, want %q (the divergent replay must not reach main)", v, "base")
	}
	f.wantPendingKept()
	f.wantStatus("config:false", "u:false")
	f.wantNoTempBranch()
}
