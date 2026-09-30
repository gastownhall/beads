package externaldeps

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi/storereader"
	"github.com/steveyegge/beads/internal/workapi/storereadycounter"
	"github.com/steveyegge/beads/internal/workapi/storereadylister"
	publicops "github.com/steveyegge/beads/issueops"
)

// policyWorkspace is one local workspace with every shape of external edge
// the policy distinguishes:
//
//	be-sat     blocked by a capability the foreign project has shipped
//	be-unsat   blocked by a capability nobody has shipped
//	be-bad     blocked by a malformed ref (fails closed)
//	be-track   a non-blocking (tracks) external edge
//	be-plain   no external edge
//	be-unconf  blocked by an unconfigured project (fails closed)
//
// So the ready set under the policy is exactly [be-sat be-track be-plain].
func policyWorkspace() (raw, foreign *fakeStore) {
	ids := []string{"be-sat", "be-unsat", "be-bad", "be-track", "be-plain", "be-unconf"}
	ready := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		ready = append(ready, issue(id))
	}
	raw = &fakeStore{
		ready: ready,
		deps: map[string][]*types.Dependency{
			"be-sat":    {externalDep("be-sat", "external:remote:shipped", types.DepBlocks)},
			"be-unsat":  {externalDep("be-unsat", "external:remote:payments", types.DepBlocks)},
			"be-bad":    {externalDep("be-bad", "external:malformed", types.DepBlocks)},
			"be-track":  {externalDep("be-track", "external:remote:payments", types.DepTracks)},
			"be-unconf": {externalDep("be-unconf", "external:elsewhere:thing", types.DepWaitsFor)},
		},
	}
	foreign = &fakeStore{labels: map[string][]*types.Issue{
		"provides:shipped": {{ID: "remote-done", Status: types.StatusClosed}},
	}}
	return raw, foreign
}

var policyReadySet = []string{"be-sat", "be-track", "be-plain"}

