//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// withSeedConn opens a short-lived raw SQL connection on main for fixture
// seeding and closes it — engine included — before fn returns. The store's
// merge/recompute calls open their own engine on the same database
// directory, and a second live engine there is a state no production flow
// reaches: the open wedges in the driver's retry loop (observed as a 19m
// test timeout), so branch seeding must not overlap the store calls. Branch
// and commit state persists in the repository; only the connection dies.
func withSeedConn(t *testing.T, te *testEnv, fn func(conn *sql.Conn)) {
	t.Helper()
	ctx := t.Context()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		cleanup()
		t.Fatalf("pin seed connection: %v", err)
	}
	fn(conn)
	_ = conn.Close()
	if err := cleanup(); err != nil {
		t.Fatalf("close seed connection: %v", err)
	}
}

// seedIssueOnConn inserts one open issue row through raw SQL and commits it
// with a full-working-set DOLT_COMMIT — the same seed shape
// seedIssueRetitleConflict (merge_strategy_test.go) uses. Raw-SQL commits are
// seed history only; the commits under test are the ones the store's merge
// and recompute paths mint.
func seedIssueOnConn(t *testing.T, ctx context.Context, conn *sql.Conn, id, msg string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'seed', '', '', '', '', 'open', 2, 'task')",
		id); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", msg); err != nil {
		t.Fatalf("commit %q: %v", msg, err)
	}
}

// execOnConn runs one statement on the seed connection.
func execOnConn(t *testing.T, ctx context.Context, conn *sql.Conn, stmt string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, stmt, args...); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

// TestEmbeddedMergeAndRecomputeCommitAuthorFollowsGitAuthorEnv pins GH#7628
// AC-3: the remaining embedded commits — the non-fast-forward branch merges
// (both the plain Merge and the --strategy path) and the is_blocked recompute
// repair commits (the post-merge pass and the full recompute) — take the
// resolved GIT_AUTHOR_* identity instead of the constant beads <beads@local>
// that merge and recompute commits previously carried. Merge commit messages
// are Dolt's own, so only authorship is asserted there; the recompute rounds
// assert the message too, because they pin which commit landed.
func TestEmbeddedMergeAndRecomputeCommitAuthorFollowsGitAuthorEnv(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	t.Setenv("GIT_AUTHOR_NAME", "Alice Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "alice@example.com")

	t.Run("merge", func(t *testing.T) {
		te := newTestEnv(t, "mca")
		withSeedConn(t, te, func(conn *sql.Conn) {
			ctx := t.Context()
			seedIssueOnConn(t, ctx, conn, "mca-m1", "seed main")
			execOnConn(t, ctx, conn, "CALL DOLT_BRANCH(?, 'HEAD')", "feat-a")
			seedIssueOnConn(t, ctx, conn, "mca-m2", "main work")
			execOnConn(t, ctx, conn, "CALL DOLT_CHECKOUT(?)", "feat-a")
			seedIssueOnConn(t, ctx, conn, "mca-f1", "feature work")
			execOnConn(t, ctx, conn, "CALL DOLT_CHECKOUT('main')")
		})

		conflicts, err := te.store.Merge(t.Context(), "feat-a")
		if err != nil {
			t.Fatalf("Merge feat-a: %v", err)
		}
		if len(conflicts) != 0 {
			t.Fatalf("Merge feat-a: unexpected conflicts: %v", conflicts)
		}
		assertNewCommitAuthored(t, te, "Alice Author", "alice@example.com", "")
	})

	t.Run("merge_with_strategy", func(t *testing.T) {
		te := newTestEnv(t, "mcs")
		withSeedConn(t, te, func(conn *sql.Conn) {
			ctx := t.Context()
			seedIssueOnConn(t, ctx, conn, "mcs-m1", "seed main")
			execOnConn(t, ctx, conn, "CALL DOLT_BRANCH(?, 'HEAD')", "feat-b")
			seedIssueOnConn(t, ctx, conn, "mcs-m2", "main work")
			execOnConn(t, ctx, conn, "CALL DOLT_CHECKOUT(?)", "feat-b")
			seedIssueOnConn(t, ctx, conn, "mcs-f1", "feature work")
			execOnConn(t, ctx, conn, "CALL DOLT_CHECKOUT('main')")
		})

		conflicts, err := te.store.MergeWithStrategy(t.Context(), "feat-b", "ours")
		if err != nil {
			t.Fatalf("MergeWithStrategy feat-b: %v", err)
		}
		if len(conflicts) != 0 {
			t.Fatalf("MergeWithStrategy feat-b: unexpected conflicts: %v", conflicts)
		}
		assertNewCommitAuthored(t, te, "Alice Author", "alice@example.com", "")
	})

	t.Run("recompute_after_merge", func(t *testing.T) {
		te := newTestEnv(t, "mcr")
		withSeedConn(t, te, func(conn *sql.Conn) {
			ctx := t.Context()
			// mcr-r has no blockers, so its correct is_blocked is 0; the
			// feature branch commits a stale 1 the way a clone whose local
			// hooks were bypassed would, and the merge brings that stale
			// row in — exactly what recomputeBlockedAfterPull exists to
			// repair.
			seedIssueOnConn(t, ctx, conn, "mcr-r", "seed main")
			execOnConn(t, ctx, conn, "CALL DOLT_BRANCH(?, 'HEAD')", "feat-c")
			seedIssueOnConn(t, ctx, conn, "mcr-m1", "main work")
			execOnConn(t, ctx, conn, "CALL DOLT_CHECKOUT(?)", "feat-c")
			execOnConn(t, ctx, conn, "UPDATE issues SET is_blocked = 1 WHERE id = ?", "mcr-r")
			execOnConn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', ?)", "feature work with stale is_blocked")
			execOnConn(t, ctx, conn, "CALL DOLT_CHECKOUT('main')")
		})

		if _, err := te.store.Merge(t.Context(), "feat-c"); err != nil {
			t.Fatalf("Merge feat-c: %v", err)
		}
		// The merge commit is second-newest; the recompute repair that
		// corrected the merged-in stale flag owns the newest row.
		assertNewCommitAuthored(t, te, "Alice Author", "alice@example.com", "bd: recompute is_blocked after pull")
	})

	t.Run("recompute_all", func(t *testing.T) {
		te := newTestEnv(t, "mcf")
		withSeedConn(t, te, func(conn *sql.Conn) {
			ctx := t.Context()
			// A committed stale flag (no blockers, is_blocked forced to
			// 1) — the state `bd recompute-blocked` exists to repair.
			seedIssueOnConn(t, ctx, conn, "mcf-r", "seed main")
			execOnConn(t, ctx, conn, "UPDATE issues SET is_blocked = 1 WHERE id = ?", "mcf-r")
			execOnConn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', ?)", "commit stale is_blocked")
		})

		changed, err := te.store.RecomputeAllBlocked(t.Context())
		if err != nil {
			t.Fatalf("RecomputeAllBlocked: %v", err)
		}
		if changed != 1 {
			t.Fatalf("RecomputeAllBlocked corrected %d rows, want 1", changed)
		}
		assertNewCommitAuthored(t, te, "Alice Author", "alice@example.com", versioncontrolops.BlockedRecomputeCommitMsg)
	})
}