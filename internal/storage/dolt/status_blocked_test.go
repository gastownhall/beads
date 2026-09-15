package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// countStatusDrift wraps the read-only detection behind the bd doctor
// "Status Blocked Drift" check and the 'bd recompute-blocked --status' flag
// (be-ntbxt): issues/wisps whose manually-set status='blocked' disagrees with
// the dependency graph because their blockers all closed (or none were ever
// recorded).
func countStatusDrift(ctx context.Context, t *testing.T, db *sql.DB) int64 {
	t.Helper()
	n, err := issueops.CountStatusBlockedDriftInTx(ctx, db)
	if err != nil {
		t.Fatalf("CountStatusBlockedDriftInTx: %v", err)
	}
	return n
}

// fixStatusDrift wraps the repair behind 'bd recompute-blocked --fix' and
// 'bd doctor --fix' and returns the number of rows it corrected.
func fixStatusDrift(ctx context.Context, t *testing.T, db *sql.DB) int64 {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin status-drift fix tx: %v", err)
	}
	changed, err := issueops.FixStatusBlockedDriftInTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("FixStatusBlockedDriftInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit status-drift fix tx: %v", err)
	}
	return changed
}

func statusOf(ctx context.Context, t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var status string
	if err := db.QueryRowContext(ctx, "SELECT status FROM issues WHERE id = ?", id).Scan(&status); err != nil {
		t.Fatalf("read status of %s: %v", id, err)
	}
	return status
}

// TestStatusBlockedDrift_ClosedBlockerIsFlaggedAndFixed is the be-ntbxt bug's
// core repro: a bead manually left status='blocked' whose only blocker has
// since closed. 'bd close' already prints "Newly unblocked" when this
// happens, but nothing actually clears the status — the bead stays invisible
// to 'bd ready' forever. Detection must see it, the fix must return it to
// open, and detection and fix must then agree the database is consistent.
func TestStatusBlockedDrift_ClosedBlockerIsFlaggedAndFixed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true) // bm-w blocks on open bm-x
	// Manually mark status='blocked', mirroring how the fleet-wide victims
	// the bug report found got there (types.StatusBlocked is a manual enum
	// value, never auto-cleared).
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = 'blocked' WHERE id = 'bm-w'"); err != nil {
		t.Fatalf("seed status=blocked: %v", err)
	}
	if n := countStatusDrift(ctx, t, store.db); n != 0 {
		t.Fatalf("blocker still open: want 0 drifted rows, got %d", n)
	}

	// Close the blocker — the exact 'bd close' path whose "Newly unblocked"
	// message currently lies about the transition it claims.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = 'closed' WHERE id = 'bm-x'"); err != nil {
		t.Fatalf("close blocker: %v", err)
	}
	if got := statusOf(ctx, t, store.db, "bm-w"); got != "blocked" {
		t.Fatalf("setup: bm-w must still read status=blocked before the fix (the drift IS the bug), got %q", got)
	}
	if n := countStatusDrift(ctx, t, store.db); n != 1 {
		t.Fatalf("after blocker closed: want 1 drifted row, got %d", n)
	}

	if changed := fixStatusDrift(ctx, t, store.db); changed != 1 {
		t.Fatalf("fix: want 1 row corrected, got %d", changed)
	}
	if got := statusOf(ctx, t, store.db, "bm-w"); got != "open" {
		t.Fatalf("after fix: bm-w status = %q, want open", got)
	}
	if n := countStatusDrift(ctx, t, store.db); n != 0 {
		t.Fatalf("after fix: want 0 drifted rows, got %d", n)
	}
	if again := fixStatusDrift(ctx, t, store.db); again != 0 {
		t.Fatalf("fix must be idempotent: want 0 corrected on second run, got %d", again)
	}
}

// TestStatusBlockedDrift_NoBlockerRecordedIsFlaggedAndFixed covers the other
// half of the bug's fleet impact (be-ntbxt: 15 of 29 stranded beads never had
// a blocker on record at all, not just a closed one). Same drift, same fix.
func TestStatusBlockedDrift_NoBlockerRecordedIsFlaggedAndFixed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	iss := &types.Issue{ID: "bm-solo", Title: "bm-solo", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = 'blocked' WHERE id = 'bm-solo'"); err != nil {
		t.Fatalf("seed status=blocked: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed unblocked solo issue')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit seed: %v", err)
	}

	if n := countStatusDrift(ctx, t, store.db); n != 1 {
		t.Fatalf("no blocker ever recorded: want 1 drifted row, got %d", n)
	}
	if changed := fixStatusDrift(ctx, t, store.db); changed != 1 {
		t.Fatalf("fix: want 1 row corrected, got %d", changed)
	}
	if got := statusOf(ctx, t, store.db, "bm-solo"); got != "open" {
		t.Fatalf("after fix: bm-solo status = %q, want open", got)
	}
}

// TestStatusBlockedDrift_OpenBlockerIsNeverFlaggedOrTouched is the negative
// case the exit contract calls out explicitly: a bead legitimately blocked on
// a still-open blocker must never be flagged or fixed. Getting this wrong
// would force-open beads that are genuinely not ready to work — the
// regression this test guards against is exactly as important as detecting
// the drift in the first place.
func TestStatusBlockedDrift_OpenBlockerIsNeverFlaggedOrTouched(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true) // bm-w blocks on open bm-x
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = 'blocked' WHERE id = 'bm-w'"); err != nil {
		t.Fatalf("seed status=blocked: %v", err)
	}

	if n := countStatusDrift(ctx, t, store.db); n != 0 {
		t.Fatalf("blocker still open: want 0 drifted rows, got %d", n)
	}
	if changed := fixStatusDrift(ctx, t, store.db); changed != 0 {
		t.Fatalf("fix must not touch a legitimately blocked row: want 0 corrected, got %d", changed)
	}
	if got := statusOf(ctx, t, store.db, "bm-w"); got != "blocked" {
		t.Fatalf("fix must not touch a legitimately blocked row: status = %q, want still blocked", got)
	}
}
