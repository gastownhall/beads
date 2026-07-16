package dolt

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

func claimWithFence(t *testing.T, ctx context.Context, store *DoltStore, id, owner string) int64 {
	t.Helper()
	seedOpenIssue(t, ctx, store, id)
	if err := store.ClaimIssue(issueops.WithLeaseTTL(ctx, time.Second), id, owner); err != nil {
		t.Fatalf("claim %s: %v", id, err)
	}
	return readFenceState(t, ctx, store, id).fence
}

// TestRenewLeasesBatchOutcomes: the orchestrator renews confirmed-live claims
// in one call keyed on (id, fence); every ref gets a typed outcome, and a
// superseded fence is reported lost rather than silently renewed.
func TestRenewLeasesBatchOutcomes(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	fLive := claimWithFence(t, ctx, store, "renew-live", "alice")
	fStale := claimWithFence(t, ctx, store, "renew-stale", "bob")
	// bob's claim is reclaimed + re-owned, superseding the fence the caller holds.
	seedClaimedIssue(t, ctx, store, "renew-unleased", "carol", time.Hour) // has a lease via auto-on default
	fUnleasedRow := readFenceState(t, ctx, store, "renew-unleased").fence

	// Supersede renew-stale's fence out from under the caller.
	if err := store.UnclaimIssue(ctx, "renew-stale", "bob", false); err != nil {
		t.Fatalf("supersede unclaim: %v", err)
	}

	refs := []storage.LeaseRef{
		{ID: "renew-live", Fence: fLive},
		{ID: "renew-stale", Fence: fStale},          // fence now superseded
		{ID: "renew-missing", Fence: 0},             // no such row
		{ID: "renew-unleased", Fence: fUnleasedRow}, // leased (auto-on), should renew
	}
	res, err := store.RenewLeases(ctx, refs, 30*time.Second)
	if err != nil {
		t.Fatalf("RenewLeases: %v", err)
	}
	got := map[string]storage.LeaseRenewalOutcome{}
	for _, r := range res {
		got[r.ID] = r.Outcome
	}
	if got["renew-live"] != storage.LeaseRenewed {
		t.Errorf("renew-live outcome = %v, want renewed", got["renew-live"])
	}
	if got["renew-stale"] != storage.LeaseRenewalLost {
		t.Errorf("renew-stale outcome = %v, want lost (fence superseded)", got["renew-stale"])
	}
	if got["renew-missing"] != storage.LeaseRenewalNotFound {
		t.Errorf("renew-missing outcome = %v, want not-found", got["renew-missing"])
	}
	if got["renew-unleased"] != storage.LeaseRenewed {
		t.Errorf("renew-unleased outcome = %v, want renewed", got["renew-unleased"])
	}

	// The live lease actually moved forward: it survives a reclaim of leases
	// expired more than a moment ago.
	time.Sleep(1200 * time.Millisecond)
	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	for _, r := range reclaimed {
		if r.ID == "renew-live" {
			t.Error("renew-live was reclaimed despite a fresh 30s renewal")
		}
	}
}

// TestRenewLeasesUnleasedIsTyped: renewing a claim that carries no lease
// (disarmed store) reports unleased, never arms one.
func TestRenewLeasesUnleasedIsTyped(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.LeaseAutoConfigKey, "off"); err != nil {
		t.Fatalf("disarm: %v", err)
	}
	seedOpenIssue(t, ctx, store, "renew-noneed")
	if err := store.ClaimIssue(ctx, "renew-noneed", "dave"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	fence := readFenceState(t, ctx, store, "renew-noneed").fence

	res, err := store.RenewLeases(ctx, []storage.LeaseRef{{ID: "renew-noneed", Fence: fence}}, time.Minute)
	if err != nil {
		t.Fatalf("RenewLeases: %v", err)
	}
	if len(res) != 1 || res[0].Outcome != storage.LeaseRenewalUnleased {
		t.Fatalf("outcome = %+v, want unleased", res)
	}
	ls := readLeaseState(t, ctx, store, "renew-noneed")
	if ls.leaseExpires.Valid {
		t.Error("renewal armed a lease on an unleased claim")
	}
}

// TestRenewLeasesChunking: a batch larger than the chunk size still renews
// every live ref (chunk boundaries are internal).
func TestRenewLeasesChunking(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	var refs []storage.LeaseRef
	for i := 0; i < 7; i++ {
		id := "renew-chunk-" + string(rune('a'+i))
		f := claimWithFence(t, ctx, store, id, "alice")
		refs = append(refs, storage.LeaseRef{ID: id, Fence: f})
	}
	res, err := storage.RenewLeasesChunked(ctx, store, refs, time.Minute, 3)
	if err != nil {
		t.Fatalf("RenewLeasesChunked: %v", err)
	}
	if len(res) != 7 {
		t.Fatalf("got %d outcomes, want 7", len(res))
	}
	for _, r := range res {
		if r.Outcome != storage.LeaseRenewed {
			t.Errorf("%s outcome = %v, want renewed", r.ID, r.Outcome)
		}
	}
}

// TestCountActiveClaimsByOwner: the session-close gate can ask "does this
// owner still hold claims?" across both tiers without scanning.
func TestCountActiveClaimsByOwner(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "own-issue", "worker-1", time.Hour)
	seedClaimedWisp(t, ctx, store, "own-wisp", "worker-1", time.Hour)
	seedClaimedIssue(t, ctx, store, "own-other", "worker-2", time.Hour)

	n, err := store.CountActiveClaimsByOwner(ctx, "worker-1")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("worker-1 active claims = %d, want 2 (one per tier)", n)
	}

	if err := store.UnclaimIssue(ctx, "own-issue", "worker-1", false); err != nil {
		t.Fatalf("unclaim: %v", err)
	}
	n, err = store.CountActiveClaimsByOwner(ctx, "worker-1")
	if err != nil {
		t.Fatalf("count after release: %v", err)
	}
	if n != 1 {
		t.Errorf("worker-1 active claims after release = %d, want 1", n)
	}
}
