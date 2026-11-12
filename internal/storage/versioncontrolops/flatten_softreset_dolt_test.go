package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// These tests run Flatten against a real dolt sql-server on compact's
// fixture and hook connection. Flatten's final step, like Compact's, must
// keep what main holds beyond the snapshot it built: writes in main's working
// set and a commit that reaches main after flatten-tmp was built. Its old
// DOLT_RESET('--hard', 'flatten-tmp') discarded both.

const (
	flattenSnapshotMsg = "flatten: squash all history into single commit"
	flattenFollowupMsg = "flatten: commit changes that reached main during flatten"
)

// flattenedLog is main's history after a flatten that added no follow-up
// commit, newest first.
var flattenedLog = []string{flattenSnapshotMsg, "Initialize data repository"}

// flatten runs Flatten on hook, pinning a connection of its own if unset.
func (f *compactFixture) flatten(hook *compactHookConn) error {
	f.t.Helper()
	if hook.Conn == nil {
		hook.Conn = f.conn()
	}
	return Flatten(f.ctx, hook)
}

func (f *compactFixture) wantNoFlattenBranch() {
	f.t.Helper()
	if n := f.count("SELECT COUNT(*) FROM dolt_branches WHERE name = 'flatten-tmp'"); n != 0 {
		f.t.Fatalf("flatten-tmp still exists after Flatten")
	}
}

// wantLandedKept checks landRow's row is in main's HEAD through Flatten's
// follow-up commit, and that the landed commit's own hash and message are not
// in main's history.
func (f *compactFixture) wantLandedKept(landed string) {
	f.t.Helper()
	if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); n != 1 {
		f.t.Fatalf("landed t row 600 in main's HEAD: present=false, want present=true")
	}
	f.wantHeadTree(landed)
	if n := f.count("SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ? OR message = 'landed 600'", landed); n != 0 {
		f.t.Fatalf("main's history still holds the landed commit, want only its rows kept")
	}
}

// AC-2: changes in main's working set that no Dolt commit holds survive
// Flatten, stay out of HEAD and stay pending.
func TestFlattenKeepsUncommittedChanges(t *testing.T) {
	port := startCompactTestServer(t)

	t.Run("written before flatten", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)

		if err := f.flatten(&compactHookConn{}); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		f.wantPendingKept()
		f.wantLog(flattenedLog)
		f.wantHeadTree(f.head)
		f.wantStatus("config:false", "u:false")
		f.wantNoFlattenBranch()
	})

	t.Run("written just before the final reset", func(t *testing.T) {
		f := newCompactFixture(t, port)
		writer := f.conn()

		hook := &compactHookConn{beforeExec: onceBeforeFinalReset(func() { f.writePending(writer) })}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		f.wantPendingKept()
		f.wantLog(flattenedLog)
		f.wantHeadTree(f.head)
		f.wantStatus("config:false", "u:false")
	})
}

