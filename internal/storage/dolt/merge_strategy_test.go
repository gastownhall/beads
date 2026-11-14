package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
)

// These tests cover #4992 for server-mode DoltStore: `bd vc merge --strategy`
// was dead code because Merge ran as a bare CALL DOLT_MERGE under autocommit,
// so Dolt rejected any real conflict (Error 1105, "@autocommit must be
// disabled ...") before the strategy could ever be applied.
// MergeWithStrategy pins a single connection and runs
// versioncontrolops.MergeWithStrategy on it, so the strategy actually reaches
// DOLT_CONFLICTS_RESOLVE. The conflict shape (equal updated_at on both sides)
// is the same ambiguous-LWW class TestTryAutoResolveMergeConflicts_IssuesLWW*
// exercises for the auto-resolve path, chosen so the resolver declines it and
// only the explicit strategy resolves it.

func TestMergeWithStrategy_TheirsResolvesAndCommits(t *testing.T) {
	const issueID = "mgs-svr-theirs"
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", "2026-07-10 10:00:00",
		"ours", "2026-07-10 11:00:00", "theirs", "2026-07-10 11:00:00", true)

	ctx, cancel := testContext(t)
	defer cancel()

	conflicts, err := store.MergeWithStrategy(ctx, peerBranch, "theirs")
	if err != nil {
		t.Fatalf("MergeWithStrategy(theirs): %v", err)
	}
	foundIssues := false
	for _, c := range conflicts {
		if c.Field == "issues" {
			foundIssues = true
		}
	}
	if !foundIssues {
		t.Errorf("returned conflicts %+v do not name the issues table", conflicts)
	}

	var title string
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issueID).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "theirs" {
		t.Errorf("title = %q after --strategy theirs, want %q", title, "theirs")
	}

	var dirty int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d dirty table(s) remain: the resolution was not committed", dirty)
	}
}

func TestMergeWithStrategy_OursKeepsLocal(t *testing.T) {
	const issueID = "mgs-svr-ours"
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", "2026-07-10 10:00:00",
		"ours", "2026-07-10 11:00:00", "theirs", "2026-07-10 11:00:00", true)

	ctx, cancel := testContext(t)
	defer cancel()

	conflicts, err := store.MergeWithStrategy(ctx, peerBranch, "ours")
	if err != nil {
		t.Fatalf("MergeWithStrategy(ours): %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatal("expected the resolved conflict to be reported")
	}

	var title string
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issueID).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "ours" {
		t.Errorf("title = %q after --strategy ours, want %q", title, "ours")
	}

	var dirty int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("count dolt_status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("%d dirty table(s) remain: the resolution was not committed", dirty)
	}
}

// TestMerge_NoStrategyStillErrorsWithEscapeHatchHint pins the "no --strategy"
// contract #4992 asked to preserve for server-mode Merge: a real conflict is
// still an error, but the message now names the --strategy escape hatch.
func TestMerge_NoStrategyStillErrorsWithEscapeHatchHint(t *testing.T) {
	const issueID = "mgs-svr-nostrat"
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", "2026-07-10 10:00:00",
		"ours", "2026-07-10 11:00:00", "theirs", "2026-07-10 11:00:00", true)

	ctx, cancel := testContext(t)
	defer cancel()

	_, err := store.Merge(ctx, peerBranch)
	if err == nil {
		t.Fatal("expected the unresolved conflict to error")
	}
	if !strings.Contains(err.Error(), "--strategy") {
		t.Errorf("error = %v, want it to mention the --strategy escape hatch", err)
	}
}

