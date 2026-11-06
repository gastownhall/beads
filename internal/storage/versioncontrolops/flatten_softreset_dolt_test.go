package versioncontrolops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// These tests run Flatten against a real dolt sql-server. The clone-local
// fixture shapes a database like a beads store's working set: tracked tables
// plus dolt_ignored tables carrying the eight clone-local foreign keys of
// fix.CloneLocalFKs, whose enforcement DOLT_RESET('--hard') silently drops
// (bd-7bpkd). The compact fixture (compact_softreset_dolt_test.go) covers
// the t/u/config shape Flatten's other guarantees are pinned on.

// countCloneLocalFKs counts the FK constraints on the fixture's clone-local
// tables, the same plane information_schema the doctor's scan reads.
const countCloneLocalFKs = `SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
	WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_TYPE = 'FOREIGN KEY'
	  AND (TABLE_NAME = 'events' OR TABLE_NAME LIKE 'wisp\_%')`

// flattenFKFixture is one database on a test server with the eight
// clone-local FKs enforced and two commits of history beyond the initial
// commit: 'schema' and 'issue 3'.
type flattenFKFixture struct {
	t   *testing.T
	ctx context.Context
	db  *sql.DB
}

func newFlattenFKFixture(t *testing.T, port string) *flattenFKFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	name := fmt.Sprintf("flatten_%d", time.Now().UnixNano())
	root, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/", port))
	if err != nil {
		t.Fatalf("open server: %v", err)
	}
	defer root.Close()
	if _, err := root.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/%s?parseTime=true", port, name))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := &flattenFKFixture{t: t, ctx: ctx, db: db}
	f.exec(db,
		"CREATE TABLE issues (id INT PRIMARY KEY)",
		"CREATE TABLE wisps (id INT PRIMARY KEY)",
		"CREATE TABLE events (id INT PRIMARY KEY, issue_id INT NOT NULL, "+
			"CONSTRAINT fk_events_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE)",
		"CREATE TABLE wisp_dependencies (id INT PRIMARY KEY, issue_id INT NOT NULL, "+
			"depends_on_wisp_id INT, depends_on_issue_id INT, "+
			"CONSTRAINT fk_wisp_dep_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE, "+
			"CONSTRAINT fk_wisp_dep_wisp_target FOREIGN KEY (depends_on_wisp_id) REFERENCES wisps(id) ON DELETE CASCADE, "+
			"CONSTRAINT fk_wisp_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE)",
		"CREATE TABLE wisp_labels (id INT PRIMARY KEY, issue_id INT NOT NULL, "+
			"CONSTRAINT fk_wisp_labels_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE)",
		"CREATE TABLE wisp_comments (id INT PRIMARY KEY, issue_id INT NOT NULL, "+
			"CONSTRAINT fk_wisp_comments_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE)",
		"CREATE TABLE wisp_events (id INT PRIMARY KEY, issue_id INT NOT NULL, "+
			"CONSTRAINT fk_wisp_events_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE)",
		"CREATE TABLE wisp_child_counters (parent_id INT PRIMARY KEY, "+
			"CONSTRAINT fk_wisp_child_counters_parent FOREIGN KEY (parent_id) REFERENCES wisps(id) ON DELETE CASCADE)",
		// Registered before the first commit, so every FK-carrying table but
		// issues stays clone-local: rows and constraints live in the working
		// set only, exactly the plane a hard reset severs them from.
		"INSERT INTO dolt_ignore VALUES ('wisps', true), ('wisp_%', true), ('events', true)",
		"INSERT INTO issues VALUES (1), (2)",
		"INSERT INTO wisps VALUES (10)",
		"INSERT INTO events VALUES (100, 1)",
		"INSERT INTO wisp_labels VALUES (200, 10)",
		"CALL DOLT_COMMIT('-Am', 'schema')",
		"INSERT INTO issues VALUES (3)",
		"CALL DOLT_COMMIT('-Am', 'issue 3')",
	)
	return f
}

func (f *flattenFKFixture) exec(conn DBConn, queries ...string) {
	f.t.Helper()
	for _, q := range queries {
		if _, err := conn.ExecContext(f.ctx, q); err != nil {
			f.t.Fatalf("%s: %v", q, err)
		}
	}
}

