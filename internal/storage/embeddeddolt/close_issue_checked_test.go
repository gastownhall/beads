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
