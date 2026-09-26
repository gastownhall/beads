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

// addStatusDriftDep adds one dependency edge through the normal write path, so
// is_blocked is maintained exactly as it would be in production.
func addStatusDriftDep(ctx context.Context, t *testing.T, store *DoltStore, src, tgt string, typ types.DependencyType) {
	t.Helper()
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
		t.Fatalf("add dep %s -> %s (%s): %v", src, tgt, typ, err)
	}
}

// markStatusBlocked sets the manual status enum directly, mirroring how the
// fleet-wide victims in be-ntbxt got there (types.StatusBlocked is a manual
// value that nothing auto-clears).
func markStatusBlocked(ctx context.Context, t *testing.T, store *DoltStore, table, id string) {
	t.Helper()
	//nolint:gosec // G201: table is a hardcoded "issues" or "wisps" from the callers below.
	if _, err := store.db.ExecContext(ctx, "UPDATE "+table+" SET status = 'blocked' WHERE id = ?", id); err != nil {
		t.Fatalf("seed status=blocked on %s.%s: %v", table, id, err)
	}
}

// TestStatusBlockedDrift_GraphBlockedRowIsNeverFlaggedOrTouched extends
// TestStatusBlockedDrift_OpenBlockerIsNeverFlaggedOrTouched to every OTHER
// reason the dependency graph holds a row blocked. "Blocked" is defined once,
// by shouldBeBlockedIDsUnionSQL in blocked_consistency.go — the same predicate
// is_blocked itself is derived from — and it counts conditional-blocks, an
// inherited block through parent-child, and a held waits-for gate alongside
// plain 'blocks'. A drift predicate that knows only 'blocks' reports each of
// these as drift and force-opens a row the graph still holds blocked: the
// exact regression the open-blocker test guards, one edge type over.
//
// Every case asserts is_blocked = 1 first, so a subtest that stops failing
// because its seeding stopped producing a blocked row fails loudly instead of
// passing vacuously.
func TestStatusBlockedDrift_GraphBlockedRowIsNeverFlaggedOrTouched(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		seed    func(ctx context.Context, t *testing.T, store *DoltStore)
	}{
		{
			// A conditional-blocks edge to a still-open target.
			name:    "conditional-blocks on an open target",
			subject: "sbg-cb-src",
			seed: func(ctx context.Context, t *testing.T, store *DoltStore) {
				createPerm(t, ctx, store, "sbg-cb-tgt")
				createPerm(t, ctx, store, "sbg-cb-src")
				addStatusDriftDep(ctx, t, store, "sbg-cb-src", "sbg-cb-tgt", types.DepConditionalBlocks)
			},
		},
		{
			// A child inheriting its parent's block: the child itself has no
			// blocking edge of its own, only the parent-child edge upward.
			name:    "parent-child under a blocked parent",
			subject: "sbg-pc-child",
			seed: func(ctx context.Context, t *testing.T, store *DoltStore) {
				createPerm(t, ctx, store, "sbg-pc-blocker")
				createPerm(t, ctx, store, "sbg-pc-parent")
				createPerm(t, ctx, store, "sbg-pc-child")
				addStatusDriftDep(ctx, t, store, "sbg-pc-parent", "sbg-pc-blocker", types.DepBlocks)
				addStatusDriftDep(ctx, t, store, "sbg-pc-child", "sbg-pc-parent", types.DepParentChild)
			},
		},
		{
			// A waits-for gate held open by an active child of the spawner —
			// the waiter has no blocks/conditional-blocks edge at all.
			name:    "waits-for gate held by an active child",
			subject: "sbg-wf-waiter",
			seed: func(ctx context.Context, t *testing.T, store *DoltStore) {
				createPerm(t, ctx, store, "sbg-wf-spawner")
				createPerm(t, ctx, store, "sbg-wf-child")
				createPerm(t, ctx, store, "sbg-wf-waiter")
				addStatusDriftDep(ctx, t, store, "sbg-wf-child", "sbg-wf-spawner", types.DepParentChild)
				addStatusDriftDep(ctx, t, store, "sbg-wf-waiter", "sbg-wf-spawner", types.DepWaitsFor)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()

			tc.seed(ctx, t, store)

			// The graph's own verdict, before status is touched at all. If
			// this is false the case is not testing what it claims to.
			if !getIsBlocked(t, ctx, store, "issues", tc.subject) {
				t.Fatalf("precondition: %s must be is_blocked=1 — the graph holds it blocked", tc.subject)
			}
			markStatusBlocked(ctx, t, store, "issues", tc.subject)

			if n := countStatusDrift(ctx, t, store.db); n != 0 {
				t.Fatalf("%s is still blocked by the graph: want 0 drifted rows, got %d", tc.subject, n)
			}
			if changed := fixStatusDrift(ctx, t, store.db); changed != 0 {
				t.Fatalf("fix touched a graph-blocked row: want 0 corrected, got %d", changed)
			}
			if got := statusOf(ctx, t, store.db, tc.subject); got != "blocked" {
				t.Fatalf("after fix: %s status = %q, want still blocked", tc.subject, got)
			}
			if !getIsBlocked(t, ctx, store, "issues", tc.subject) {
				t.Fatalf("after fix: %s must still read is_blocked=1", tc.subject)
			}
		})
	}
}

