package dolt

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func seedClaimedWisp(t *testing.T, ctx context.Context, store *DoltStore, id, owner string, ttl time.Duration) {
	t.Helper()
	wisp := &types.Issue{
		ID:        id,
		Title:     "tier " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "seeder"); err != nil {
		t.Fatalf("seed wisp %s: %v", id, err)
	}
	claimCtx := issueops.WithLeaseTTL(ctx, ttl)
	if err := store.ClaimIssue(claimCtx, id, owner); err != nil {
		t.Fatalf("claim wisp %s: %v", id, err)
	}
}

func readWispLease(t *testing.T, ctx context.Context, store *DoltStore, id string) (status string, assignee string, leaseValid bool, fence int64) {
	t.Helper()
	var lease timeNull
	err := store.db.QueryRowContext(ctx, `
		SELECT status, COALESCE(assignee,''), lease_expires_at, claim_fence FROM wisps WHERE id = ?
	`, id).Scan(&status, &assignee, &lease, &fence)
	if err != nil {
		t.Fatalf("read wisp lease %s: %v", id, err)
	}
	return status, assignee, lease.Valid, fence
}

// TestWispLeaseLifecycle: leases are tier-complete for requested leases — a
// wisp-table row (Gas City's durable no_history workflow tier lives there)
// claims with a lease, renews it via heartbeat, and is reclaimed on expiry
// with the recovery event in wisp_events and the tier reported.
func TestWispLeaseLifecycle(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedWisp(t, ctx, store, "tier-wisp", "alice", time.Second)
	_, _, leased, fenceBefore := readWispLease(t, ctx, store, "tier-wisp")
	if !leased {
		t.Fatal("explicit-TTL wisp claim did not stamp a lease")
	}

	// The owner renews — the store-level wisp rejection is gone. Renew with
	// the same short TTL so the expiry below stays in test range.
	if err := store.HeartbeatIssue(issueops.WithLeaseTTL(ctx, time.Second), "tier-wisp", "alice"); err != nil {
		t.Fatalf("wisp heartbeat: %v", err)
	}

	// Expire and reclaim: the wisp row reverts to ready, the fence bumps
	// (zombie fenced out), and the tier is reported.
	time.Sleep(4 * time.Second)
	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("reclaimed %d, want 1 (the expired wisp lease)", len(reclaimed))
	}
	r := reclaimed[0]
	if r.ID != "tier-wisp" || r.PreviousOwner != "alice" {
		t.Errorf("reclaimed %+v, want tier-wisp/alice", r)
	}
	if r.Tier != "wisps" {
		t.Errorf("reclaimed tier = %q, want wisps", r.Tier)
	}

	status, assignee, leasedAfter, fenceAfter := readWispLease(t, ctx, store, "tier-wisp")
	if status != "open" || assignee != "" || leasedAfter {
		t.Errorf("post-reclaim wisp: status=%q assignee=%q leased=%v, want open/unassigned/unleased", status, assignee, leasedAfter)
	}
	if fenceAfter != fenceBefore+1 {
		t.Errorf("reclaim fence %d → %d, want bump by one", fenceBefore, fenceAfter)
	}

	// The recovery event landed in wisp_events (wisp rows have no rows in
	// the permanent events table).
	var n int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wisp_events WHERE issue_id = ? AND event_type = ?`,
		"tier-wisp", string(types.EventLeaseReclaimed)).Scan(&n); err != nil {
		t.Fatalf("count wisp_events: %v", err)
	}
	if n != 1 {
		t.Errorf("wisp_events lease_reclaimed rows = %d, want 1", n)
	}
}

// TestReclaimSweepsBothTiersAndReportsTier: one reclaim pass covers issues
// and wisps; each result carries its tier and fence.
func TestReclaimSweepsBothTiersAndReportsTier(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "tier-issue", "bob", time.Second)
	seedClaimedWisp(t, ctx, store, "tier-wisp2", "carol", time.Second)
	time.Sleep(4 * time.Second)

	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 2 {
		t.Fatalf("reclaimed %d, want 2 (one per tier)", len(reclaimed))
	}
	tiers := map[string]string{}
	for _, r := range reclaimed {
		tiers[r.ID] = r.Tier
		if r.Fence == 0 {
			t.Errorf("reclaimed %s carries fence 0, want the post-bump fence", r.ID)
		}
	}
	if tiers["tier-issue"] != "issues" || tiers["tier-wisp2"] != "wisps" {
		t.Errorf("tiers = %v, want tier-issue→issues, tier-wisp2→wisps", tiers)
	}
}

// TestUnleasedWispInvisibleToReclaim: a wisp claimed WITHOUT a lease request
// on a disarmed store stays invisible to the reaper — tier-completeness does
// not resurrect the auto-lease exposure.
func TestUnleasedWispInvisibleToReclaim(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.LeaseAutoConfigKey, "off"); err != nil {
		t.Fatalf("disarm: %v", err)
	}
	wisp := &types.Issue{ID: "tier-unleased", Title: "unleased", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, wisp, "seeder"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.ClaimIssue(ctx, "tier-unleased", "dave"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Errorf("reclaim reaped %d unleased wisp claims, want 0", len(reclaimed))
	}
}

// timeNull is a minimal nullable-time scanner for direct assertions.
type timeNull struct {
	Time  time.Time
	Valid bool
}

func (n *timeNull) Scan(v interface{}) error {
	if v == nil {
		n.Valid = false
		return nil
	}
	if t, ok := v.(time.Time); ok {
		n.Time, n.Valid = t, true
	}
	return nil
}

// TestDisarmClearsLegacyWispLeases guards the upgrade transition: shipped
// binaries stamped a lease on every wisp claim (auto-on default) that no
// shipped path could renew or reclaim, so an upgrading auto-on store holds
// in_progress wisp rows with stale leases. Now that reclaim sweeps wisps,
// those would be mass-reclaimable — `bd lease disarm` is the interlock: it
// clears armed leases on BOTH tiers, so a store disarmed before the first
// post-upgrade reclaim has nothing stale to reclaim.
func TestDisarmClearsLegacyWispLeases(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Simulate a legacy armed wisp claim: auto-on default stamps a lease.
	wisp := &types.Issue{ID: "legacy-wisp", Title: "legacy", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, wisp, "seeder"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.ClaimIssue(ctx, "legacy-wisp", "alice"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, _, leased, _ := readWispLease(t, ctx, store, "legacy-wisp"); !leased {
		t.Fatal("auto-on wisp claim did not stamp a lease (legacy precondition)")
	}

	n, err := store.DisarmAutoLeases(ctx)
	if err != nil {
		t.Fatalf("disarm: %v", err)
	}
	if n < 1 {
		t.Errorf("disarm cleared %d leases, want >=1 (the wisp)", n)
	}

	// The wisp is no longer armed, and reclaim finds nothing to sweep.
	status, assignee, leased, _ := readWispLease(t, ctx, store, "legacy-wisp")
	if leased {
		t.Error("wisp still armed after disarm")
	}
	if status != "in_progress" || assignee != "alice" {
		t.Errorf("disarm disturbed ownership: status=%q assignee=%q", status, assignee)
	}
	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	for _, r := range reclaimed {
		if r.ID == "legacy-wisp" {
			t.Error("disarmed legacy wisp was still reclaimed")
		}
	}
}