// TestMergeWithStrategy_ResetsSessionFlags pins bd-sesknw's session hygiene:
// MergeWithStrategy sets Dolt's conflict-tolerance flags on its pinned
// connection, and that connection goes back to the pool afterwards. On a
// single-connection pool (setupTestStore) the next borrower is the same
// session, so it must find both flags off after success and after failure —
// otherwise unrelated writes silently commit conflicts.
func TestMergeWithStrategy_ResetsSessionFlags(t *testing.T) {
	const issueID = "mgs-svr-flags"
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", "2026-07-10 10:00:00",
		"ours", "2026-07-10 11:00:00", "theirs", "2026-07-10 11:00:00", true)

	ctx, cancel := testContext(t)
	defer cancel()

	assertFlagsOff := func(when string) {
		t.Helper()
		var allow, force int
		if err := store.db.QueryRowContext(ctx,
			"SELECT @@dolt_allow_commit_conflicts, @@dolt_force_transaction_commit").Scan(&allow, &force); err != nil {
			t.Fatalf("%s: read session flags: %v", when, err)
		}
		if allow != 0 || force != 0 {
			t.Errorf("%s: pooled session kept dolt_allow_commit_conflicts=%d dolt_force_transaction_commit=%d, want 0/0",
				when, allow, force)
		}
	}

	if _, err := store.MergeWithStrategy(ctx, peerBranch, "theirs"); err != nil {
		t.Fatalf("MergeWithStrategy(theirs): %v", err)
	}
	assertFlagsOff("after a successful merge")

	if _, err := store.MergeWithStrategy(ctx, "no-such-branch-mgs", "theirs"); err == nil {
		t.Fatal("MergeWithStrategy of an unknown branch succeeded")
	}
	assertFlagsOff("after a failed merge")
}

// TestMergeWithStrategy_ConcludesViolationOnlyMerge pins bd-sesknw's
// conflict-free settle: this branch deletes an issue the other branch labels,
// so the merge has no row conflict, only an FK cascade violation. Under the
// force flag that merge used to stay open with its violation persisted while
// MergeWithStrategy reported success, so `bd vc merge --strategy` printed
// "Successfully merged" over it. The dangling label must be repaired and the
// merge concluded as one two-parent commit.
func TestMergeWithStrategy_ConcludesViolationOnlyMerge(t *testing.T) {
	const issueID = "mgs-svr-fkonly"
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	db := store.db

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	scan := func(dest any, query string, args ...any) {
		t.Helper()
		if err := db.QueryRowContext(ctx, query, args...).Scan(dest); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}

	var current string
	scan(&current, "SELECT active_branch()")
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'seed', '', '', '', '', 'open', 2, 'task')", issueID)
	exec("CALL DOLT_COMMIT('-Am', 'seed issue')")

	peerBranch := current + "_fkonly_peer"
	exec("CALL DOLT_BRANCH(?)", peerBranch)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", current)
		_, _ = db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})
	exec("CALL DOLT_CHECKOUT(?)", peerBranch)
	exec("INSERT INTO labels (issue_id, label) VALUES (?, 'late-label')", issueID)
	exec("CALL DOLT_COMMIT('-Am', 'peer labels the issue')")
	var peerHead string
	scan(&peerHead, "SELECT HASHOF('HEAD')")
	exec("CALL DOLT_CHECKOUT(?)", current)
	exec("DELETE FROM issues WHERE id = ?", issueID)
	exec("CALL DOLT_COMMIT('-Am', 'delete the issue')")
	var localHead string
	scan(&localHead, "SELECT HASHOF('HEAD')")

	conflicts, err := store.MergeWithStrategy(ctx, peerBranch, "theirs")
	if err != nil {
		t.Fatalf("MergeWithStrategy over a violation-only merge: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %+v, want none: the merge had no row conflict", conflicts)
	}

	var n int
	scan(&n, "SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ?", issueID)
	if n != 0 {
		t.Errorf("HEAD keeps %d dangling label row(s) for the deleted issue", n)
	}
	scan(&n, "SELECT COALESCE(SUM(num_violations), 0) FROM dolt_constraint_violations")
	if n != 0 {
		t.Errorf("%d constraint violation(s) remain", n)
	}
	var merging bool
	scan(&merging, "SELECT is_merging FROM dolt_merge_status")
	if merging {
		t.Error("the merge is still open (dolt_merge_status.is_merging)")
	}
	scan(&n, "SELECT COUNT(*) FROM dolt_status")
	if n != 0 {
		t.Errorf("%d dirty table(s) remain: the merge was not concluded", n)
	}
	for name, head := range map[string]string{"local": localHead, "peer": peerHead} {
		scan(&n, "SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", head)
		if n != 1 {
			t.Errorf("%s pre-merge head %s is not an ancestor of HEAD: no two-parent merge commit", name, head)
		}
	}
	scan(&n, "SELECT COUNT(*) FROM dolt_log WHERE message LIKE CONCAT(?, '%')", "Merge "+peerBranch+": auto-repair")
	if n != 1 {
		t.Errorf("%d merge commit(s) name %s: the conclusion must say what it merged", n, peerBranch)
	}
}