// AC-2: a commit that reaches main after flatten-tmp was built is in main's
// HEAD through one preservation commit, without its own hash or message,
// while working-only changes stay pending.
func TestFlattenKeepsCommitLandingBeforeMainMoves(t *testing.T) {
	port := startCompactTestServer(t)

	t.Run("landing commit", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		writer := f.conn()

		var landed string
		hook := &compactHookConn{beforeExec: onceBeforeFinalReset(func() { landed = f.landRow(writer) })}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		if hook.followUps != 1 {
			t.Fatalf("Flatten issued %d preservation commits, want 1", hook.followUps)
		}
		f.wantLog(append([]string{flattenFollowupMsg}, flattenedLog...))
		f.wantLandedKept(landed)
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoFlattenBranch()
	})

	// Lands while flatten-tmp is checked out, before its snapshot commit.
	t.Run("landing while the snapshot is built", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		writer := f.conn()

		var done bool
		var landed string
		hook := &compactHookConn{beforeExec: func(query string) {
			if !done && strings.Contains(query, "DOLT_CHECKOUT('flatten-tmp')") {
				done = true
				landed = f.landRow(writer)
			}
		}}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		if hook.followUps != 1 {
			t.Fatalf("Flatten issued %d preservation commits, want 1", hook.followUps)
		}
		f.wantLog(append([]string{flattenFollowupMsg}, flattenedLog...))
		f.wantLandedKept(landed)
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoFlattenBranch()
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
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v (a nothing-to-commit preservation commit must be benign)", err)
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
		f.wantNoFlattenBranch()
	})

	t.Run("pre-staged row joins the preservation commit", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.exec(f.db,
			"INSERT INTO u VALUES (800, 'staged')",
			"CALL DOLT_ADD('u')",
			"UPDATE u SET v = 'edited' WHERE id = 1",
		)

		if err := f.flatten(&compactHookConn{}); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		f.wantLog(append([]string{flattenFollowupMsg}, flattenedLog...))
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

// AC-2: a quiet flatten adds no preservation commit: the initial commit plus
// the flattened snapshot, nothing else.
func TestFlattenQuietRunAddsNoCommit(t *testing.T) {
	port := startCompactTestServer(t)

	t.Run("clean working set", func(t *testing.T) {
		f := newCompactFixture(t, port)
		hook := &compactHookConn{}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		if hook.followUps != 0 {
			t.Fatalf("Flatten issued %d preservation commits, want 0", hook.followUps)
		}
		f.wantLog(flattenedLog)
		f.wantHeadTree(f.head)
		if n := f.count("SELECT COUNT(*) FROM dolt_status"); n != 0 {
			t.Fatalf("dolt_status has %d rows after a quiet Flatten, want none", n)
		}
		f.wantNoFlattenBranch()
	})

	t.Run("only ignored tables dirty", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.exec(f.db, "CREATE TABLE scratch_x (id INT PRIMARY KEY)", "INSERT INTO scratch_x VALUES (1)")

		hook := &compactHookConn{}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		if hook.followUps != 0 {
			t.Fatalf("Flatten issued %d preservation commits, want 0", hook.followUps)
		}
		f.wantLog(flattenedLog)
		f.wantHeadTree(f.head)
		f.wantStatus()
		if n := f.count("SELECT COUNT(*) FROM scratch_x"); n != 1 {
			t.Fatalf("ignored scratch_x row on main: present=false, want present=true")
		}
		f.wantNoFlattenBranch()
	})
}

// AC-4: a caller cancelling just as Flatten deletes flatten-tmp still gets
// the branch removed, so the next flatten runs.
func TestFlattenCanceledAtFinalDeleteLetsTheNextFlattenRun(t *testing.T) {
	port := startCompactTestServer(t)
	f := newCompactFixture(t, port)

	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	var canceled bool
	hook := &compactHookConn{Conn: f.conn(), beforeExec: func(query string) {
		if !canceled && strings.Contains(query, "DOLT_BRANCH('-D', 'flatten-tmp')") {
			canceled = true
			cancel()
		}
	}}
	if err := Flatten(ctx, hook); !errors.Is(err, context.Canceled) {
		t.Fatalf("Flatten error = %v, want the cancellation reported", err)
	}
	f.wantNoFlattenBranch()
	f.wantLog(flattenedLog)

	f.exec(f.db, "INSERT INTO t VALUES (900, 'after')", "CALL DOLT_COMMIT('-Am', 't 900')")
	if err := f.flatten(&compactHookConn{}); err != nil {
		t.Fatalf("next Flatten: %v", err)
	}
	f.wantLog(flattenedLog)
	f.wantNoFlattenBranch()
}

// compactFixtureLog is the compact fixture's history before any flatten,
// newest first.
var compactFixtureLog = []string{"u 1", "t 2", "t 1", "schema", "Initialize data repository"}

