//go:build cgo

package dolt

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestDoltAddAndCommitSkipsEmptyCommitWithUnrelatedDirtyTable is a regression
// test for the high-frequency "nothing to commit" Dolt warning spam.
//
// Root cause: doltAddAndCommit / doltAddAndCommitInTx stage only a FIXED table
// list (e.g. ["dependencies"]) and then issue DOLT_COMMIT('-m', ...). An
// idempotent no-op write (re-adding an existing dependency via INSERT IGNORE, or
// removing a non-existent one) leaves that table clean, so the DOLT_ADD stages
// nothing and the '-m' commit fails server-side with "nothing to commit" —
// logged as a warning on every call. At reconcile cadence this floods dolt.log.
//
// Crucially, a *global* "any pending changes" check (issueops.HasPendingChanges,
// as used by StageAndCommit which stages ALL dirty tables) is NOT sufficient
// here: when an UNRELATED table (config, issues, …) is dirty in the working set,
// the global check reports "pending" yet the selective DOLT_ADD still stages
// nothing, so the empty '-m' commit fires anyway. The fix checks the STAGED set
// (hasStagedChanges) after staging the target tables.
//
// This test reproduces that exact condition: it leaves `config` dirty, then
// asserts no-op dependency writes neither error nor advance HEAD, while real
// writes still commit (no over-suppression).
func TestDoltAddAndCommitSkipsEmptyCommitWithUnrelatedDirtyTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	a := &types.Issue{ID: "pc-a", Title: "A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	b := &types.Issue{ID: "pc-b", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	c := &types.Issue{ID: "pc-c", Title: "C", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	for _, iss := range []*types.Issue{a, b, c} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", iss.ID, err)
		}
	}

	dep := &types.Dependency{IssueID: a.ID, DependsOnID: b.ID, Type: types.DepBlocks}

	// Real add: must commit (HEAD advances).
	head0 := headCommit(t, store)
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("real AddDependency: %v", err)
	}
	head1 := headCommit(t, store)
	if head1 == head0 {
		t.Fatalf("real AddDependency did not advance HEAD (%s)", head1)
	}

	// Dirty an UNRELATED table without committing it. This is the working-set
	// state that defeats a naive global-pending guard: `config` shows up in
	// dolt_status, so "any pending changes" is true, but the dependencies table
	// is clean for the idempotent calls below.
	dirtyConfigUncommitted(t, store)

	// Idempotent add (same edge, INSERT IGNORE no-op): must NOT error and must
	// NOT advance HEAD — and (the bug) must NOT issue an empty '-m' commit.
	headBefore := headCommit(t, store)
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("idempotent AddDependency returned error (the bug): %v", err)
	}
	if got := headCommit(t, store); got != headBefore {
		t.Errorf("idempotent AddDependency advanced HEAD %s -> %s (empty commit)", headBefore, got)
	}

	// No-op remove (edge that does not exist): same expectations.
	headBefore = headCommit(t, store)
	if err := store.RemoveDependency(ctx, a.ID, c.ID, "tester"); err != nil {
		t.Fatalf("no-op RemoveDependency returned error (the bug): %v", err)
	}
	if got := headCommit(t, store); got != headBefore {
		t.Errorf("no-op RemoveDependency advanced HEAD %s -> %s (empty commit)", headBefore, got)
	}

	// Regression guard against over-suppression: a REAL remove must still commit
	// even though config is dirty in the working set.
	headBefore = headCommit(t, store)
	if err := store.RemoveDependency(ctx, a.ID, b.ID, "tester"); err != nil {
		t.Fatalf("real RemoveDependency: %v", err)
	}
	if got := headCommit(t, store); got == headBefore {
		t.Errorf("real RemoveDependency did not advance HEAD (%s) — guard over-suppressed a real change", got)
	}
}