// TestMergeWithStrategy_RefusesOpenMerge pins bd-sesknw's open-merge check. A
// merge can already be open in the branch working set: a violation-only one
// bd left open before this fix, another session's merge in progress, a
// stopped `dolt merge`. DOLT_MERGE then fails with Dolt's "you have not
// committed an active merge", and the FK repair or the strategy resolve used
// to settle that OPEN merge instead, so `bd vc merge --strategy` reported the
// other branch merged although it never was. The call must refuse, name the
// open merge, and leave it exactly as it was.
func TestMergeWithStrategy_RefusesOpenMerge(t *testing.T) {
	for _, tc := range []struct {
		name string
		// open and local are what the open merge's branch and the current
		// branch each do to the seeded issue.
		open, local string
		// blockers counts what keeps the open merge from concluding.
		blockers string
	}{
		{
			name:     "violation-only merge",
			open:     "INSERT INTO labels (issue_id, label) VALUES (?, 'late-label')",
			local:    "DELETE FROM issues WHERE id = ?",
			blockers: "SELECT COALESCE(SUM(num_violations), 0) FROM dolt_constraint_violations",
		},
		{
			name:     "conflicted merge",
			open:     "UPDATE issues SET title = 'theirs' WHERE id = ?",
			local:    "UPDATE issues SET title = 'ours' WHERE id = ?",
			blockers: "SELECT COALESCE(SUM(num_conflicts), 0) FROM dolt_conflicts",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const issueID, nextID = "mgs-svr-open", "mgs-svr-next"
			store, cleanup := setupTestStore(t)
			t.Cleanup(cleanup)
			ctx, cancel := testContext(t)
			t.Cleanup(cancel)
			db := store.db

			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatalf("%s: %v", query, err)
				}
			}
			scan := func(dest any, query string, args ...any) {
				t.Helper()
				if err := db.QueryRowContext(ctx, query, args...).Scan(dest); err != nil {
					t.Fatalf("%s: %v", query, err)
				}
			}
			const insertIssue = "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'seed', '', '', '', '', 'open', 2, 'task')"

			var current string
			scan(&current, "SELECT active_branch()")
			exec(insertIssue, issueID)
			exec("CALL DOLT_COMMIT('-Am', 'seed issue')")

			openBranch, nextBranch := current+"_open", current+"_next"
			exec("CALL DOLT_BRANCH(?)", openBranch)
			exec("CALL DOLT_BRANCH(?)", nextBranch)
			t.Cleanup(func() {
				_, _ = db.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1, @@dolt_force_transaction_commit = 1")
				_, _ = db.ExecContext(ctx, "CALL DOLT_MERGE('--abort')")
				_, _ = db.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 0, @@dolt_force_transaction_commit = 0")
				_, _ = db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", current)
				_, _ = db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", openBranch)
				_, _ = db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", nextBranch)
			})
			exec("CALL DOLT_CHECKOUT(?)", openBranch)
			exec(tc.open, issueID)
			exec("CALL DOLT_COMMIT('-Am', 'open branch changes the issue')")
			exec("CALL DOLT_CHECKOUT(?)", nextBranch)
			exec(insertIssue, nextID)
			exec("CALL DOLT_COMMIT('-Am', 'next branch adds an issue')")
			var nextHead string
			scan(&nextHead, "SELECT HASHOF('HEAD')")
			exec("CALL DOLT_CHECKOUT(?)", current)
			exec(tc.local, issueID)
			exec("CALL DOLT_COMMIT('-Am', 'current branch changes the issue')")

			// Park the merge of openBranch the way bd before this fix left a
			// violation-only one: both conflict-tolerance flags on, and never
			// concluded.
			exec("SET @@dolt_allow_commit_conflicts = 1, @@dolt_force_transaction_commit = 1")
			exec("CALL DOLT_MERGE(?)", openBranch)
			exec("SET @@dolt_allow_commit_conflicts = 0, @@dolt_force_transaction_commit = 0")

			assertOpen := func(when string) {
				t.Helper()
				var merging bool
				var source sql.NullString
				if err := db.QueryRowContext(ctx, "SELECT is_merging, source FROM dolt_merge_status").Scan(&merging, &source); err != nil {
					t.Fatalf("%s: read dolt_merge_status: %v", when, err)
				}
				if !merging || source.String != openBranch {
					t.Fatalf("%s: dolt_merge_status = (is_merging %v, source %q), want the merge of %s open", when, merging, source.String, openBranch)
				}
			}
			assertOpen("fixture")
			var headBefore string
			scan(&headBefore, "SELECT HASHOF('HEAD')")
			var blockersBefore int
			scan(&blockersBefore, tc.blockers)
			if blockersBefore == 0 {
				t.Fatalf("fixture: nothing keeps the merge of %s open (%s)", openBranch, tc.blockers)
			}

			conflicts, err := store.MergeWithStrategy(ctx, nextBranch, "theirs")
			want := "a merge of " + openBranch + " is already open"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("MergeWithStrategy(%s) over an open merge = (%+v, %v), want the refusal %q", nextBranch, conflicts, err, want)
			}

			assertOpen("after the refusal")
			var headAfter string
			scan(&headAfter, "SELECT HASHOF('HEAD')")
			if headAfter != headBefore {
				t.Errorf("HEAD moved %s -> %s: the open merge was concluded", headBefore, headAfter)
			}
			var n int
			scan(&n, "SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", nextHead)
			if n != 0 {
				t.Errorf("%s's head %s is in the log, but it was never merged", nextBranch, nextHead)
			}
			var blockersAfter int
			scan(&blockersAfter, tc.blockers)
			if blockersAfter != blockersBefore {
				t.Errorf("the open merge's blockers went %d -> %d: the refusal repaired or resolved them", blockersBefore, blockersAfter)
			}
		})
	}
}