// AC-4: a failure after this invocation created flatten-tmp triggers a safe
// cleanup (back to main, branch deleted) with the primary error preserved,
// joined with any cleanup failure and the branch it left; a preexisting
// branch is never removed, and the next flatten is unblocked.
func TestFlattenCleanup(t *testing.T) {
	port := startCompactTestServer(t)

	// Failures injected at each step after the temp branch exists.
	// mainMoved: whether main already points at the flattened history when
	// the step fails (only the final delete runs after it).
	for _, tc := range []struct {
		name      string
		mainMoved bool
		match     func(f *compactFixture, query string, args []any) bool
	}{
		{"checkout temp branch", false, func(_ *compactFixture, q string, _ []any) bool {
			return strings.Contains(q, "DOLT_CHECKOUT('flatten-tmp')")
		}},
		{"soft reset to initial", false, func(f *compactFixture, q string, args []any) bool {
			return strings.Contains(q, "DOLT_RESET") && len(args) == 1 && args[0] == f.initial
		}},
		{"commit flattened snapshot", false, func(_ *compactFixture, q string, _ []any) bool {
			return strings.Contains(q, "DOLT_COMMIT('-Am'")
		}},
		{"checkout main", false, func(_ *compactFixture, q string, _ []any) bool {
			return strings.Contains(q, "DOLT_CHECKOUT('main')")
		}},
		{"reset main to flattened", false, func(_ *compactFixture, q string, args []any) bool {
			return isFinalReset(q) && len(args) == 0
		}},
		{"delete temp branch", true, func(_ *compactFixture, q string, _ []any) bool {
			return strings.Contains(q, "DOLT_BRANCH('-D'")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCompactFixture(t, port)
			injected := errors.New("injected " + tc.name + " failure")

			// The failure fires once: the cleanup's own run of a failed
			// statement must be allowed to succeed.
			var fired bool
			hook := &compactHookConn{failExec: func(query string, args []any) error {
				if !fired && tc.match(f, query, args) {
					fired = true
					return injected
				}
				return nil
			}}
			if err := f.flatten(hook); !errors.Is(err, injected) {
				t.Fatalf("Flatten error = %v, want it to wrap the injected failure", err)
			}
			// The cleanup removed the temp branch and left main's history
			// as far as it had got, so a rerun works.
			f.wantNoFlattenBranch()
			if tc.mainMoved {
				f.wantLog(flattenedLog)
			} else {
				f.wantLog(compactFixtureLog)
			}
			if err := f.flatten(&compactHookConn{}); err != nil {
				t.Fatalf("rerun Flatten after cleanup: %v", err)
			}
			f.wantLog(flattenedLog)
			f.wantNoFlattenBranch()
		})
	}

	// A cleanup whose checkout also fails keeps the branch (the delete must
	// not run) and joins both failures into the returned error.
	t.Run("cleanup checkout failure joins and keeps the branch", func(t *testing.T) {
		f := newCompactFixture(t, port)
		primary := errors.New("injected snapshot failure")
		cleanupErr := errors.New("injected cleanup checkout failure")
		var failed bool

		hook := &compactHookConn{failExec: func(query string, _ []any) error {
			if !failed && strings.Contains(query, "DOLT_COMMIT('-Am'") {
				failed = true
				return primary
			}
			if failed && strings.Contains(query, "DOLT_CHECKOUT('main')") {
				return cleanupErr
			}
			return nil
		}}
		err := f.flatten(hook)
		if !errors.Is(err, primary) || !errors.Is(err, cleanupErr) {
			t.Fatalf("Flatten error = %v, want the primary and cleanup failures joined", err)
		}
		if !strings.Contains(err.Error(), "flatten-tmp") {
			t.Fatalf("Flatten error = %v, want it to name the branch left behind", err)
		}
		if n := f.count("SELECT COUNT(*) FROM dolt_branches WHERE name = 'flatten-tmp'"); n != 1 {
			t.Fatalf("flatten-tmp after the failed cleanup = %d branch(es), want it kept", n)
		}
		f.wantLog(compactFixtureLog)
	})

	// A flatten-tmp this invocation did not create fails the branch create
	// before cleanup is armed, and is never deleted.
	t.Run("preexisting flatten-tmp is kept", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.exec(f.db, "CALL DOLT_BRANCH('flatten-tmp')")

		err := f.flatten(&compactHookConn{})
		if err == nil || !strings.Contains(err.Error(), "create temp branch") {
			t.Fatalf("Flatten error = %v, want the branch create to fail", err)
		}
		// The advice keys on the server's own branch-exists text.
		if !strings.Contains(err.Error(), "CALL DOLT_BRANCH('-D', 'flatten-tmp')") {
			t.Errorf("Flatten error = %v, want it to say how to delete the leftover branch", err)
		}
		if n := f.count("SELECT COUNT(*) FROM dolt_branches WHERE name = 'flatten-tmp'"); n != 1 {
			t.Fatalf("preexisting flatten-tmp after the failed create = %d, want it kept", n)
		}
		f.wantLog(compactFixtureLog)
	})
}