func pageIDs(page publicops.IssuePage) []string {
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

func intPtr(v int) *int { return &v }

// TestReadyRolesMatchTheDecoratorBeforeTheMove is the before/after
// equivalence: the ready roles used to be built over the decorator (the
// store-backed bodies calling its filtered store methods); they now narrow the
// request and delegate to the inner store's roles. For every request shape the
// two must answer the same page, the same count and the same claim.
func TestReadyRolesMatchTheDecoratorBeforeTheMove(t *testing.T) {
	requests := map[string]publicops.ReadyRequest{
		"defaults":          {Sort: "priority"},
		"unlimited":         {Sort: "priority", Limit: intPtr(0)},
		"first page":        {Sort: "priority", Limit: intPtr(1)},
		"second page":       {Sort: "priority", Limit: intPtr(1), Offset: 1},
		"caller exclusions": {Sort: "priority", Limit: intPtr(0), ExcludeIDs: []string{"be-plain", "be-unsat"}},
	}
	for name, req := range requests {
		t.Run(name, func(t *testing.T) {
			raw, foreign := policyWorkspace()
			store := testStore(raw, foreign, true)

			before, err := storereader.New(store)
			if err != nil {
				t.Fatal(err)
			}
			after, err := store.IssueReader()
			if err != nil {
				t.Fatal(err)
			}
			want, err := before.Ready(t.Context(), req)
			if err != nil {
				t.Fatalf("before Ready: %v", err)
			}
			got, err := after.Ready(t.Context(), req)
			if err != nil {
				t.Fatalf("after Ready: %v", err)
			}
			if !slices.Equal(pageIDs(got), pageIDs(want)) || got.HasMore != want.HasMore {
				t.Fatalf("Ready = %v (more=%v), before the move %v (more=%v)", pageIDs(got), got.HasMore, pageIDs(want), want.HasMore)
			}

			count := req
			count.Limit, count.Offset = nil, 0
			beforeCounter, err := storereadycounter.New(store)
			if err != nil {
				t.Fatal(err)
			}
			afterCounter, err := store.ReadyCounter()
			if err != nil {
				t.Fatal(err)
			}
			wantTotal, err := beforeCounter.CountReady(t.Context(), count)
			if err != nil {
				t.Fatalf("before CountReady: %v", err)
			}
			gotTotal, err := afterCounter.CountReady(t.Context(), count)
			if err != nil {
				t.Fatalf("after CountReady: %v", err)
			}
			if gotTotal != wantTotal {
				t.Fatalf("CountReady = %d, before the move %d", gotTotal.Total, wantTotal.Total)
			}

			// The listing: before = the single-pass body over the decorator's
			// store-level GetReadyWorkWithCountsAndTotal override (what `bd
			// ready --json` called); after = the role narrowed and delegated.
			beforeLister, err := storereadylister.New(store)
			if err != nil {
				t.Fatal(err)
			}
			afterLister, err := store.ReadyLister()
			if err != nil {
				t.Fatal(err)
			}
			wantListing, err := beforeLister.ListReady(t.Context(), publicops.ReadyListRequest{ReadyRequest: req})
			if err != nil {
				t.Fatalf("before ListReady: %v", err)
			}
			gotListing, err := afterLister.ListReady(t.Context(), publicops.ReadyListRequest{ReadyRequest: req})
			if err != nil {
				t.Fatalf("after ListReady: %v", err)
			}
			if !slices.Equal(pageIDs(publicops.IssuePage{Items: gotListing.Items}), pageIDs(publicops.IssuePage{Items: wantListing.Items})) ||
				gotListing.HasMore != wantListing.HasMore || gotListing.Total != wantListing.Total {
				t.Fatalf("ListReady = %+v, before the move %+v", gotListing, wantListing)
			}
			if !slices.Equal(pageIDs(publicops.IssuePage{Items: gotListing.Items}), pageIDs(got)) || gotListing.HasMore != got.HasMore {
				t.Fatalf("ListReady page = %v (more=%v), Ready = %v (more=%v)",
					pageIDs(publicops.IssuePage{Items: gotListing.Items}), gotListing.HasMore, pageIDs(got), got.HasMore)
			}
			if gotListing.Total != gotTotal.Total {
				t.Fatalf("ListReady Total = %d, CountReady = %d", gotListing.Total, gotTotal.Total)
			}
		})
	}

	// The oracle itself, so the equivalence is not two copies of one mistake.
	raw, foreign := policyWorkspace()
	reader, err := testStore(raw, foreign, true).IssueReader()
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.Ready(t.Context(), publicops.ReadyRequest{Sort: "priority", Limit: intPtr(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pageIDs(page), policyReadySet) {
		t.Fatalf("policy ready set = %v, want %v", pageIDs(page), policyReadySet)
	}
}

// TestReadyClaimerMatchesTheDecoratorBeforeTheMove claims through the role
// and through the decorator's store-level ClaimReadyIssue (what the role used
// to call) on twin workspaces, for requests whose winner differs.
func TestReadyClaimerMatchesTheDecoratorBeforeTheMove(t *testing.T) {
	for name, exclude := range map[string][]string{
		"front row":             nil,
		"caller excluded front": {"be-sat"},
		"everything excluded":   policyReadySet,
	} {
		t.Run(name, func(t *testing.T) {
			rawBefore, foreignBefore := policyWorkspace()
			before := testStore(rawBefore, foreignBefore, true)
			rawAfter, foreignAfter := policyWorkspace()
			claimer, err := testStore(rawAfter, foreignAfter, true).ReadyClaimer()
			if err != nil {
				t.Fatal(err)
			}
			want, err := before.ClaimReadyIssue(t.Context(), types.WorkFilter{
				Status: types.StatusOpen, SortPolicy: types.SortPolicyPriority, ExcludeIDs: slices.Clone(exclude),
			}, "worker")
			if err != nil {
				t.Fatalf("before claim: %v", err)
			}
			got, err := claimer.ClaimNext(t.Context(), publicops.ClaimNextRequest{
				Actor: "worker", Filter: publicops.ReadyRequest{Sort: "priority", ExcludeIDs: exclude},
			})
			if err != nil {
				t.Fatalf("after claim: %v", err)
			}
			if (want == nil) != (got.Claimed == nil) || (want != nil && want.ID != got.Claimed.ID) {
				t.Fatalf("claimed %v, before the move %v", got.Claimed, want)
			}
			if !slices.Equal(rawAfter.claimed, rawBefore.claimed) {
				t.Fatalf("claimed %v, before the move %v", rawAfter.claimed, rawBefore.claimed)
			}
		})
	}
}

// TestReadyRolesReadEdgesOncePerCall pins the cost: one exclusion query per
// role call, and none for a request the role refuses on validation.
func TestReadyRolesReadEdgesOncePerCall(t *testing.T) {
	raw, foreign := policyWorkspace()
	store := testStore(raw, foreign, true)
	reader, err := store.IssueReader()
	if err != nil {
		t.Fatal(err)
	}
	counter, err := store.ReadyCounter()
	if err != nil {
		t.Fatal(err)
	}
	claimer, err := store.ReadyClaimer()
	if err != nil {
		t.Fatal(err)
	}
	lister, err := store.ReadyLister()
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"ListReady": func() error {
			_, err := lister.ListReady(t.Context(), publicops.ReadyListRequest{ReadyRequest: publicops.ReadyRequest{Sort: "priority", Limit: intPtr(1)}})
			return err
		},
		"Ready": func() error {
			_, err := reader.Ready(t.Context(), publicops.ReadyRequest{Sort: "priority"})
			return err
		},
		"CountReady": func() error {
			_, err := counter.CountReady(t.Context(), publicops.ReadyRequest{Sort: "priority"})
			return err
		},
		"ClaimNext": func() error {
			_, err := claimer.ClaimNext(t.Context(), publicops.ClaimNextRequest{Actor: "w", Filter: publicops.ReadyRequest{Sort: "priority"}})
			return err
		},
	}
	for name, call := range calls {
		before := raw.edgeReads
		if err := call(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := raw.edgeReads - before; got != 1 {
			t.Errorf("%s read the external edges %d times, want exactly 1", name, got)
		}
	}

	invalid := map[string]func() error{
		"ListReady": func() error {
			_, err := lister.ListReady(t.Context(), publicops.ReadyListRequest{ReadyRequest: publicops.ReadyRequest{Sort: "bogus"}})
			return err
		},
		"Ready": func() error {
			_, err := reader.Ready(t.Context(), publicops.ReadyRequest{Sort: "bogus"})
			return err
		},
		"CountReady": func() error {
			_, err := counter.CountReady(t.Context(), publicops.ReadyRequest{Sort: "priority", Limit: intPtr(1)})
			return err
		},
		"ClaimNext": func() error {
			_, err := claimer.ClaimNext(t.Context(), publicops.ClaimNextRequest{Filter: publicops.ReadyRequest{Sort: "priority"}})
			return err
		},
	}
	for name, call := range invalid {
		before := raw.edgeReads
		if err := call(); !errors.Is(err, publicops.ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
		if got := raw.edgeReads - before; got != 0 {
			t.Errorf("invalid %s read the external edges %d times, want 0", name, got)
		}
	}
}

// TestReadyRolesDoNotMutateTheCallerRequest: the exclusions go on a clone.
func TestReadyRolesDoNotMutateTheCallerRequest(t *testing.T) {
	raw, foreign := policyWorkspace()
	store := testStore(raw, foreign, true)
	reader, err := store.IssueReader()
	if err != nil {
		t.Fatal(err)
	}
	callerIDs := make([]string, 1, 8) // spare capacity an append would write into
	callerIDs[0] = "be-plain"
	req := publicops.ReadyRequest{Sort: "priority", ExcludeIDs: callerIDs}
	if _, err := reader.Ready(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(req.ExcludeIDs, []string{"be-plain"}) || !slices.Equal(callerIDs[:cap(callerIDs)][:2], []string{"be-plain", ""}) {
		t.Fatalf("caller's ExcludeIDs written through: %v (backing %v)", req.ExcludeIDs, callerIDs[:cap(callerIDs)])
	}
}

type serverEnforcedStore struct {
	*fakeStore
	enforced bool
}

func (s *serverEnforcedStore) PolicyEnforcedByServer() bool { return s.enforced }

// TestWrapSkipsOnlyServerEnforcedStores: a client whose server applies the
// policy is returned unchanged even beneath another decorator; everything
// else — including a store that implements the marker but answers false —
// is wrapped, so an unknown backend fails closed.
func TestWrapSkipsOnlyServerEnforcedStores(t *testing.T) {
	remote := &serverEnforcedStore{fakeStore: &fakeStore{}, enforced: true}
	if got := Wrap(remote, nil, nil); got != storage.DoltStorage(remote) {
		t.Fatalf("Wrap(server-enforced) = %T, want the store unchanged", got)
	}
	hooked := storage.NewHookFiringStore(remote, nil)
	if got := Wrap(hooked, nil, nil); got != storage.DoltStorage(hooked) {
		t.Fatalf("Wrap(hooks over server-enforced) = %T, want the chain unchanged", got)
	}
	for name, store := range map[string]storage.DoltStorage{
		"local":             &fakeStore{},
		"marker says false": &serverEnforcedStore{fakeStore: &fakeStore{}},
	} {
		if _, ok := Wrap(store, nil, nil).(*Store); !ok {
			t.Errorf("Wrap(%s) did not install the policy", name)
		}
	}
	if Wrap(nil, nil, nil) != nil {
		t.Error("Wrap(nil) != nil")
	}
}

// TestBatchCloserReadsNoEdgesWhenForcedWithoutAClaim: a forced batch skips the
// close guard and, with no claim to narrow, has no use for the exclusions, so
// it must not pay any edge read. A batch that earns a claim reads the
// workspace's edges exactly once (the claim may land on any ready issue); an
// unforced one without a claim reads only its items' own edges, once.
func TestBatchCloserReadsNoEdgesWhenForcedWithoutAClaim(t *testing.T) {
	raw, foreign := policyWorkspace()
	closer, err := testStore(raw, foreign, true).BatchCloser()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		req   publicops.CloseBatchRequest
		reads int
		own   int
	}{
		{"forced, no claim", publicops.CloseBatchRequest{Actor: "w", Force: true, Items: []publicops.BatchCloseItem{{IssueID: "be-unsat"}}}, 0, 0},
		{"forced, claim", publicops.CloseBatchRequest{Actor: "w", Force: true, Items: []publicops.BatchCloseItem{{IssueID: "be-unsat"}}, ClaimNext: &publicops.ReadyRequest{Sort: "priority"}}, 1, 0},
		{"unforced", publicops.CloseBatchRequest{Actor: "w", Items: []publicops.BatchCloseItem{{IssueID: "be-plain"}}}, 0, 1},
		{"unforced, claim", publicops.CloseBatchRequest{Actor: "w", Items: []publicops.BatchCloseItem{{IssueID: "be-plain"}}, ClaimNext: &publicops.ReadyRequest{Sort: "priority"}}, 1, 0},
	} {
		before, ownBefore := raw.edgeReads, raw.ownReads
		if _, err := closer.CloseBatch(t.Context(), tc.req); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := raw.edgeReads - before; got != tc.reads {
			t.Errorf("%s read the workspace's external edges %d times, want %d", tc.name, got, tc.reads)
		}
		if got := raw.ownReads - ownBefore; got != tc.own {
			t.Errorf("%s read its items' own edges %d times, want %d", tc.name, got, tc.own)
		}
	}
}
