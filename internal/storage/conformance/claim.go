package conformance

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Claim / lease behavior (Gas Station v1.1 dead-worker recovery). Each backend
// routes ClaimIssue/ClaimReadyIssue/HeartbeatIssue/ReclaimExpiredLeases through the
// shared issueops implementations, so these assertions hold identically on Dolt and
// SQLite. issueops.WithLeaseTTL pins the lease deterministically
// so the reclaim path is testable without wall-clock waits.

// testClaim: claiming an open issue stamps assignee, in_progress, started_at, and a
// fresh lease (lease_expires_at in the future + heartbeat_at set).
func testClaim(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cl-1", Title: "T"}), "a"))
	before := time.Now().UTC()
	must(t, s.ClaimIssue(ctx(), "cl-1", "worker"))

	got, err := s.GetIssue(ctx(), "cl-1")
	must(t, err)
	if got.Assignee != "worker" {
		t.Errorf("assignee = %q, want %q", got.Assignee, "worker")
	}
	if got.Status != types.StatusInProgress {
		t.Errorf("status = %q, want %q", got.Status, types.StatusInProgress)
	}
	if got.StartedAt == nil {
		t.Error("started_at not set on claim")
	}
	if got.LeaseExpiresAt == nil || !got.LeaseExpiresAt.After(before) {
		t.Errorf("lease_expires_at should be in the future, got %v", got.LeaseExpiresAt)
	}
	if got.HeartbeatAt == nil {
		t.Error("heartbeat_at not set on claim")
	}
}

// testClaimIdempotent: re-claiming an in_progress issue by the SAME actor is a no-op
// success (supports agent retry workflows).
func testClaimIdempotent(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cl-2", Title: "T"}), "a"))
	must(t, s.ClaimIssue(ctx(), "cl-2", "worker"))
	if err := s.ClaimIssue(ctx(), "cl-2", "worker"); err != nil {
		t.Errorf("idempotent re-claim by same actor: %v", err)
	}
}

// testClaimAlreadyClaimed: claiming an issue held by a different actor is ErrAlreadyClaimed.
func testClaimAlreadyClaimed(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cl-3", Title: "T"}), "a"))
	must(t, s.ClaimIssue(ctx(), "cl-3", "worker1"))
	err := s.ClaimIssue(ctx(), "cl-3", "worker2")
	if !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Errorf("claim by different actor: err = %v, want ErrAlreadyClaimed", err)
	}
}

// testClaimNotClaimable: claiming a closed issue is ErrNotClaimable.
func testClaimNotClaimable(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cl-4", Title: "T"}), "a"))
	must(t, s.CloseIssue(ctx(), "cl-4", "done", "closer", "sess"))
	err := s.ClaimIssue(ctx(), "cl-4", "worker")
	if !errors.Is(err, storage.ErrNotClaimable) {
		t.Errorf("claim closed issue: err = %v, want ErrNotClaimable", err)
	}
}

// testClaimReadyIssue: ClaimReadyIssue picks and claims a ready issue.
func testClaimReadyIssue(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cl-r1", Title: "ready", Priority: 1}), "a"))
	claimed, err := s.ClaimReadyIssue(ctx(), types.WorkFilter{}, "worker")
	must(t, err)
	if claimed == nil {
		t.Fatal("ClaimReadyIssue returned nil, expected a ready issue to claim")
	}
	if claimed.ID != "cl-r1" {
		t.Errorf("claimed id = %q, want cl-r1", claimed.ID)
	}
	if claimed.Assignee != "worker" || claimed.Status != types.StatusInProgress {
		t.Errorf("ready issue not properly claimed: assignee=%q status=%q", claimed.Assignee, claimed.Status)
	}
}