// statusOfIn is statusOf for either row table, so the wisps-side pass of
// CountStatusBlockedDriftInTx/FixStatusBlockedDriftInTx can be asserted the
// same way as the issues-side one.
func statusOfIn(ctx context.Context, t *testing.T, db *sql.DB, table, id string) string {
	t.Helper()
	var status string
	//nolint:gosec // G201: table is a hardcoded "issues" or "wisps" from the callers below.
	if err := db.QueryRowContext(ctx, "SELECT status FROM "+table+" WHERE id = ?", id).Scan(&status); err != nil {
		t.Fatalf("read status of %s.%s: %v", table, id, err)
	}
	return status
}

// TestStatusBlockedDrift_WispBlockerIsHonouredUntilItCloses pins the wisp arm
// of the membership union — the leg that joins a dependency row's
// depends_on_wisp_id to the wisps table. A durable issue can be blocked by an
// ephemeral one, and that edge lives in 'dependencies' with depends_on_issue_id
// NULL, so the issues-to-issues leg alone cannot see it.
//
// Both halves matter and they fail in opposite directions: drop the wisp leg
// and the open-wisp case below reports drift on a legitimately blocked row;
// keep it but stop checking the wisp's status and the closed-wisp case stops
// reporting real drift. Before this test the whole leg could be deleted with
// every status-drift test still green.
func TestStatusBlockedDrift_WispBlockerIsHonouredUntilItCloses(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "sbw-src")
	createWisp(t, ctx, store, "sbw-wisp")
	addStatusDriftDep(ctx, t, store, "sbw-src", "sbw-wisp", types.DepBlocks)

	// The edge really is the wisp-target shape this leg exists for: the
	// dependency row carries depends_on_wisp_id, not depends_on_issue_id.
	var wispTargets int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_wisp_id = ?",
		"sbw-src", "sbw-wisp").Scan(&wispTargets); err != nil {
		t.Fatalf("count wisp-target dependency rows: %v", err)
	}
	if wispTargets != 1 {
		t.Fatalf("precondition: want 1 dependencies row with depends_on_wisp_id=sbw-wisp, got %d", wispTargets)
	}
	if !getIsBlocked(t, ctx, store, "issues", "sbw-src") {
		t.Fatal("precondition: sbw-src must be is_blocked=1 — blocked by the open wisp")
	}

	markStatusBlocked(ctx, t, store, "issues", "sbw-src")

	// Open wisp blocker: legitimately blocked, never drift.
	if n := countStatusDrift(ctx, t, store.db); n != 0 {
		t.Fatalf("wisp blocker still open: want 0 drifted rows, got %d", n)
	}
	if changed := fixStatusDrift(ctx, t, store.db); changed != 0 {
		t.Fatalf("fix touched a row blocked by an open wisp: want 0 corrected, got %d", changed)
	}
	if got := statusOf(ctx, t, store.db, "sbw-src"); got != "blocked" {
		t.Fatalf("after fix: sbw-src status = %q, want still blocked", got)
	}

	// Close the wisp — now nothing blocks sbw-src and the status is drift.
	if _, err := store.db.ExecContext(ctx, "UPDATE wisps SET status = 'closed' WHERE id = ?", "sbw-wisp"); err != nil {
		t.Fatalf("close wisp blocker: %v", err)
	}
	if n := countStatusDrift(ctx, t, store.db); n != 1 {
		t.Fatalf("after wisp blocker closed: want 1 drifted row, got %d", n)
	}
	if changed := fixStatusDrift(ctx, t, store.db); changed != 1 {
		t.Fatalf("fix: want 1 row corrected, got %d", changed)
	}
	if got := statusOf(ctx, t, store.db, "sbw-src"); got != "open" {
		t.Fatalf("after fix: sbw-src status = %q, want open", got)
	}
}

// TestStatusBlockedDrift_WispRowIsFlaggedAndFixed covers the other wisp-shaped
// gap: the drifted row in the 'wisps' table itself. CountStatusBlockedDriftInTx
// and FixStatusBlockedDriftInTx each make a second pass over
// (wisps, wisp_dependencies, wisp_events); before this test that pass could
// return nothing at all and every other status-drift test stayed green.
func TestStatusBlockedDrift_WispRowIsFlaggedAndFixed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createWisp(t, ctx, store, "sbwr-w")
	markStatusBlocked(ctx, t, store, "wisps", "sbwr-w")

	if n := countStatusDrift(ctx, t, store.db); n != 1 {
		t.Fatalf("wisp row with no blocker: want 1 drifted row, got %d", n)
	}
	if changed := fixStatusDrift(ctx, t, store.db); changed != 1 {
		t.Fatalf("fix: want 1 wisp row corrected, got %d", changed)
	}
	if got := statusOfIn(ctx, t, store.db, "wisps", "sbwr-w"); got != "open" {
		t.Fatalf("after fix: wisps.sbwr-w status = %q, want open", got)
	}
	if n := countStatusDrift(ctx, t, store.db); n != 0 {
		t.Fatalf("after fix: want 0 drifted rows, got %d", n)
	}
}
