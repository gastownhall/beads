//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// wakeCommitCount reads how many defer-wake commits dolt_log holds — the
// sweep's own commit, named WakeDefersCommitMessage, so its count is the
// direct observable of "the sweep ran and wrote".
func (te *testEnv) wakeCommitCount(t *testing.T) int {
	t.Helper()
	var count int
	te.queryScalar(t, t.Context(),
		"SELECT COUNT(*) FROM dolt_log WHERE message = ?", []any{issueops.WakeDefersCommitMessage(1)}, &count)
	return count
}

// TestEmbeddedDeferWakeSweepSwitch pins the per-store sweep gate (#7634):
// with SetDeferWakeSweepEnabled(false), every ready-work entry point — the
// ready read, the count read, and the advisory sweep itself — leaves a
// committed expired deferral exactly as it stands: still deferred, defer_until
// intact, and no defer-wake commit in dolt_log. Re-enabled, the next ready
// read wakes the defer once, byte-identical to the unfrozen contract.
//
// The fixture holds no other ready issue, so the count read and the ready
// read observe this defer and nothing else. This tests sweep suppression
// only, never the CLI's --claim safety: the claim path is an intentional
// write with its own sweep, out of the freeze's scope.
func TestEmbeddedDeferWakeSweepSwitch(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "dwsg")
	ctx := t.Context()

	issue := &types.Issue{
		Title:     "switch gate probe",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	// A committed expired deferral: dated, in the past, and landed as a Dolt
	// commit so the sweep's no-op below cannot be confused with uncommitted
	// state.
	te.exec(t, ctx, "UPDATE issues SET status = 'deferred', defer_until = '2020-01-01 00:00:00' WHERE id = ?", issue.ID)
	if _, err := te.store.CommitPending(ctx, "seed expired defer"); err != nil {
		t.Fatalf("CommitPending seed: %v", err)
	}

	assertAsleep := func(read string) {
		t.Helper()
		got, err := te.store.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("GetIssue after %s: %v", read, err)
		}
		if got.Status != types.StatusDeferred {
			t.Errorf("%s woke %s: status = %q, want deferred", read, issue.ID, got.Status)
		}
		if got.DeferUntil == nil {
			t.Errorf("%s cleared %s's defer_until, want the committed date retained", read, issue.ID)
		}
		if n := te.wakeCommitCount(t); n != 0 {
			t.Errorf("%s ran the sweep anyway: %d defer-wake commit(s) in dolt_log, want 0", read, n)
		}
	}

	te.store.SetDeferWakeSweepEnabled(false)

	// The ready read stands down.
	ready, err := te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork (disabled): %v", err)
	}
	for _, w := range ready {
		if w.ID == issue.ID {
			t.Errorf("disabled ready read surfaced expired defer %s", issue.ID)
		}
	}
	assertAsleep("disabled ready read")

	// The count read stands down.
	n, err := te.store.CountReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("CountReadyWork (disabled): %v", err)
	}
	if n != 0 {
		t.Errorf("disabled count read counted %d ready issue(s), want 0 (the only issue is the expired defer)", n)
	}
	assertAsleep("disabled count read")

	// The advisory sweep itself stands down.
	te.store.WakeExpiredDefersAdvisory(ctx)
	assertAsleep("disabled advisory sweep")

	// Re-enabled, the next ready read wakes the defer exactly once — the
	// gate suppresses nothing once lifted, and the frozen period left no
	// half-state behind.
	te.store.SetDeferWakeSweepEnabled(true)
	ready, err = te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork (re-enabled): %v", err)
	}
	var woke bool
	for _, w := range ready {
		if w.ID == issue.ID {
			woke = true
		}
	}
	if !woke {
		t.Errorf("re-enabled ready read did not surface expired defer %s", issue.ID)
	}
	got, err := te.store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue (re-enabled): %v", err)
	}
	if got.Status != types.StatusOpen || got.DeferUntil != nil {
		t.Errorf("re-enabled wake left %s status = %q, defer_until = %v, want open and cleared", issue.ID, got.Status, got.DeferUntil)
	}
	if n := te.wakeCommitCount(t); n != 1 {
		t.Errorf("re-enabled sweep wrote %d defer-wake commit(s), want exactly 1", n)
	}
}