// testClaimReadyIssueLabelFilters: the claim path is FENCED by the label filters it is
// given. --label-any (LabelsAny, OR-set) used to be dropped on the ready/claim path, so a
// worker asking for its own lane could atomically claim another lane's work while
// believing it was fenced. Asserts the OR-set works alone, AND-combines with the Labels
// AND-set and with --parent, and — the safety property — that an exhausted filter claims
// NOTHING rather than falling back to unfenced ready work.
func testClaimReadyIssueLabelFilters(t *testing.T, f Factory) {
	s := f(t)
	// clf-free is unlabeled and top-priority: it wins any claim whose label filter was
	// dropped, so every assertion below doubles as a check that the filter was applied.
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "clf-free", Title: "unfenced", Priority: 0}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "clf-p.1", Title: "other lane", Priority: 1}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "clf-p.2", Title: "my lane", Priority: 3}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "clf-x", Title: "my lane, other parent", Priority: 2}), "a"))
	must(t, s.AddLabel(ctx(), "clf-p.1", "lane-b", "a"))
	must(t, s.AddLabel(ctx(), "clf-p.2", "lane-a", "a"))
	must(t, s.AddLabel(ctx(), "clf-p.2", "tier:opus", "a"))
	must(t, s.AddLabel(ctx(), "clf-x", "lane-a", "a"))

	claim := func(filter types.WorkFilter) *types.Issue {
		t.Helper()
		claimed, err := s.ClaimReadyIssue(ctx(), filter, "worker")
		must(t, err)
		return claimed
	}
	parent := "clf-p"

	// --label-any + --parent: the only child carrying lane-a, even though clf-p.1 is the
	// higher-priority child and clf-x is the higher-priority lane-a issue.
	got := claim(types.WorkFilter{LabelsAny: []string{"lane-a", "lane-c"}, ParentID: &parent})
	if got == nil || got.ID != "clf-p.2" {
		t.Fatalf("claim(--label-any lane-a,lane-c --parent clf-p) = %v, want clf-p.2", issueID(got))
	}

	// AND-set + OR-set: the AND-set is unsatisfiable, so nothing is claimable — an
	// unfenced claim would take clf-free.
	if got := claim(types.WorkFilter{Labels: []string{"tier:nobody"}, LabelsAny: []string{"lane-a"}}); got != nil {
		t.Errorf("claim(--label tier:nobody --label-any lane-a) = %v, want no claim", issueID(got))
	}

	// --label-any alone still fences: clf-x, not the unlabeled higher-priority clf-free.
	if got := claim(types.WorkFilter{LabelsAny: []string{"lane-a"}}); got == nil || got.ID != "clf-x" {
		t.Fatalf("claim(--label-any lane-a) = %v, want clf-x", issueID(got))
	}

	// Lane exhausted: claim NOTHING rather than falling back to the unfenced clf-free.
	if got := claim(types.WorkFilter{LabelsAny: []string{"lane-a"}}); got != nil {
		t.Errorf("claim(--label-any lane-a) with the lane exhausted = %v, want no claim", issueID(got))
	}
	if got := claim(types.WorkFilter{}); got == nil || got.ID != "clf-free" {
		t.Errorf("unfiltered claim = %v, want clf-free (it must still be claimable)", issueID(got))
	}
}

func issueID(i *types.Issue) string {
	if i == nil {
		return "<nil>"
	}
	return i.ID
}

// testHeartbeatRenewsLease: a heartbeat extends the lease (and keeps the claim).
// Heartbeating with a one-hour TTL must push lease_expires_at far into the future.
func testHeartbeatRenewsLease(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "hb-1", Title: "T"}), "a"))
	must(t, s.ClaimIssue(ctx(), "hb-1", "worker"))
	must(t, s.HeartbeatIssue(issueops.WithLeaseTTL(ctx(), time.Hour), "hb-1", "worker"))

	got, err := s.GetIssue(ctx(), "hb-1")
	must(t, err)
	if got.Status != types.StatusInProgress {
		t.Errorf("heartbeat changed status to %q", got.Status)
	}
	if got.HeartbeatAt == nil {
		t.Error("heartbeat_at not set after heartbeat")
	}
	if got.LeaseExpiresAt == nil || !got.LeaseExpiresAt.After(time.Now().UTC().Add(30*time.Minute)) {
		t.Errorf("heartbeat should renew lease well into the future, got %v", got.LeaseExpiresAt)
	}
}

// testHeartbeatWisp: wisps are ephemeral and never leased — heartbeating one is
// ErrNotClaimable (matching the Dolt reference).
func testHeartbeatWisp(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "hb-w1", Title: "W", Ephemeral: true}), "a"))
	err := s.HeartbeatIssue(ctx(), "hb-w1", "worker")
	if !errors.Is(err, storage.ErrNotClaimable) {
		t.Errorf("heartbeat wisp: err = %v, want ErrNotClaimable", err)
	}
}