// TestMergeWithStrategy_FailedSessionResetStillRecomputesBlocked pins how
// MergeWithStrategy treats a session reset that fails after its merge
// concluded (bd-sesknw): the error is recorded, never returned, so the call
// still reports the merge and still runs the is_blocked recompute the merge
// owes. Returning it failed a merge that had already landed and skipped that
// recompute. Not parallel: it swaps the package-level releaseMergeConn seam.
func TestMergeWithStrategy_FailedSessionResetStillRecomputesBlocked(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, false)
	if isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("precondition: bm-w should start unblocked")
	}

	// The peer branch adds "bm-w blocks on bm-x" the way a merge brings it
	// in: a raw row, with no local write path maintaining is_blocked. One
	// pinned session keeps the checkouts together on the two-connection pool.
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	var current string
	if err := conn.QueryRowContext(ctx, "SELECT active_branch()").Scan(&current); err != nil {
		t.Fatalf("read active branch: %v", err)
	}
	peerBranch := current + "_blocking_peer"
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{"CALL DOLT_BRANCH(?)", []any{peerBranch}},
		{"CALL DOLT_CHECKOUT(?)", []any{peerBranch}},
		{"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (?, 'bm-w', 'bm-x', 'blocks', 'peer')",
			[]any{depid.New("bm-w", "bm-x")}},
		{"CALL DOLT_COMMIT('-Am', 'peer: bm-w blocks on bm-x')", nil},
		{"CALL DOLT_CHECKOUT(?)", []any{current}},
	} {
		if _, err := conn.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.query, err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("release fixture connection: %v", err)
	}

	restore := releaseMergeConn
	t.Cleanup(func() { releaseMergeConn = restore })
	released := false
	releaseMergeConn = func(releaseCtx context.Context, pinned *sql.Conn) error {
		released = true
		if err := restore(releaseCtx, pinned); err != nil {
			return err
		}
		return errors.New("injected: reset merge session flags failed")
	}

	if _, err := store.MergeWithStrategy(ctx, peerBranch, "theirs"); err != nil {
		t.Fatalf("MergeWithStrategy with a failed session reset = %v, want nil: the merge concluded", err)
	}
	if !released {
		t.Fatal("MergeWithStrategy did not release its connection through releaseMergeConn")
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Error("bm-w is not blocked after the merge brought in its blocking edge: the is_blocked recompute did not run")
	}
}

