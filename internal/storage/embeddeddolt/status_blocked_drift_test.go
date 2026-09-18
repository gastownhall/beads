//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// statusOf reads an issue's status column directly, mirroring the dolt
// package's status_blocked_test.go helper of the same name.
func (te *testEnv) statusOf(t *testing.T, ctx context.Context, id string) string {
	t.Helper()
	var status string
	te.queryScalar(t, ctx, "SELECT status FROM issues WHERE id = ?", []any{id}, &status)
	return status
}

// TestEmbeddedStatusBlockedDriftWiring confirms the embedded store satisfies
// the cross-mode StatusBlockedDriftRecomputer capability and exercises the
// be-ntbxt bug's core repro: a bead manually left status='blocked' whose only
// blocker has since closed. Before this store implemented the interface,
// 'bd recompute-blocked --status' failed in embedded mode with "storage
// backend does not support status=blocked drift check" — the type assertion
// below is the exact point that failed.
func TestEmbeddedStatusBlockedDriftWiring(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "sbd")
	ctx := t.Context()

	for _, id := range []string{"sbd-w", "sbd-x"} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "sbd-w", DependsOnID: "sbd-x", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	// Manually mark status='blocked', mirroring how the fleet-wide victims the
	// bug report found got there (types.StatusBlocked is a manual enum value,
	// never auto-cleared).
	te.exec(t, ctx, "UPDATE issues SET status = 'blocked' WHERE id = ?", "sbd-w")

	rc, ok := storage.UnwrapStore(te.store).(storage.StatusBlockedDriftRecomputer)
	if !ok {
		t.Fatal("embedded store must implement storage.StatusBlockedDriftRecomputer")
	}

	// Blocker still open: not drift yet, must not be flagged.
	if n, err := rc.CountStatusBlockedDrift(ctx); err != nil || n != 0 {
		t.Fatalf("blocker still open: want 0 drifted rows, got n=%d err=%v", n, err)
	}

	// Close the blocker — the exact 'bd close' path whose "Newly unblocked"
	// message lied about the transition it claimed before this fix.
	te.exec(t, ctx, "UPDATE issues SET status = 'closed' WHERE id = ?", "sbd-x")

	if got := te.statusOf(t, ctx, "sbd-w"); got != "blocked" {
		t.Fatalf("setup: sbd-w must still read status=blocked before the fix (the drift IS the bug), got %q", got)
	}
	n, err := rc.CountStatusBlockedDrift(ctx)
	if err != nil {
		t.Fatalf("CountStatusBlockedDrift: %v", err)
	}
	if n != 1 {
		t.Fatalf("after blocker closed: want 1 drifted row, got %d", n)
	}

	changed, err := rc.FixStatusBlockedDrift(ctx)
	if err != nil {
		t.Fatalf("FixStatusBlockedDrift: %v", err)
	}
	if changed != 1 {
		t.Fatalf("fix: want 1 row corrected, got %d", changed)
	}
	if got := te.statusOf(t, ctx, "sbd-w"); got != "open" {
		t.Fatalf("after fix: sbd-w status = %q, want open", got)
	}
	if n, err := rc.CountStatusBlockedDrift(ctx); err != nil || n != 0 {
		t.Fatalf("after fix: want 0 drifted rows, got n=%d err=%v", n, err)
	}
	if again, err := rc.FixStatusBlockedDrift(ctx); err != nil || again != 0 {
		t.Fatalf("fix must be idempotent: want 0 corrected on second run, got again=%d err=%v", again, err)
	}
}

// TestEmbeddedStatusBlockedDriftOpenBlockerNeverFlagged is the negative case
// the exit contract calls out explicitly (be-ntbxt): a bead legitimately
// blocked on a still-open blocker must never be flagged or fixed by either
// store method. Getting this wrong would force-open beads that are genuinely
// not ready to work — as important a regression to guard as detecting the
// drift in the first place.
func TestEmbeddedStatusBlockedDriftOpenBlockerNeverFlagged(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "sbdo")
	ctx := t.Context()

	for _, id := range []string{"sbdo-w", "sbdo-x"} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "sbdo-w", DependsOnID: "sbdo-x", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	te.exec(t, ctx, "UPDATE issues SET status = 'blocked' WHERE id = ?", "sbdo-w")

	rc, ok := storage.UnwrapStore(te.store).(storage.StatusBlockedDriftRecomputer)
	if !ok {
		t.Fatal("embedded store must implement storage.StatusBlockedDriftRecomputer")
	}

	if n, err := rc.CountStatusBlockedDrift(ctx); err != nil || n != 0 {
		t.Fatalf("blocker still open: want 0 drifted rows, got n=%d err=%v", n, err)
	}
	changed, err := rc.FixStatusBlockedDrift(ctx)
	if err != nil {
		t.Fatalf("FixStatusBlockedDrift: %v", err)
	}
	if changed != 0 {
		t.Fatalf("fix must not touch a legitimately blocked row: want 0 corrected, got %d", changed)
	}
	if got := te.statusOf(t, ctx, "sbdo-w"); got != "blocked" {
		t.Fatalf("fix must not touch a legitimately blocked row: status = %q, want still blocked", got)
	}
}
