package conformance

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Claim / lease behavior (Gas Station v1.1 dead-worker recovery). Every backend
// routes ClaimIssue/ClaimReadyIssue/HeartbeatIssue/ReclaimExpiredLeases through the
// shared issueops implementations, so these assertions hold identically on the Dolt
// reference and every SQL backend. issueops.WithLeaseTTL pins the lease deterministically
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

// requireMultiWriter skips the caller unless the backend backs concurrent writers on
// separate connections. Postgres and MySQL use a multi-connection pool; SQLite pins its
// pool to a single connection (sqliteDialect.Open sets MaxOpenConns(1)) and embedded-Dolt
// serializes writers, so a "concurrent" claim race there only ever exercises the trivial
// serialized path. The dialect name is the profile signal: sqlkit-backed SQL stores
// expose DialectName(); the Dolt reference does not, so it skips via the type assertion.
func requireMultiWriter(t *testing.T, s storage.DoltStorage) {
	t.Helper()
	dn, ok := s.(interface{ DialectName() string })
	if !ok {
		t.Skip("backend does not expose a SQL dialect; concurrent multi-writer claim race not applicable")
	}
	switch dn.DialectName() {
	case "postgres", "mysql":
		// multi-connection pool: a genuine cross-connection race.
	default:
		t.Skipf("dialect %q is single-writer (pool pinned to one connection); concurrent claim race not applicable", dn.DialectName())
	}
}

// testClaimReadyIssueConcurrentExclusivity races many workers, each on its own pooled
// connection, to claim a small pool of ready issues and asserts every issue is claimed by
// exactly one worker. This is the multi-writer property the SQL backends exist for — the
// whole reason to run bd on Postgres/MySQL instead of single-writer Dolt — and the shared
// claim path's cross-process exclusivity (a conditional-UPDATE CAS: only the writer whose
// UPDATE finds the row still unassigned wins; the losers' ClaimReadyIssue retries the next
// ready candidate) is otherwise only asserted, never raced. Gated to the multi-connection
// backends; see requireMultiWriter.
func testClaimReadyIssueConcurrentExclusivity(t *testing.T, f Factory) {
	s := f(t)
	requireMultiWriter(t, s)

	const readyCount = 8
	for i := 0; i < readyCount; i++ {
		must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{
			ID:       fmt.Sprintf("ccx-%d", i),
			Title:    "ready",
			Priority: 1,
		}), "a"))
	}

	const workers = 24 // > readyCount, so workers must contend and most must lose a race

	var (
		mu     sync.Mutex
		claims = make(map[string]string) // issueID -> claiming worker
		dupes  []string
		errs   []error
	)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker string) {
			defer wg.Done()
			<-start // barrier: release all workers together to maximize contention
			// Drain: keep claiming until no ready work remains. Bounded so a bug that
			// kept returning issues can never hang the suite.
			for attempt := 0; attempt < readyCount+workers; attempt++ {
				claimed, err := s.ClaimReadyIssue(ctx(), types.WorkFilter{}, worker)
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
				if claimed == nil {
					return // no ready work left to claim
				}
				if claimed.Assignee != worker || claimed.Status != types.StatusInProgress {
					t.Errorf("claimed %s not owned by claimer: assignee=%q status=%q", claimed.ID, claimed.Assignee, claimed.Status)
				}
				mu.Lock()
				if prev, ok := claims[claimed.ID]; ok {
					dupes = append(dupes, fmt.Sprintf("%s claimed by both %s and %s", claimed.ID, prev, worker))
				} else {
					claims[claimed.ID] = worker
				}
				mu.Unlock()
			}
		}(fmt.Sprintf("worker-%d", w))
	}
	close(start)
	wg.Wait()

	for _, e := range errs {
		t.Errorf("ClaimReadyIssue errored under concurrency: %v", e)
	}
	for _, d := range dupes {
		t.Errorf("claim exclusivity violated — %s", d)
	}
	if len(claims) != readyCount {
		t.Errorf("distinct claimed issues = %d, want %d (every ready issue claimed exactly once, none lost)", len(claims), readyCount)
	}
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
