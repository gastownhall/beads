package dolt

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/depid"
)

// canceledContext returns a child of ctx that is already canceled, as the
// root context of `bd dolt pull` or `bd sync` is after its first SIGINT or
// SIGTERM.
func canceledContext(ctx context.Context) context.Context {
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	return canceled
}

// TestFinishCLIPullSettlesAfterCallerCancellation pins, against a Dolt
// server, that the conflicts a dolt CLI pull left are still resolved when the
// caller was canceled after the CLI exited (the first SIGINT or SIGTERM,
// GH#7989). The CLI commits its merge together with its conflicts, so they
// are already in the server's working set; run on the canceled context, the
// resolver could not get a connection and left them there, refusing every
// later write on the branch.
func TestFinishCLIPullSettlesAfterCallerCancellation(t *testing.T) {
	store, peerBranch := setupDependencyMergeConflict(t, "blocks", "alice", "blocks", "bob")
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	// Commit the conflicted merge as the dolt CLI's pull does: with
	// @@dolt_force_transaction_commit set in its own session, which bd's
	// connections never have, so reset it once the merge is in.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SET @@dolt_force_transaction_commit = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("force transaction commit: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		t.Logf("merge returned: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit the conflicted merge: %v", err)
	}
	if _, err := db.ExecContext(ctx, "SET @@dolt_force_transaction_commit = 0"); err != nil {
		t.Fatalf("reset force transaction commit: %v", err)
	}
	var conflicts int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("read dolt_conflicts: %v", err)
	}
	if conflicts == 0 {
		t.Fatal("the merge left no conflict in the working set, so this test cannot exercise the resolver")
	}

	cliErr := errors.New("dolt pull failed: Automatic merge failed; fix conflicts and then commit the result.: exit status 1")
	if err := store.finishCLIPull(canceledContext(ctx), cliErr); err != nil {
		t.Fatalf("finishCLIPull after the caller was canceled = %v, want the conflicts resolved", err)
	}

	var merging bool
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("read dolt_conflicts: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		t.Fatalf("read dolt_merge_status: %v", err)
	}
	if conflicts != 0 || merging {
		t.Errorf("working set left mid-merge: %d conflicted table(s), is_merging=%v", conflicts, merging)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('depc-z', 'depc-z', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Errorf("a write after the pull: %v", err)
	}
}

// TestFinishCLIPullBoundsSettleAfterCallerCancellation pins the bound on the
// settle after a CLI pull: when the server does not answer the resolver, a
// caller canceled after the CLI exited gets its result afterMergeGrace later,
// not at once (the settle must outlive the cancellation) and not whenever the
// server answers (a first signal must still end bd).
func TestFinishCLIPullBoundsSettleAfterCallerCancellation(t *testing.T) {
	prevGrace := afterMergeGrace
	afterMergeGrace = 100 * time.Millisecond
	t.Cleanup(func() { afterMergeGrace = prevGrace })

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
		WillDelayFor(time.Minute).
		WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))

	store := &DoltStore{db: db}
	cliErr := errors.New("dolt pull failed: exit status 1")
	start := time.Now()
	err = store.finishCLIPull(canceledContext(t.Context()), cliErr)
	took := time.Since(start)
	if !errors.Is(err, cliErr) {
		t.Errorf("finishCLIPull = %v, want the CLI's error once the settle gave up", err)
	}
	if took < afterMergeGrace {
		t.Errorf("settle gave up after %s, before the %s grace: it did not outlive the caller's cancellation", took, afterMergeGrace)
	}
	if took > 30*time.Second {
		t.Errorf("settle ran %s after the caller's cancellation, past the %s grace", took, afterMergeGrace)
	}
}

// TestRecomputeBlockedAfterPullOutlivesCallerCancellation pins, against a
// Dolt server, that the is_blocked recompute after a pull's merge still runs,
// or still leaves its marker for the next pull (bd-578h9.11), when the caller
// was canceled after the merge (GH#7989). Run on the canceled context, both
// failed, and the next pull, which merges nothing, skipped the recompute and
// left is_blocked stale.
func TestRecomputeBlockedAfterPullOutlivesCallerCancellation(t *testing.T) {
	t.Run("recomputes", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		preHead := seedBlockedPair(ctx, t, store, false)
		// The merge: the remote added bm-w blocks-on bm-x without touching
		// is_blocked, as a pulled row does.
		if _, err := store.db.ExecContext(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (?, 'bm-w', 'bm-x', 'blocks', 'remote')",
			depid.New("bm-w", "bm-x")); err != nil {
			t.Fatalf("simulate merged edge: %v", err)
		}
		if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-am', 'merged: remote added edge')"); err != nil {
			t.Fatalf("commit merged edge: %v", err)
		}

		if err := store.recomputeBlockedAfterPull(canceledContext(ctx), preHead); err != nil {
			t.Fatalf("recompute after the caller was canceled: %v", err)
		}
		if !isBlocked(ctx, t, store.db, "bm-w") {
			t.Error("bm-w not blocked: the recompute did not run after the caller was canceled")
		}
	})

	t.Run("leaves the marker", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := testContext(t)
		defer cancel()

		// A malformed pre-merge head is a recompute failure a healthy server
		// produces on demand; the marker records it as a full recompute.
		if err := store.recomputeBlockedAfterPull(canceledContext(ctx), "not-a-commit"); err == nil {
			t.Fatal("recompute from a malformed commit succeeded")
		}
		var marker string
		if err := store.db.QueryRowContext(ctx,
			"SELECT value FROM metadata WHERE `key` = 'is_blocked_recompute_pending'").Scan(&marker); err != nil {
			t.Fatalf("read the marker after the caller was canceled: %v", err)
		}
		if marker != "full" {
			t.Errorf("marker = %q, want %q", marker, "full")
		}
	})
}
