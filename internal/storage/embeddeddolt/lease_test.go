//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestLeaseLifecycleEmbedded confirms the lease columns exist in the embedded
// backend and that claim → heartbeat → reclaim are wired through EmbeddedDoltStore.
func TestLeaseLifecycleEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "lease")
	ctx := t.Context()

	issue := &types.Issue{
		ID:        "lease-1",
		Title:     "lease lifecycle",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, issue, "seeder"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Claim with a 1s lease so it expires within the test.
	claimCtx := issueops.WithLeaseTTL(ctx, time.Second)
	if err := te.store.ClaimIssue(claimCtx, "lease-1", "alice"); err != nil {
		t.Fatalf("ClaimIssue: %v", err)
	}

	// The claim stamped a non-zero row_lock and a lease.
	var rowLock int64
	te.queryScalar(t, ctx, "SELECT row_lock FROM issues WHERE id = ?", []any{"lease-1"}, &rowLock)
	if rowLock == 0 {
		t.Error("row_lock = 0 after claim, want non-zero")
	}

	// Owner can heartbeat; a stranger cannot. Heartbeat with the same short TTL
	// so the lease still expires within the test (a default-TTL heartbeat would
	// push expiry minutes out).
	if err := te.store.HeartbeatIssue(claimCtx, "lease-1", "alice"); err != nil {
		t.Fatalf("owner HeartbeatIssue: %v", err)
	}
	if err := te.store.HeartbeatIssue(ctx, "lease-1", "mallory"); !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Errorf("stranger heartbeat err = %v, want ErrAlreadyClaimed", err)
	}

	// Let the lease expire, then reclaim it.
	time.Sleep(2500 * time.Millisecond)
	reclaimed, err := te.store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("ReclaimExpiredLeases: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "lease-1" || reclaimed[0].PreviousOwner != "alice" {
		t.Fatalf("reclaimed = %+v, want [{lease-1 alice}]", reclaimed)
	}

	got, err := te.store.GetIssue(ctx, "lease-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Status != types.StatusOpen {
		t.Errorf("status = %q after reclaim, want open", got.Status)
	}
	if got.Assignee != "" {
		t.Errorf("assignee = %q after reclaim, want empty", got.Assignee)
	}
}

func TestHeartbeatRejectsWispEmbedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "lease_wisp")
	ctx := t.Context()

	wisp := &types.Issue{
		ID:        "lease-wisp-1",
		Title:     "ephemeral work",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := te.store.CreateIssue(ctx, wisp, "seeder"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}
	if err := te.store.HeartbeatIssue(ctx, "lease-wisp-1", "alice"); !errors.Is(err, storage.ErrNotClaimable) {
		t.Fatalf("wisp heartbeat err = %v, want ErrNotClaimable", err)
	}
}