// TestMergeWithStrategy_ReleaseConn pins how releaseConflictTolerantConn
// hands MergeWithStrategy's pinned session back: reset and reused when it
// can be, discarded with the reset's own error when it cannot. Each case pins
// one connection of a single-connection pool of its own and sets both flags
// on it, as versioncontrolops.MergeWithStrategy does; the pool's next
// borrower then shows whether the session came back, and with what flags.
func TestMergeWithStrategy_ReleaseConn(t *testing.T) {
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)

	pinFlaggedSession := func(t *testing.T) (*sql.DB, *sql.Conn, int64) {
		t.Helper()
		pool, err := sql.Open("mysql", store.connStr)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		t.Cleanup(func() { _ = pool.Close() })
		pool.SetMaxOpenConns(1)
		conn, err := pool.Conn(ctx)
		if err != nil {
			t.Fatalf("pin connection: %v", err)
		}
		var id int64
		if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
			t.Fatalf("read connection id: %v", err)
		}
		if _, err := conn.ExecContext(ctx,
			"SET @@dolt_allow_commit_conflicts = 1, @@dolt_force_transaction_commit = 1"); err != nil {
			t.Fatalf("set conflict-tolerance flags: %v", err)
		}
		return pool, conn, id
	}
	nextSession := func(t *testing.T, pool *sql.DB) (id int64, allow, force int) {
		t.Helper()
		if err := pool.QueryRowContext(ctx,
			"SELECT CONNECTION_ID(), @@dolt_allow_commit_conflicts, @@dolt_force_transaction_commit").Scan(&id, &allow, &force); err != nil {
			t.Fatalf("read the next session: %v", err)
		}
		return id, allow, force
	}

	// The reset runs detached from the caller's cancellation: on the
	// canceled context itself the SET would fail before reaching the server,
	// discarding a session that could have been cleared and reused.
	t.Run("canceled caller still resets", func(t *testing.T) {
		pool, conn, id := pinFlaggedSession(t)
		canceled, cancelCaller := context.WithCancel(ctx)
		cancelCaller()
		if err := releaseConflictTolerantConn(canceled, conn); err != nil {
			t.Fatalf("release after the caller was canceled: %v", err)
		}
		gotID, allow, force := nextSession(t, pool)
		if gotID != id {
			t.Errorf("session %d was discarded (next session %d), want it reset and reused", id, gotID)
		}
		if allow != 0 || force != 0 {
			t.Errorf("next session has dolt_allow_commit_conflicts=%d dolt_force_transaction_commit=%d, want 0/0", allow, force)
		}
	})

	// A dead session cannot be reset: it is discarded, and the error says why
	// the reset failed, not the sql.ErrConnDone a Close after the discard
	// reports.
	t.Run("dead session reports the reset error", func(t *testing.T) {
		pool, conn, id := pinFlaggedSession(t)
		// Close the physical connection behind database/sql's back, the way a
		// dropped network connection or a server restart leaves it.
		if err := conn.Raw(func(dc any) error { return dc.(driver.Conn).Close() }); err != nil {
			t.Fatalf("close the physical connection: %v", err)
		}
		err := releaseConflictTolerantConn(ctx, conn)
		if err == nil {
			t.Fatal("release over a dead session reported success")
		}
		if errors.Is(err, sql.ErrConnDone) {
			t.Errorf("release returned %v: sql.ErrConnDone hides why the reset failed", err)
		}
		gotID, allow, force := nextSession(t, pool)
		if gotID == id {
			t.Errorf("dead session %d went back to the pool", id)
		}
		if allow != 0 || force != 0 {
			t.Errorf("next session has dolt_allow_commit_conflicts=%d dolt_force_transaction_commit=%d, want 0/0", allow, force)
		}
	})
}