// TestClaimAndCloseNoRegressionWithUnrelatedDirtyTable guards the issues.go
// commit sites that were consolidated onto the guarded doltAddAndCommitInTx
// helper (UpdateIssue/ClaimIssue/ClaimReadyIssue/CloseIssue/Delete*).
//
// IMPORTANT — what this test does and does NOT prove. The empty-commit BUG is a
// server-side "nothing to commit" WARNING, not a HEAD movement: an empty
// DOLT_COMMIT('-m') with nothing staged ERRORS (and is swallowed), so it never
// creates a commit and HEAD never advances WHETHER OR NOT the guard is present.
// A HEAD-based integration test therefore cannot discriminate the warning. The
// discriminating coverage lives in the sqlmock unit tests
// (versioncontrolops.TestStageAndCommit* and dolt.TestHasStagedChanges), which
// assert DOLT_COMMIT is NOT issued on an empty staged set and FAIL if the guard
// is reverted. The issues.go sites merely ROUTE to that already-tested helper.
//
// This test's job is the COMPLEMENTARY half: prove the consolidation introduced
// no REGRESSION / over-suppression — idempotent re-claim is a clean no-op (no
// error, no spurious commit), and real claim/close still commit (HEAD advances),
// even with an unrelated table (config) dirty in the working set. The true
// warning-rate proof is the live-city measurement at deploy time.
func TestClaimAndCloseNoRegressionWithUnrelatedDirtyTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	for _, id := range []string{"pc-claim", "pc-close"} {
		if err := store.CreateIssue(ctx, mk(id), "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	// Real claim must commit (HEAD advances).
	head0 := headCommit(t, store)
	if err := store.ClaimIssue(ctx, "pc-claim", "actorX"); err != nil {
		t.Fatalf("real ClaimIssue: %v", err)
	}
	if headCommit(t, store) == head0 {
		t.Fatal("real ClaimIssue did not advance HEAD")
	}

	// Defeat a naive global-pending guard: leave config dirty in the working set,
	// so dolt_status is non-empty for the idempotent no-op below.
	dirtyConfigUncommitted(t, store)

	// Idempotent re-claim by the SAME actor (already in_progress): no write, no
	// events row — must NOT error and must NOT advance HEAD (the residual bug was
	// an empty '-m' commit firing here because config was dirty).
	headBefore := headCommit(t, store)
	if err := store.ClaimIssue(ctx, "pc-claim", "actorX"); err != nil {
		t.Fatalf("idempotent re-claim returned error (the bug): %v", err)
	}
	if got := headCommit(t, store); got != headBefore {
		t.Errorf("idempotent re-claim advanced HEAD %s -> %s (empty commit)", headBefore, got)
	}

	// Over-suppression guard: a REAL close still rewrites closed_at/updated_at, so
	// it MUST advance HEAD even with config dirty in the working set.
	headBefore = headCommit(t, store)
	if err := store.CloseIssue(ctx, "pc-close", "done", "tester", ""); err != nil {
		t.Fatalf("real CloseIssue (config dirty): %v", err)
	}
	if got := headCommit(t, store); got == headBefore {
		t.Error("real CloseIssue did not advance HEAD — guard over-suppressed a real change")
	}
}

// headCommit returns the current HEAD commit hash via dolt_log.
func headCommit(t *testing.T, store *DoltStore) string {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	hash, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	return hash
}

// dirtyConfigUncommitted writes a row to the (non-ignored) config table on the
// store's pool and leaves it uncommitted, so dolt_status reports config dirty.
func dirtyConfigUncommitted(t *testing.T, store *DoltStore) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO config (`key`, value) VALUES ('empty_commit_test', 'x') "+
			"ON DUPLICATE KEY UPDATE value = VALUES(value)"); err != nil {
		t.Fatalf("dirty config: %v", err)
	}
	var n int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'config'").Scan(&n); err != nil {
		t.Fatalf("verify config dirty: %v", err)
	}
	if n == 0 {
		t.Fatalf("precondition failed: config not dirty in working set")
	}
}

// TestHasStagedChanges is a fast, server-free unit test of the guard predicate
// (issueops.HasStagedChanges) used by StageAndCommit, doltAddAndCommit and
// doltAddAndCommitInTx. It asserts the helper returns false on an empty staged
// set (so the caller skips the empty '-m' commit) and true when something is
// staged (so a real change still commits). Mirrors the sqlmock style of
// versioncontrolops.TestStageAndCommit*.
func TestHasStagedChanges(t *testing.T) {
	matchStaged := regexp.MustCompile(`SELECT COUNT\(\*\) FROM dolt_status WHERE staged = 1`)

	t.Run("empty staged set -> false", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(matchStaged.String()).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

		staged, err := issueops.HasStagedChanges(context.Background(), db)
		if err != nil {
			t.Fatalf("HasStagedChanges: %v", err)
		}
		if staged {
			t.Error("expected staged=false for an empty staged set")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("non-empty staged set -> true", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(matchStaged.String()).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

		staged, err := issueops.HasStagedChanges(context.Background(), db)
		if err != nil {
			t.Fatalf("HasStagedChanges: %v", err)
		}
		if !staged {
			t.Error("expected staged=true when rows are staged")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}