// count runs a COUNT(*) query on the shared pool (main's working set).
func (f *flattenFKFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRowContext(f.ctx, query, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (f *flattenFKFixture) value(query string) string {
	f.t.Helper()
	var v string
	if err := f.db.QueryRowContext(f.ctx, query).Scan(&v); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	return v
}

// conn pins a session of its own, as a second writer or as Flatten's
// connection.
func (f *flattenFKFixture) conn() *sql.Conn {
	f.t.Helper()
	c, err := f.db.Conn(f.ctx)
	if err != nil {
		f.t.Fatalf("pin connection: %v", err)
	}
	f.t.Cleanup(func() { _ = c.Close() })
	return c
}

// wantNoTempBranch fails while a flatten-tmp is still around.
func (f *flattenFKFixture) wantNoTempBranch() {
	f.t.Helper()
	if n := f.count("SELECT COUNT(*) FROM dolt_branches WHERE name = 'flatten-tmp'"); n != 0 {
		f.t.Fatalf("flatten-tmp still exists after Flatten")
	}
}

// TestFlattenKeepsCloneLocalFKs: the flatten ends in a reset of main onto
// the flattened branch; as a hard reset that drops every FK on the
// clone-local tables (bd-7bpkd), as a soft reset it keeps them enforcing
// and cascading, with the tracked snapshot unchanged and the history
// squashed to the initial commit plus one snapshot commit.
func TestFlattenKeepsCloneLocalFKs(t *testing.T) {
	port := startCompactTestServer(t)
	f := newFlattenFKFixture(t, port)

	before := f.count(countCloneLocalFKs)
	if before != 8 {
		f.t.Fatalf("clone-local FKs before flatten = %d, want 8", before)
	}
	preHead := f.value("SELECT HASHOF('HEAD')")

	if err := Flatten(f.ctx, f.conn()); err != nil {
		f.t.Fatalf("Flatten: %v", err)
	}

	if after := f.count(countCloneLocalFKs); after != before {
		f.t.Errorf("clone-local FKs after flatten = %d, want %d", after, before)
	}
	if n := f.count("SELECT COUNT(*) FROM dolt_log"); n != 2 {
		f.t.Errorf("commits after flatten = %d, want 2 (initial + flattened snapshot)", n)
	}
	if n := f.count("SELECT COUNT(*) FROM dolt_diff_summary(?, 'HEAD')", preHead); n != 0 {
		f.t.Errorf("flattened HEAD differs from the pre-flatten HEAD in %d tables, want the same tree", n)
	}
	f.wantNoTempBranch()

	// The kept FKs still enforce: an events row cannot name a missing issue…
	if _, err := f.db.ExecContext(f.ctx,
		"INSERT INTO events (id, issue_id) VALUES (999, 888)"); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		f.t.Errorf("insert of an events row for a missing issue = %v, want a foreign key violation", err)
	}
	// …still cascade over the tracked plane…
	f.exec(f.db, "DELETE FROM issues WHERE id = 1")
	if n := f.count("SELECT COUNT(*) FROM events WHERE issue_id = 1"); n != 0 {
		f.t.Errorf("events for deleted issue 1 = %d, want 0 (fk_events_issue must still cascade)", n)
	}
	// …and over the clone-local plane.
	f.exec(f.db, "DELETE FROM wisps WHERE id = 10")
	if n := f.count("SELECT COUNT(*) FROM wisp_labels WHERE issue_id = 10"); n != 0 {
		f.t.Errorf("wisp_labels for deleted wisp 10 = %d, want 0 (fk_wisp_labels_issue must still cascade)", n)
	}
}

// AC-3: a clone-local FK that was already severed, with historical orphan
// rows, is Flatten's to keep, not repair: it stays missing, its orphans stay
// put, only the FKs that were present survive, and the doctor's scan
// predicate still reports the damage for explicit repair.
func TestFlattenLeavesPreSeveredFKs(t *testing.T) {
	port := startCompactTestServer(t)
	f := newFlattenFKFixture(t, port)

	// Sever one of wisp_dependencies' three FKs and give it a historical
	// orphan (missing wisp 999) plus a row whose reference is NULL, which is
	// not an orphan.
	f.exec(f.db,
		"ALTER TABLE wisp_dependencies DROP FOREIGN KEY fk_wisp_dep_wisp_target",
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, depends_on_issue_id) "+
			"VALUES (300, 10, 999, NULL), (301, 10, NULL, NULL)",
	)
	if got := f.count(countCloneLocalFKs); got != 7 {
		f.t.Fatalf("clone-local FKs after the severing = %d, want 7", got)
	}

	if err := Flatten(f.ctx, f.conn()); err != nil {
		f.t.Fatalf("Flatten: %v", err)
	}

	// The severed FK is still severed; nothing repaired it.
	if got := f.count(countCloneLocalFKs); got != 7 {
		f.t.Errorf("clone-local FKs after flatten = %d, want 7 (the pre-severed one must stay missing)", got)
	}
	if n := f.count(`SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
		WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'fk_wisp_dep_wisp_target'`); n != 0 {
		f.t.Errorf("fk_wisp_dep_wisp_target reappeared after flatten, want it left alone")
	}
	// The historical rows are untouched, NULL references included, and the
	// orphan count is unchanged: a routine flatten must not spring a purge.
	if n := f.count("SELECT COUNT(*) FROM wisp_dependencies WHERE id IN (300, 301)"); n != 2 {
		f.t.Errorf("wisp_dependencies rows 300, 301 after flatten = %d, want both kept", n)
	}
	// The doctor's scan predicate (fix.scanSeveredCloneLocalFKs) reports the
	// constraint missing with the orphan count, so doctor still detects the
	// damage for explicit repair.
	if n := f.count("SELECT COUNT(*) FROM wisp_dependencies t "+
		"WHERE t.depends_on_wisp_id IS NOT NULL "+
		"AND NOT EXISTS (SELECT 1 FROM wisps r WHERE r.id = t.depends_on_wisp_id)"); n != 1 {
		f.t.Errorf("orphaned wisp_dependencies rows after flatten = %d, want 1 (NULL references are not orphans)", n)
	}
	// The seven FKs that were present still enforce.
	if _, err := f.db.ExecContext(f.ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, depends_on_issue_id) "+
			"VALUES (302, 777, NULL, NULL)"); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		f.t.Errorf("insert of a wisp_dependencies row for a missing wisp = %v, want a foreign key violation", err)
	}
}