// AC-5: when checking or committing main's staged set fails after main moved
// onto the flattened history, Flatten says so and how to recover, and main
// keeps the landed rows staged and the pending rows in its working set: no
// hard reset rolls the move back.
func TestFlattenAfterHeadMoveErrorsKeepChangesStaged(t *testing.T) {
	port := startCompactTestServer(t)

	for _, tc := range []struct {
		name       string
		hook       func(f *compactFixture, writer *sql.Conn, injected error) *compactHookConn
		step       string
		followUps  int
		wantInjErr bool
	}{
		{
			name: "preservation commit fails",
			hook: func(f *compactFixture, writer *sql.Conn, injected error) *compactHookConn {
				return &compactHookConn{
					beforeExec:         onceBeforeFinalReset(func() { f.landRow(writer) }),
					failFollowupCommit: injected,
				}
			},
			step:       "commit changes that reached main",
			followUps:  1,
			wantInjErr: true,
		},
		{
			name: "staged check fails",
			hook: func(f *compactFixture, writer *sql.Conn, _ error) *compactHookConn {
				return &compactHookConn{
					beforeExec:      onceBeforeFinalReset(func() { f.landRow(writer) }),
					failStagedCheck: true,
				}
			},
			step: "check main for staged changes",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCompactFixture(t, port)
			f.writePending(f.db)
			injected := errors.New("injected " + tc.name)
			var hardResets int
			hook := tc.hook(f, f.conn(), injected)
			landFirst := hook.beforeExec
			hook.beforeExec = func(query string) {
				if strings.Contains(query, "--hard") {
					hardResets++
				}
				landFirst(query)
			}

			err := f.flatten(hook)
			if err == nil {
				t.Fatalf("Flatten succeeded, want the %s failure", tc.step)
			}
			if tc.wantInjErr && !errors.Is(err, injected) {
				t.Fatalf("Flatten error = %v, want it to wrap %v", err, injected)
			}
			if hook.followUps != tc.followUps {
				t.Fatalf("Flatten attempted %d preservation commits, want %d", hook.followUps, tc.followUps)
			}
			for _, want := range []string{tc.step, "main already points at the flattened history", "dolt_status", "bd dolt commit"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Flatten error = %q, want it to mention %q", err, want)
				}
			}
			if hardResets != 0 {
				t.Fatalf("Flatten issued %d hard resets, want none", hardResets)
			}

			f.wantLog(flattenedLog)
			f.wantStatus("config:false", "t:true", "u:false")
			if n := f.count("SELECT COUNT(*) FROM t WHERE id = 600"); n != 1 {
				t.Fatalf("landed t row 600 on main: present=false, want present=true")
			}
			f.wantPendingKept()
			f.wantNoFlattenBranch()
		})
	}
}
