package dolt

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestAutoLeaseOffClaimStampsNothing: with lease.auto=off, a claim carries no
// lease — nothing for bd reclaim to reap, so an un-renewed fleet is never one
// stray reclaim away from mass-revert. The fence still bumps (ownership
// transition), and row_lock is still rewritten (pairing invariant).
func TestAutoLeaseOffClaimStampsNothing(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.LeaseAutoConfigKey, "off"); err != nil {
		t.Fatalf("set lease.auto=off: %v", err)
	}

	seedOpenIssue(t, ctx, store, "disarm-claim")
	before := readFenceState(t, ctx, store, "disarm-claim")
	if err := store.ClaimIssue(ctx, "disarm-claim", "alice"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	ls := readLeaseState(t, ctx, store, "disarm-claim")
	if ls.leaseExpires.Valid || ls.heartbeatAt.Valid {
		t.Errorf("lease.auto=off claim stamped a lease: expires=%v heartbeat=%v", ls.leaseExpires, ls.heartbeatAt)
	}
	assertFenceBumped(t, before, readFenceState(t, ctx, store, "disarm-claim"), "unleased claim")

	// Nothing to reclaim: the unleased claim is invisible to the reaper.
	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Errorf("reclaim reaped %d unleased claims, want 0", len(reclaimed))
	}
}

// TestAutoLeaseOffExplicitTTLStillLeases: WithLeaseTTL is the opt-in — an
// explicitly requested lease stamps and stays reclaimable even when auto
// stamping is off.
func TestAutoLeaseOffExplicitTTLStillLeases(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.LeaseAutoConfigKey, "off"); err != nil {
		t.Fatalf("set lease.auto=off: %v", err)
	}

	seedOpenIssue(t, ctx, store, "disarm-explicit")
	claimCtx := issueops.WithLeaseTTL(ctx, time.Second)
	if err := store.ClaimIssue(claimCtx, "disarm-explicit", "alice"); err != nil {
		t.Fatalf("claim with explicit TTL: %v", err)
	}
	ls := readLeaseState(t, ctx, store, "disarm-explicit")
	if !ls.leaseExpires.Valid {
		t.Fatal("explicit WithLeaseTTL claim did not stamp a lease under lease.auto=off")
	}

	time.Sleep(3 * time.Second)
	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 1 {
		t.Errorf("requested lease not reclaimed: got %d, want 1", len(reclaimed))
	}
}

// TestHeartbeatOnUnleasedClaimRejected: heartbeat must never ARM a lease as a
// side effect — one stray bd heartbeat on a deliberately unleased claim would
// re-create the unrequested reclaim exposure the disarm removes.
func TestHeartbeatOnUnleasedClaimRejected(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.LeaseAutoConfigKey, "off"); err != nil {
		t.Fatalf("set lease.auto=off: %v", err)
	}
	seedOpenIssue(t, ctx, store, "disarm-hb")
	if err := store.ClaimIssue(ctx, "disarm-hb", "alice"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	err := store.HeartbeatIssue(ctx, "disarm-hb", "alice")
	if !errors.Is(err, storage.ErrUnleased) {
		t.Fatalf("heartbeat on unleased claim: got %v, want ErrUnleased", err)
	}
	ls := readLeaseState(t, ctx, store, "disarm-hb")
	if ls.leaseExpires.Valid {
		t.Error("rejected heartbeat armed a lease")
	}

	// Owner/status errors still take precedence over unleased.
	if err := store.HeartbeatIssue(ctx, "disarm-hb", "mallory"); !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Errorf("stranger heartbeat: got %v, want ErrAlreadyClaimed", err)
	}
}

// TestDisarmAutoLeases: the one-shot flip — sets lease.auto=off and NULLs the
// armed auto-stamped leases on existing in_progress rows (both tables), so a
// fleet upgrading past the lease stack is safe from bd reclaim in the same
// transaction that turns stamping off.
func TestDisarmAutoLeases(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Two armed claims under default (auto=on) semantics.
	seedClaimedIssue(t, ctx, store, "disarm-a", "alice", time.Minute)
	seedClaimedIssue(t, ctx, store, "disarm-b", "bob", time.Minute)
	fenceA := readFenceState(t, ctx, store, "disarm-a")

	n, err := store.DisarmAutoLeases(ctx)
	if err != nil {
		t.Fatalf("disarm: %v", err)
	}
	if n != 2 {
		t.Errorf("disarmed %d leases, want 2", n)
	}

	for _, id := range []string{"disarm-a", "disarm-b"} {
		ls := readLeaseState(t, ctx, store, id)
		if ls.leaseExpires.Valid || ls.heartbeatAt.Valid {
			t.Errorf("%s still armed after disarm", id)
		}
		if ls.status != "in_progress" {
			t.Errorf("%s status = %q, want in_progress (disarm must not release)", id, ls.status)
		}
	}
	// Disarm is not an ownership transition — the fence must NOT move.
	if after := readFenceState(t, ctx, store, "disarm-a"); after.fence != fenceA.fence {
		t.Errorf("disarm moved claim_fence %d → %d, want unchanged", fenceA.fence, after.fence)
	}

	// The config flip persisted: subsequent claims stamp nothing.
	seedOpenIssue(t, ctx, store, "disarm-c")
	if err := store.ClaimIssue(ctx, "disarm-c", "carol"); err != nil {
		t.Fatalf("post-disarm claim: %v", err)
	}
	if ls := readLeaseState(t, ctx, store, "disarm-c"); ls.leaseExpires.Valid {
		t.Error("post-disarm claim stamped a lease")
	}

	// Nothing reclaimable remains.
	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Errorf("reclaim reaped %d post-disarm, want 0", len(reclaimed))
	}
}