// testReclaimExpiredLease: a claim whose lease has expired is reverted to open and
// unassigned, reported with its previous owner, and its lease/started_at cleared.
func testReclaimExpiredLease(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rc-1", Title: "T"}), "a"))
	must(t, s.ClaimIssue(ctx(), "rc-1", "deadworker")) // fresh default-TTL lease

	// Reclaim with a cutoff past the lease horizon: a negative olderThan moves the
	// cutoff (now - olderThan) into the future, so even the fresh lease counts as
	// expired. This exercises the revert path deterministically with no wall-clock
	// wait; ReclaimSkipsFreshLease (olderThan 0) pins the other side of the cutoff.
	reclaimed, err := s.ReclaimExpiredLeases(ctx(), -time.Hour, "reaper")
	must(t, err)
	found := false
	for _, r := range reclaimed {
		if r.ID == "rc-1" {
			found = true
			if r.PreviousOwner != "deadworker" {
				t.Errorf("reclaimed previous_owner = %q, want deadworker", r.PreviousOwner)
			}
		}
	}
	if !found {
		t.Errorf("rc-1 not reported reclaimed: %+v", reclaimed)
	}

	got, err := s.GetIssue(ctx(), "rc-1")
	must(t, err)
	if got.Status != types.StatusOpen {
		t.Errorf("after reclaim: status = %q, want open", got.Status)
	}
	if got.Assignee != "" {
		t.Errorf("after reclaim: assignee = %q, want empty", got.Assignee)
	}
	if got.LeaseExpiresAt != nil {
		t.Errorf("after reclaim: lease_expires_at = %v, want cleared", got.LeaseExpiresAt)
	}
	if got.StartedAt != nil {
		t.Errorf("after reclaim: started_at = %v, want cleared", got.StartedAt)
	}
}

// testReclaimSkipsFreshLease: a live claim (fresh, unexpired lease) is left untouched.
func testReclaimSkipsFreshLease(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rc-2", Title: "T"}), "a"))
	must(t, s.ClaimIssue(ctx(), "rc-2", "liveworker")) // fresh default-TTL lease

	reclaimed, err := s.ReclaimExpiredLeases(ctx(), 0, "reaper")
	must(t, err)
	for _, r := range reclaimed {
		if r.ID == "rc-2" {
			t.Errorf("fresh lease rc-2 must not be reclaimed")
		}
	}
	got, err := s.GetIssue(ctx(), "rc-2")
	must(t, err)
	if got.Status != types.StatusInProgress || got.Assignee != "liveworker" {
		t.Errorf("fresh claim disturbed: status=%q assignee=%q", got.Status, got.Assignee)
	}
}

// testUnclaimIfAssigneeMatch: a conditional release with the correct expected
// assignee clears the claim exactly once (assignee empty, status open). A repeat
// of the same conditional release finds the claim already gone and fails with
// storage.ErrAssigneeMismatch instead of silently succeeding — the "exactly
// once" property a release-if-current caller (e.g. a supervisor returning a
// dead worker's bead) depends on.
func testUnclaimIfAssigneeMatch(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ur-1", Title: "T"}), "a"))
	must(t, s.ClaimIssue(ctx(), "ur-1", "worker1"))

	must(t, s.UnclaimIssueIfAssignee(ctx(), "ur-1", "releaser", "worker1"))
	got, err := s.GetIssue(ctx(), "ur-1")
	must(t, err)
	if got.Assignee != "" {
		t.Errorf("after conditional release: assignee = %q, want empty", got.Assignee)
	}
	if got.Status != types.StatusOpen {
		t.Errorf("after conditional release: status = %q, want open", got.Status)
	}

	err = s.UnclaimIssueIfAssignee(ctx(), "ur-1", "releaser", "worker1")
	if !errors.Is(err, storage.ErrAssigneeMismatch) {
		t.Errorf("repeat conditional release: err = %v, want ErrAssigneeMismatch", err)
	}
}

// testUnclaimIfAssigneeStale: a conditional release whose expected assignee is
// stale (someone else holds the claim) is a loud no-op: it returns
// storage.ErrAssigneeMismatch naming the current holder, leaves the claim
// untouched, and records no "unclaimed" event. This is the CAS that makes
// release-if-current safe across processes — a stale releaser can never clobber
// another worker's live claim.
func testUnclaimIfAssigneeStale(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ur-2", Title: "T"}), "a"))
	must(t, s.ClaimIssue(ctx(), "ur-2", "worker2"))

	err := s.UnclaimIssueIfAssignee(ctx(), "ur-2", "releaser", "worker1")
	if !errors.Is(err, storage.ErrAssigneeMismatch) {
		t.Errorf("stale conditional release: err = %v, want ErrAssigneeMismatch", err)
	}

	got, err := s.GetIssue(ctx(), "ur-2")
	must(t, err)
	if got.Assignee != "worker2" {
		t.Errorf("stale release clobbered claim: assignee = %q, want worker2", got.Assignee)
	}
	if got.Status != types.StatusInProgress {
		t.Errorf("stale release changed status to %q, want in_progress", got.Status)
	}

	events, err := s.GetEvents(ctx(), "ur-2", 0)
	must(t, err)
	for _, e := range events {
		if e.EventType == types.EventType("unclaimed") {
			t.Errorf("stale conditional release recorded an unclaimed event: %+v", e)
		}
	}
}
