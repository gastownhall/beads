package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the claim half of the lifecycle update's blocker policy: a
// Claim carried by Lifecycle.Update answers to the live-direct-blocker check
// `bd close` already applies (RunLifecycleUpdateClosePolicy), refuses with a
// BlockedError wrapping ErrClaimBlocked, and writes nothing when it does. The
// cases run against LifecycleUpdateFixture, so every leg that wires the update
// role answers for them.

// RunLifecycleUpdateClaimRefusesALiveBlocker pins the refusal: an unforced
// claim of an open, unassigned issue or wisp with an open local `blocks`
// target is refused with a typed BlockedError carrying ErrClaimBlocked — not
// ErrCloseBlocked, which is the external-reference guard's claim refusal — and
// neither the claim nor a patch riding it reaches the row or the journal.
func RunLifecycleUpdateClaimRefusesALiveBlocker(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	if fixture.AddDependency == nil {
		t.Skip("fixture cannot seed an edge: AddDependency is nil, so there is no blocker to refuse over")
	}

	blockerID := fixture.IssuePrefix + "-lup-claimblock-blocker"
	blockedID := fixture.IssuePrefix + "-lup-claimblock-blocked"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(blockerID))
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(blockedID))
	seedLifecycleUpdateEdge(t, ctx, fixture, blockedID, blockerID, types.DepBlocks)

	bare := publicops.UpdateRequest{Actor: "claimant", IssueID: blockedID, Claim: true}
	patched := bare
	patched.Patch = publicops.IssuePatch{
		Title: publicops.Field[string]{Set: true, Value: "patched by a refused claim"},
		Notes: publicops.Field[string]{Set: true, Value: "refused notes"},
	}
	cases := []struct {
		name    string
		request publicops.UpdateRequest
	}{
		{name: "bare claim", request: bare},
		{name: "claim with a patch", request: patched},
	}

	if fixture.CreateWisp != nil {
		wispID := fixture.IssuePrefix + "-lup-claimblock-wisp"
		seedLifecycleUpdateWisp(t, ctx, fixture, lifecycleUpdateIssue(wispID))
		seedLifecycleUpdateEdge(t, ctx, fixture, wispID, blockerID, types.DepBlocks)
		cases = append(cases, struct {
			name    string
			request publicops.UpdateRequest
		}{name: "wisp claim", request: publicops.UpdateRequest{Actor: "claimant", IssueID: wispID, Claim: true}})
	} else {
		t.Log("fixture has no wisp plane (CreateWisp is nil): the wisp refusal is not proven on this leg")
	}

	for _, tc := range cases {
		id := tc.request.IssueID
		before := lifecycleUpdateRow(t, ctx, fixture, id)
		events := newLifecycleUpdateEventCounter(t, ctx, fixture, id)
		_, err := fixture.Lifecycle.Update(ctx, tc.request)
		assertLifecycleClaimBlocked(t, tc.name, err, id, blockerID)
		assertLifecycleUpdateRowUnchanged(t, ctx, fixture, id, "after the refused "+tc.name, before)
		events.assert(t, "refused "+tc.name, 0)
	}
	assertLifecycleUpdateStatus(t, ctx, fixture, blockerID, types.StatusOpen)
}

// assertLifecycleClaimBlocked checks that err is the blocked-claim refusal for
// id, naming blockerID: typed, carrying ErrClaimBlocked, and not the
// external-reference guard's ErrCloseBlocked.
func assertLifecycleClaimBlocked(t *testing.T, label string, err error, id, blockerID string) {
	t.Helper()
	if !errors.Is(err, storage.ErrClaimBlocked) {
		t.Fatalf("%s of %s with a live blocker: err = %v, want ErrClaimBlocked", label, id, err)
	}
	if errors.Is(err, storage.ErrCloseBlocked) {
		t.Errorf("%s of %s: err = %v also matches ErrCloseBlocked, want only ErrClaimBlocked", label, id, err)
	}
	var blocked *publicops.BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("%s of %s: err = %v (%T), want a *BlockedError", label, id, err, err)
	}
	if blocked.IssueID != id {
		t.Errorf("%s: BlockedError.IssueID = %q, want %q", label, blocked.IssueID, id)
	}
	if len(blocked.Blockers) != 1 || blocked.Blockers[0].ID != blockerID || blocked.Blockers[0].Type != types.DepBlocks {
		t.Errorf("%s: BlockedError.Blockers = %+v, want exactly [%s blocks]", label, blocked.Blockers, blockerID)
	}
}