// flattenHookConn is the DBConn Flatten runs on. It forwards every statement
// to its pinned connection, after letting a test act at a chosen point, fail
// a chosen statement without forwarding it, or cancel just before the
// temp-branch delete. It records every ExecContext with the context it ran
// under, so tests can check which context later (cleanup) statements got.
type flattenHookConn struct {
	*sql.Conn

	// beforeExec runs before each ExecContext is forwarded.
	beforeExec func(query string)
	// failExec, when set, can fail an ExecContext without forwarding it: a
	// non-nil error is returned for that statement.
	failExec func(query string, args []any) error
	// failStagedCheck makes the post-reset dolt_status read fail.
	failStagedCheck bool
	// failFollowupCommit, when set, is returned for a DOLT_COMMIT issued
	// through QueryContext after the final reset, without forwarding it.
	failFollowupCommit error
	// beforeFollowUp runs after the staged check, just before the
	// preservation commit is forwarded.
	beforeFollowUp func()
	// cancelBeforeDelete, when set, runs just before the temp-branch delete
	// is forwarded.
	cancelBeforeDelete func()

	resetDone atomic.Bool
	followUps int // preservation commit CALLs seen (forwarded or failed)

	// execs records every ExecContext Flatten issued, in order, with the
	// context it ran under; failed marks the ones that errored.
	execs []flattenExecRecord
}

// flattenExecRecord is one ExecContext as the hook saw it.
type flattenExecRecord struct {
	query       string
	ctxErr      error
	deadline    time.Time
	hasDeadline bool
	failed      bool
}

func isFlattenFinalReset(query string) bool {
	return strings.Contains(query, "DOLT_RESET(") && strings.Contains(query, "flatten-tmp")
}

