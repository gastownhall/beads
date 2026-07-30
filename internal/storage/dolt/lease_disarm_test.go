package dolt

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestAutoLeaseOffClaimStampsNothing: with lease.auto=off a claim carries no
// lease, so there is nothing for bd reclaim to reap and an un-renewed fleet is
// never one stray reclaim away from mass-revert. The claim is otherwise
// untouched — the fence still bumps (it is an ownership transition either way)
// and row_lock is still rewritten with it (pairing invariant).
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
	if ls.status != "in_progress" {
		t.Errorf("status = %q, want in_progress (disarming leases must not affect the claim)", ls.status)
	}
	assertFenceBumped(t, before, readFenceState(t, ctx, store, "disarm-claim"), "unleased claim")

	// Nothing to reclaim: the unleased claim is invisible to the reaper.
	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, types.ReclaimFilter{}, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Errorf("reclaim reaped %d unleased claims, want 0", len(reclaimed))
	}
}

// TestAutoLeaseOffIgnoresLeaseTTLContext pins the scope of this commit: the
// WithLeaseTTL context knob is a TTL override, NOT an opt-in that survives
// disarming. A per-claim "requested lease" surface (--lease-ttl and its
// renewal API) is parked pending the wisp-leasing ruling, so on a disarmed
// store every claim is unleased, however it was made.
func TestAutoLeaseOffIgnoresLeaseTTLContext(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, issueops.LeaseAutoConfigKey, "off"); err != nil {
		t.Fatalf("set lease.auto=off: %v", err)
	}

	seedOpenIssue(t, ctx, store, "disarm-explicit")
	if err := store.ClaimIssue(issueops.WithLeaseTTL(ctx, time.Second), "disarm-explicit", "alice"); err != nil {
		t.Fatalf("claim with explicit TTL: %v", err)
	}
	if ls := readLeaseState(t, ctx, store, "disarm-explicit"); ls.leaseExpires.Valid {
		t.Error("WithLeaseTTL claim stamped a lease under lease.auto=off")
	}
}

// TestHeartbeatOnUnleasedClaimRejected: heartbeat must never ARM a lease as a
// side effect — one stray bd heartbeat on a deliberately unleased claim would
// re-create the reclaim exposure the disarm removes.
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
	if ls := readLeaseState(t, ctx, store, "disarm-hb"); ls.leaseExpires.Valid {
		t.Error("rejected heartbeat armed a lease")
	}

	// Owner/status errors still take precedence over unleased.
	if err := store.HeartbeatIssue(ctx, "disarm-hb", "mallory"); !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Errorf("stranger heartbeat: got %v, want ErrAlreadyClaimed", err)
	}
}

// TestHeartbeatRenewsLeaseGrantedBeforeDisarm: disarming is a one-shot sweep,
// not a standing rejection. A claim that still holds a lease row — granted
// before the flip, or restored by an import — keeps renewing normally; only an
// owned claim with no lease row at all is refused.
func TestHeartbeatRenewsLeaseGrantedBeforeDisarm(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedClaimedIssue(t, ctx, store, "disarm-renew", "alice", time.Minute)
	if err := store.SetConfig(ctx, issueops.LeaseAutoConfigKey, "off"); err != nil {
		t.Fatalf("set lease.auto=off: %v", err)
	}

	before := readLeaseState(t, ctx, store, "disarm-renew")
	if !before.leaseExpires.Valid {
		t.Fatal("test setup: claim under lease.auto=on did not stamp a lease")
	}
	if err := store.HeartbeatIssue(issueops.WithLeaseTTL(ctx, time.Hour), "disarm-renew", "alice"); err != nil {
		t.Fatalf("heartbeat on a still-leased claim: %v", err)
	}
	after := readLeaseState(t, ctx, store, "disarm-renew")
	if !after.leaseExpires.Valid {
		t.Fatal("heartbeat dropped the lease row")
	}
	if !after.leaseExpires.Time.After(before.leaseExpires.Time) {
		t.Errorf("lease_expires_at %v did not advance past %v", after.leaseExpires.Time, before.leaseExpires.Time)
	}
}

// TestDisarmAutoLeases: the one-shot flip — sets lease.auto=off and clears the
// lease rows of the claims already holding one, so a fleet opting out of the
// lease regime is safe from bd reclaim in the same transaction that turns
// stamping off. Idempotent: a second run flips an already-flipped key and
// sweeps nothing.
func TestDisarmAutoLeases(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// Two armed claims under the default (auto=on) semantics.
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
		if !ls.assignee.Valid || ls.assignee.String == "" {
			t.Errorf("%s lost its assignee to the disarm", id)
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
	reclaimed, err := store.ReclaimExpiredLeases(ctx, 0, types.ReclaimFilter{}, "reaper")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Errorf("reclaim reaped %d post-disarm, want 0", len(reclaimed))
	}

	// Idempotent: the key is already off and there is nothing left to sweep.
	again, err := store.DisarmAutoLeases(ctx)
	if err != nil {
		t.Fatalf("second disarm: %v", err)
	}
	if again != 0 {
		t.Errorf("second disarm cleared %d leases, want 0", again)
	}
}

// TestLeaseAutoValueParsing pins which config values disarm stamping. Only an
// explicitly falsy value does; unset, "on" and a typo all leave the shipped
// default armed, because a knob that silently drops recovery on a misspelling
// is worse than one that ignores it.
func TestLeaseAutoValueParsing(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	cases := []struct {
		value  string
		leased bool
	}{
		{"off", false},
		{"false", false},
		{"0", false},
		{"OFF", false},
		{"on", true},
		{"true", true},
		{"", true},
		{"maybe", true},
	}
	for i, tc := range cases {
		if err := store.SetConfig(ctx, issueops.LeaseAutoConfigKey, tc.value); err != nil {
			t.Fatalf("set lease.auto=%q: %v", tc.value, err)
		}
		id := fmt.Sprintf("disarm-parse-%d", i)
		seedOpenIssue(t, ctx, store, id)
		if err := store.ClaimIssue(ctx, id, "alice"); err != nil {
			t.Fatalf("claim %s: %v", id, err)
		}
		if got := readLeaseState(t, ctx, store, id).leaseExpires.Valid; got != tc.leased {
			t.Errorf("lease.auto=%q: leased = %v, want %v", tc.value, got, tc.leased)
		}
	}
}
