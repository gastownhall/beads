//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedCloseIssueChecked exercises the guarded close through the
// EmbeddedDoltStore's withConn wrapper: a blocked issue is refused with
// storage.ErrCloseBlocked and the transaction rolls back (issue stays open, no
// closed event), while Force bypasses the guard and closes. The SQL guard/close
// core is the shared issueops.CloseIssueCheckedInTx already covered against a
// real engine by the dolt package; this test proves the embedded wrapper wires
// it up and rolls back correctly.
func TestEmbeddedCloseIssueChecked(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cic")
	ctx := t.Context()

	for _, id := range []string{"cic-blocker", "cic-target"} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{
		IssueID: "cic-target", DependsOnID: "cic-blocker", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if blocked, _, err := te.store.IsBlocked(ctx, "cic-target"); err != nil || !blocked {
		t.Fatalf("cic-target should be blocked (blocked=%v err=%v)", blocked, err)
	}

	// Blocked without force: refuse atomically — no close, no closed event.
	res, err := te.store.CloseIssueChecked(ctx, "cic-target", "tester", storage.CloseIssueOptions{Reason: "done"})
	if !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("blocked close err = %v, want errors.Is(_, ErrCloseBlocked)", err)
	}
	if res.Unchanged {
		t.Fatalf("res.Unchanged = true on refusal, want false")
	}
	iss, err := te.store.GetIssue(ctx, "cic-target")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if iss.Status == types.StatusClosed {
		t.Fatalf("cic-target closed after refusal; withConn did not roll back")
	}
	events, err := te.store.GetEvents(ctx, "cic-target", 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	for _, e := range events {
		if e.EventType == types.EventClosed {
			t.Fatalf("closed event recorded despite refusal (tx must roll back)")
		}
	}

	// Force bypasses the guard and closes.
	res, err = te.store.CloseIssueChecked(ctx, "cic-target", "tester", storage.CloseIssueOptions{Reason: "done", Force: true})
	if err != nil {
		t.Fatalf("force close err = %v, want nil", err)
	}
	if res.Unchanged {
		t.Fatalf("res.Unchanged = true on force close of open issue, want false")
	}
	iss, err = te.store.GetIssue(ctx, "cic-target")
	if err != nil {
		t.Fatalf("GetIssue after force: %v", err)
	}
	if iss.Status != types.StatusClosed {
		t.Fatalf("cic-target status = %q after force, want closed", iss.Status)
	}
}

// TestEmbeddedCloseIssueCheckedTransitiveBlockedCloses proves the guard refuses
// only on a LIVE direct blocker (blocked && len(blockers) > 0): a transitively-
// blocked child (parent-child of a blocked parent) has is_blocked=1 but no direct
// blocker of its own, so it closes without Force — the historical `bd close`
// behavior, threaded through the embedded wrapper.
func TestEmbeddedCloseIssueCheckedTransitiveBlockedCloses(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "cictr")
	ctx := t.Context()

	for _, id := range []string{"cictr-blocker", "cictr-parent", "cictr-child"} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{
		IssueID: "cictr-parent", DependsOnID: "cictr-blocker", Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency(parent blocks blocker): %v", err)
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{
		IssueID: "cictr-child", DependsOnID: "cictr-parent", Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency(parent-child): %v", err)
	}
	// The child inherits is_blocked=1 transitively, with NO direct blocker.
	blocked, blockers, err := te.store.IsBlocked(ctx, "cictr-child")
	if err != nil {
		t.Fatalf("IsBlocked(child): %v", err)
	}
	if !blocked {
		t.Fatal("cictr-child should inherit is_blocked=1 from its blocked parent")
	}
	if len(blockers) != 0 {
		t.Fatalf("cictr-child should have NO direct blocker, got %v", blockers)
	}

	// No Force: the guard sees no live direct blocker and closes.
	res, err := te.store.CloseIssueChecked(ctx, "cictr-child", "tester", storage.CloseIssueOptions{Reason: "done"})
	if err != nil {
		t.Fatalf("transitive-blocked close err = %v, want nil (no live direct blocker)", err)
	}
	if res.Unchanged {
		t.Fatalf("res.Unchanged = true, want false (a real close)")
	}
	if iss, _ := te.store.GetIssue(ctx, "cictr-child"); iss.Status != types.StatusClosed {
		t.Fatalf("cictr-child status = %q, want closed", iss.Status)
	}
}

// TestEmbeddedCloseIssueCheckedVersionCAS proves the ExpectedVersion CAS wires
// through the EmbeddedDoltStore's withConn wrapper: a matching version closes, a
// stale version is refused atomically with storage.ErrVersionMismatch (issue
// stays open, no closed event) even under Force, and a nil ExpectedVersion is
// unchanged behavior. The compare-and-swap core is the shared
// issueops.CloseIssueCheckedInTx/CheckVersionInTx already covered against a real
// engine by the dolt package; this test proves the embedded wrapper threads it
// and rolls back.
func TestEmbeddedCloseIssueCheckedVersionCAS(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "casv")
	ctx := t.Context()
	ptr := func(v int64) *int64 { return &v }

	create := func(id string) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	rowVersion := func(id string) int64 {
		iss, err := te.store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		return iss.RowVersion
	}

	// Matching version closes.
	create("casv-match")
	res, err := te.store.CloseIssueChecked(ctx, "casv-match", "tester",
		storage.CloseIssueOptions{Reason: "done", ExpectedVersion: ptr(rowVersion("casv-match"))})
	if err != nil {
		t.Fatalf("matching-version close err = %v, want nil", err)
	}
	if res.Unchanged {
		t.Fatalf("res.Unchanged = true, want false (a real close)")
	}
	if iss, _ := te.store.GetIssue(ctx, "casv-match"); iss.Status != types.StatusClosed {
		t.Fatalf("casv-match status = %q, want closed", iss.Status)
	}

	// Stale version refuses atomically, even with Force.
	create("casv-stale")
	stale := rowVersion("casv-stale") + 1
	res, err = te.store.CloseIssueChecked(ctx, "casv-stale", "tester",
		storage.CloseIssueOptions{Reason: "done", Force: true, ExpectedVersion: ptr(stale)})
	if !errors.Is(err, storage.ErrVersionMismatch) {
		t.Fatalf("stale close err = %v, want errors.Is(_, ErrVersionMismatch) even with Force", err)
	}
	if res.Unchanged {
		t.Fatalf("res.Unchanged = true on mismatch, want false")
	}
	iss, err := te.store.GetIssue(ctx, "casv-stale")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if iss.Status == types.StatusClosed {
		t.Fatalf("casv-stale closed after refusal; withConn did not roll back")
	}
	events, err := te.store.GetEvents(ctx, "casv-stale", 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	for _, e := range events {
		if e.EventType == types.EventClosed {
			t.Fatalf("closed event recorded despite version mismatch (tx must roll back)")
		}
	}

	// nil ExpectedVersion is unchanged behavior.
	res, err = te.store.CloseIssueChecked(ctx, "casv-stale", "tester",
		storage.CloseIssueOptions{Reason: "done", ExpectedVersion: nil})
	if err != nil {
		t.Fatalf("nil-version close err = %v, want nil (back-compat)", err)
	}
	if res.Unchanged {
		t.Fatalf("res.Unchanged = true on nil-version close of open issue, want false")
	}
	if iss, _ := te.store.GetIssue(ctx, "casv-stale"); iss.Status != types.StatusClosed {
		t.Fatalf("casv-stale status = %q after nil-version close, want closed", iss.Status)
	}
}