func (c *flattenHookConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if c.failExec != nil {
		if err := c.failExec(query, args); err != nil {
			deadline, hasDeadline := ctx.Deadline()
			c.execs = append(c.execs, flattenExecRecord{query: query, ctxErr: ctx.Err(), deadline: deadline, hasDeadline: hasDeadline, failed: true})
			return nil, err
		}
	}
	if c.beforeExec != nil {
		c.beforeExec(query)
	}
	if c.cancelBeforeDelete != nil && strings.Contains(query, "DOLT_BRANCH('-D'") {
		c.cancelBeforeDelete()
	}
	if isFlattenFinalReset(query) {
		c.resetDone.Store(true)
	}
	deadline, hasDeadline := ctx.Deadline()
	res, err := c.Conn.ExecContext(ctx, query, args...)
	c.execs = append(c.execs, flattenExecRecord{query: query, ctxErr: ctx.Err(), deadline: deadline, hasDeadline: hasDeadline, failed: err != nil})
	return res, err
}

func (c *flattenHookConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
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

func (c *flattenHookConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if c.resetDone.Load() && c.failStagedCheck && strings.Contains(query, "dolt_status") {
		return c.Conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM flatten_injected_missing_table")
	}
	return c.Conn.QueryRowContext(ctx, query, args...)
}

// onceBeforeFlattenFinalReset returns a beforeExec hook that runs fn once,
// just before the final soft reset of main onto flatten-tmp is forwarded.
func onceBeforeFlattenFinalReset(fn func()) func(string) {
	var done bool
	return func(query string) {
		if !done && isFlattenFinalReset(query) {
			done = true
			fn()
		}
	}
}

// afterFirstFailure returns the exec records issued after the first failed
// one: a failed flatten's cleanup statements.
func (c *flattenHookConn) afterFirstFailure() []flattenExecRecord {
	for i, r := range c.execs {
		if r.failed {
			return c.execs[i+1:]
		}
	}
	return nil
}

// flattenedLog is the compact fixture's history after a quiet flatten,
// newest first.
var flattenedLog = []string{flattenSnapshotMsg, "Initialize data repository"}

// flatten runs Flatten on hook (whose Conn it pins if unset) on the compact
// fixture's database.
func (f *compactFixture) flatten(hook *flattenHookConn) error {
	f.t.Helper()
	if hook.Conn == nil {
		hook.Conn = f.conn()
	}
	return Flatten(f.ctx, hook)
}

// AC-2: SQL-committed but unstaged changes in main's working set survive
// Flatten, stay out of HEAD and stay pending.
func TestFlattenKeepsUncommittedChanges(t *testing.T) {
	port := startCompactTestServer(t)

	t.Run("written before flatten", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)

		if err := f.flatten(&flattenHookConn{}); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		f.wantPendingKept()
		f.wantLog(flattenedLog)
		f.wantHeadTree(f.head)
		f.wantStatus("config:false", "u:false")
		f.wantNoTempBranch()
	})

	t.Run("written just before the final reset", func(t *testing.T) {
		f := newCompactFixture(t, port)
		writer := f.conn()

		hook := &flattenHookConn{beforeExec: onceBeforeFlattenFinalReset(func() { f.writePending(writer) })}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		f.wantPendingKept()
		f.wantLog(flattenedLog)
		f.wantHeadTree(f.head)
		f.wantStatus("config:false", "u:false")
		f.wantNoTempBranch()
	})
}