// RunLifecycleUpdateForceClaimBlockedClaimsPastALiveBlocker pins the override:
// ForceClaimBlocked lets a claim through a live local blocker — the issue goes
// in_progress under the actor and the blocker and its edge stay as they were —
// and waives nothing else: a live foreign holder still refuses with
// ErrAlreadyClaimed.
func RunLifecycleUpdateForceClaimBlockedClaimsPastALiveBlocker(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	if fixture.AddDependency == nil {
		t.Skip("fixture cannot seed an edge: AddDependency is nil, so there is no blocker to force past")
	}

	blockerID := fixture.IssuePrefix + "-lup-forceclaim-blocker"
	blockedID := fixture.IssuePrefix + "-lup-forceclaim-blocked"
	heldID := fixture.IssuePrefix + "-lup-forceclaim-held"
	for _, id := range []string{blockerID, blockedID, heldID} {
		seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(id))
	}
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "holder", IssueID: heldID, Claim: true}); err != nil {
		t.Fatalf("seed holder's claim of %s: %v", heldID, err)
	}
	seedLifecycleUpdateEdge(t, ctx, fixture, blockedID, blockerID, types.DepBlocks)
	seedLifecycleUpdateEdge(t, ctx, fixture, heldID, blockerID, types.DepBlocks)
	blockerBefore := lifecycleUpdateRow(t, ctx, fixture, blockerID)

	forced, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "claimant", IssueID: blockedID, Claim: true, ForceClaimBlocked: true})
	if err != nil {
		t.Fatalf("forced claim of %s past a live blocker: %v", blockedID, err)
	}
	if !forced.Changed {
		t.Errorf("forced claim of %s reported Changed = false, want a committed claim", blockedID)
	}
	assertLifecycleUpdateAssigneeAndStatus(t, ctx, fixture, blockedID, "claimant", types.StatusInProgress)
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, blockerID, "after a forced claim of the issue it blocks", blockerBefore)
	if fixture.ListDependencies != nil {
		edges, err := fixture.ListDependencies(ctx, blockedID)
		if err != nil {
			t.Fatalf("list %s's edges: %v", blockedID, err)
		}
		if !lifecycleUpdateHasEdge(edges, blockerID) {
			t.Errorf("forced claim of %s dropped its edge to %s: %+v", blockedID, blockerID, edges)
		}
	} else {
		t.Log("fixture cannot list edges (ListDependencies is nil): the blocker edge surviving the forced claim is not proven on this leg")
	}

	before := lifecycleUpdateRow(t, ctx, fixture, heldID)
	_, err = fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "rival", IssueID: heldID, Claim: true, ForceClaimBlocked: true})
	if !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Fatalf("forced claim of %s held by another actor: err = %v, want ErrAlreadyClaimed", heldID, err)
	}
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, heldID, "after a forced claim against a live holder", before)
}

// RunLifecycleUpdateReclaimByTheHolderIgnoresANewBlocker pins the idempotent
// re-claim: an issue already in_progress under the actor that gains a live
// blocker afterwards is not refused when the same actor claims it again, and
// the repeat writes nothing — the blocker check guards taking a claim, not
// holding one.
func RunLifecycleUpdateReclaimByTheHolderIgnoresANewBlocker(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	if fixture.AddDependency == nil {
		t.Skip("fixture cannot seed an edge: AddDependency is nil, so there is no blocker to ignore")
	}

	blockerID := fixture.IssuePrefix + "-lup-reclaim-blocker"
	heldID := fixture.IssuePrefix + "-lup-reclaim-held"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(blockerID))
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(heldID))
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "holder", IssueID: heldID, Claim: true}); err != nil {
		t.Fatalf("seed holder's claim of %s: %v", heldID, err)
	}
	seedLifecycleUpdateEdge(t, ctx, fixture, heldID, blockerID, types.DepBlocks)

	before := lifecycleUpdateRow(t, ctx, fixture, heldID)
	events := newLifecycleUpdateEventCounter(t, ctx, fixture, heldID)
	result, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "holder", IssueID: heldID, Claim: true})
	if err != nil {
		t.Fatalf("holder's repeat claim of %s after a blocker appeared: %v, want the idempotent no-op", heldID, err)
	}
	if result.Changed {
		t.Errorf("holder's repeat claim of %s reported Changed = true, want a no-op", heldID)
	}
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, heldID, "after the holder's repeat claim", before)
	assertLifecycleUpdateAssigneeAndStatus(t, ctx, fixture, heldID, "holder", types.StatusInProgress)
	events.assert(t, "holder's repeat claim", 0)
}