// AC-2: a commit that reaches main after the temporary snapshot is built is
// preserved through one preservation commit — its row values committed, its
// original hash and message not retained — while working-only edits stay
// pending.
func TestFlattenKeepsCommitLandingBeforeMainMoves(t *testing.T) {
	port := startCompactTestServer(t)

	// Lands between the snapshot commit and the final reset: only the soft
	// reset plus the preservation commit keep it.
	t.Run("landing just before the final reset", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		writer := f.conn()

		var landed string
		hook := &flattenHookConn{beforeExec: onceBeforeFlattenFinalReset(func() { landed = f.landRow(writer) })}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v", err)
		}

		if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); n != 1 {
			t.Fatalf("landed t row 600 in main's HEAD: present=false, want present=true")
		}
		if n := f.count("SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", landed); n != 0 {
			t.Fatalf("the landed commit's hash survived the flatten, want it replaced by the preservation commit")
		}
		f.wantLog(append([]string{flattenPreserveMsg}, flattenedLog...))
		f.wantHeadTree(landed)
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoTempBranch()
	})

	// Lands while the temp branch is checked out, before its snapshot commit.
	t.Run("landing while the snapshot is built", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		writer := f.conn()

		var done bool
		var landed string
		hook := &flattenHookConn{beforeExec: func(query string) {
			if !done && strings.Contains(query, "DOLT_CHECKOUT('flatten-tmp')") {
				done = true
				landed = f.landRow(writer)
			}
		}}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v", err)
		}

		if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); n != 1 {
			t.Fatalf("landed t row 600 in main's HEAD: present=false, want present=true")
		}
		if n := f.count("SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", landed); n != 0 {
			t.Fatalf("the landed commit's hash survived the flatten, want it replaced by the preservation commit")
		}
		f.wantLog(append([]string{flattenPreserveMsg}, flattenedLog...))
		f.wantHeadTree(landed)
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoTempBranch()
	})

	t.Run("another writer commits the staged rows first", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		writer := f.conn()

		hook := &flattenHookConn{
			beforeExec:   onceBeforeFlattenFinalReset(func() { _ = f.landRow(writer) }),
			beforeFollowUp: func() {
				f.exec(writer, "CALL DOLT_COMMIT('-m', 'writer took staging')")
			},
		}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v (a nothing-to-commit preservation must be benign)", err)
		}
		if hook.followUps != 1 {
			t.Fatalf("Flatten issued %d preservation commits, want 1", hook.followUps)
		}
		f.wantLog(append([]string{"writer took staging"}, flattenedLog...))
		if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); n != 1 {
			t.Fatalf("landed t row 600 in main's HEAD: present=false, want present=true")
		}
		var one int
		if err := hook.Conn.QueryRowContext(f.ctx, "SELECT 1").Scan(&one); err != nil {
			t.Fatalf("Flatten's connection is unusable after a benign nothing-to-commit: %v", err)
		}
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoTempBranch()
	})

	t.Run("pre-staged row joins the preservation commit", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.exec(f.db,
			"INSERT INTO u VALUES (800, 'staged')",
			"CALL DOLT_ADD('u')",
			"UPDATE u SET v = 'edited' WHERE id = 1",
		)

		if err := f.flatten(&flattenHookConn{}); err != nil {
			t.Fatalf("Flatten: %v", err)
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
		f.wantLog(append([]string{flattenPreserveMsg}, flattenedLog...))
		f.wantStatus("u:false")
		f.wantNoTempBranch()
	})
}

// AC-2: a quiet flatten adds no preservation commit: the initial commit plus
// the flattened snapshot, nothing else.
func TestFlattenQuietRunAddsNoCommit(t *testing.T) {
	port := startCompactTestServer(t)

	t.Run("clean working set", func(t *testing.T) {
		f := newCompactFixture(t, port)
		if err := f.flatten(&flattenHookConn{}); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		f.wantLog(flattenedLog)
		f.wantHeadTree(f.head)
		f.wantStatus()
		if n := f.count("SELECT COUNT(*) FROM dolt_status"); n != 0 {
			t.Fatalf("dolt_status has %d rows after a quiet Flatten, want none", n)
		}
		f.wantNoTempBranch()
	})

	t.Run("only ignored tables dirty", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.exec(f.db, "CREATE TABLE scratch_x (id INT PRIMARY KEY)", "INSERT INTO scratch_x VALUES (1)")

		if err := f.flatten(&flattenHookConn{}); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		f.wantLog(flattenedLog)
		f.wantHeadTree(f.head)
		f.wantStatus()
		if n := f.count("SELECT COUNT(*) FROM scratch_x"); n != 1 {
			t.Fatalf("ignored scratch_x row on main: present=false, want present=true")
		}
		f.wantNoTempBranch()
	})
}
