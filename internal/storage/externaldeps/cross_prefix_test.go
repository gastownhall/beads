package externaldeps

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// crossPrefixRig is a routed rig for the GH#4647 tests: the store a fake
// router hands out for its prefix, with a count of the opens and lookups the
// policy made.
type crossPrefixRig struct {
	store   storage.DoltStorage
	opens   int
	lookups int
	// routeErr fails the router lookup; openErr fails the route's Open.
	routeErr error
	openErr  error
}

// crossPrefixStore decorates raw with a router that sends every gt- ID to rig
// and answers ErrNoIssueRoute for any other prefix. Route warnings are
// recorded in the returned slice rather than printed.
func crossPrefixStore(raw *fakeStore, rig *crossPrefixRig) (*Store, *[]string) {
	router := func(_ context.Context, id string) (IssueRoute, error) {
		if types.ExtractPrefix(id) != "gt-" {
			return IssueRoute{}, ErrNoIssueRoute
		}
		rig.lookups++
		if rig.routeErr != nil {
			return IssueRoute{}, rig.routeErr
		}
		return IssueRoute{Key: "rig", Open: func(context.Context) (storage.DoltStorage, error) {
			rig.opens++
			if rig.openErr != nil {
				return nil, rig.openErr
			}
			return rig.store, nil
		}}, nil
	}
	store := New(raw, nil, nil, WithIssueRouter(router))
	var warnings []string
	store.warnRoute = func(prefix string) { warnings = append(warnings, prefix) }
	return store, &warnings
}

func readyIDs(t *testing.T, store *Store) []string {
	t.Helper()
	issues, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

// TestCrossPrefixBlockerExcludesSourceUntilTargetClosedOrPinned (GH#4647,
// AC-4): a routed open blocks or conditional-blocks target keeps its source
// out of ready, the ready count, the counted listing and claim; once the
// target is closed, or pinned, the next query admits the source with no write
// to the source's store.
func TestCrossPrefixBlockerExcludesSourceUntilTargetClosedOrPinned(t *testing.T) {
	for _, depType := range []types.DependencyType{types.DepBlocks, types.DepConditionalBlocks} {
		t.Run(string(depType), func(t *testing.T) {
			src, other := issue("hq-src"), issue("hq-other")
			target := issue("gt-target")
			raw := &fakeStore{
				ready: []*types.Issue{src, other},
				deps:  map[string][]*types.Dependency{src.ID: {externalDep(src.ID, target.ID, depType)}},
			}
			rig := &crossPrefixRig{store: &fakeStore{ready: []*types.Issue{target}}}
			store, warnings := crossPrefixStore(raw, rig)

			if got := readyIDs(t, store); !slices.Equal(got, []string{other.ID}) {
				t.Fatalf("ready with open %s = %v, want [%s]", target.ID, got, other.ID)
			}
			if n, err := store.CountReadyWork(t.Context(), types.WorkFilter{}); err != nil || n != 1 {
				t.Fatalf("CountReadyWork = %d, %v; want 1", n, err)
			}
			if rows, total, err := store.GetReadyWorkWithCountsAndTotal(t.Context(), types.WorkFilter{}); err != nil || total != 1 || len(rows) != 1 || rows[0].ID != other.ID {
				t.Fatalf("GetReadyWorkWithCountsAndTotal = %d rows, total %d, %v; want only %s", len(rows), total, err, other.ID)
			}
			claimed, err := store.ClaimReadyIssue(t.Context(), types.WorkFilter{}, "worker")
			if err != nil || claimed == nil || claimed.ID != other.ID {
				t.Fatalf("ClaimReadyIssue = %+v, %v; want %s", claimed, err, other.ID)
			}
			other.Assignee, other.Status = "", types.StatusOpen

			for _, status := range []types.Status{types.StatusClosed, types.StatusPinned} {
				target.Status = status
				if got := readyIDs(t, store); !slices.Equal(got, []string{src.ID, other.ID}) {
					t.Fatalf("ready with %s %s = %v, want [%s %s]", status, target.ID, got, src.ID, other.ID)
				}
			}
			if len(*warnings) != 0 {
				t.Fatalf("route warnings = %v, want none", *warnings)
			}
			if len(raw.closed) != 0 || len(raw.claimed) != 1 {
				t.Fatalf("source store writes: closed %v, claimed %v; want only the one claim", raw.closed, raw.claimed)
			}
		})
	}
}

// TestCrossPrefixBlockerBlocksInspectionCloseAndClaim (GH#4647, AC-5): an
// open routed cross-prefix blocker is named by blocked inspection and refuses
// an unforced close or claim of its source; a forced close goes through.
func TestCrossPrefixBlockerBlocksInspectionCloseAndClaim(t *testing.T) {
	src := issue("hq-src")
	target := issue("gt-target")
	lifecycle := &fakeLifecycle{}
	raw := &fakeStore{
		ready:     []*types.Issue{src},
		lifecycle: lifecycle,
		deps:      map[string][]*types.Dependency{src.ID: {externalDep(src.ID, target.ID, types.DepBlocks)}},
	}
	rig := &crossPrefixRig{store: &fakeStore{ready: []*types.Issue{target}}}
	store, _ := crossPrefixStore(raw, rig)

	blocked, blockers, err := store.IsBlocked(t.Context(), src.ID)
	if err != nil || !blocked || !slices.Equal(blockers, []string{target.ID}) {
		t.Fatalf("IsBlocked = %v, %v, %v; want true, [%s]", blocked, blockers, err, target.ID)
	}
	batch, err := store.IsBlockedBatch(t.Context(), []string{src.ID})
	if err != nil || !batch[src.ID] {
		t.Fatalf("IsBlockedBatch = %v, %v; want %s blocked", batch, err, src.ID)
	}
	listed, err := store.GetBlockedIssues(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != src.ID || !slices.Equal(listed[0].BlockedBy, []string{target.ID}) {
		t.Fatalf("GetBlockedIssues = %+v, want %s blocked by [%s]", listed, src.ID, target.ID)
	}

	if _, err := store.CloseIssueChecked(t.Context(), src.ID, "tester", storage.CloseIssueOptions{}); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("CloseIssueChecked error = %v, want ErrCloseBlocked", err)
	}
	ops, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle: %v", err)
	}
	if _, err := ops.Close(t.Context(), publicops.CloseRequest{IssueID: src.ID}); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("lifecycle Close error = %v, want ErrCloseBlocked", err)
	}
	if _, err := ops.Update(t.Context(), publicops.UpdateRequest{IssueID: src.ID, Claim: true}); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("lifecycle claim error = %v, want ErrCloseBlocked", err)
	}
	if lifecycle.closed != 0 || lifecycle.updated != 0 || len(raw.closed) != 0 {
		t.Fatalf("unforced writes reached the store: lifecycle close %d update %d, raw close %v", lifecycle.closed, lifecycle.updated, raw.closed)
	}

	if _, err := ops.Close(t.Context(), publicops.CloseRequest{IssueID: src.ID, Force: true}); err != nil {
		t.Fatalf("forced lifecycle Close: %v", err)
	}
	if _, err := store.CloseIssueChecked(t.Context(), src.ID, "tester", storage.CloseIssueOptions{Force: true}); err != nil {
		t.Fatalf("forced CloseIssueChecked: %v", err)
	}
	if lifecycle.closed != 1 || !slices.Equal(raw.closed, []string{src.ID}) {
		t.Fatalf("forced closes: lifecycle %d, raw %v; want one each", lifecycle.closed, raw.closed)
	}
}

// TestCrossPrefixNonBlockingEdgeTypesDoNotExclude (GH#4647, AC-6): a bare
// cross-prefix edge of any type but blocks / conditional-blocks — waits-for,
// related, relates-to among them — is not evaluated, even with an open target.
func TestCrossPrefixNonBlockingEdgeTypesDoNotExclude(t *testing.T) {
	for _, depType := range []types.DependencyType{types.DepWaitsFor, types.DepRelated, types.DepRelatesTo, types.DepDiscoveredFrom} {
		t.Run(string(depType), func(t *testing.T) {
			src := issue("hq-src")
			raw := &fakeStore{
				ready: []*types.Issue{src},
				deps:  map[string][]*types.Dependency{src.ID: {externalDep(src.ID, "gt-target", depType)}},
			}
			rig := &crossPrefixRig{store: &fakeStore{ready: []*types.Issue{issue("gt-target")}}}
			store, _ := crossPrefixStore(raw, rig)

			if got := readyIDs(t, store); !slices.Equal(got, []string{src.ID}) {
				t.Fatalf("ready = %v, want [%s]", got, src.ID)
			}
			if blocked, blockers, err := store.IsBlocked(t.Context(), src.ID); err != nil || blocked {
				t.Fatalf("IsBlocked = %v, %v, %v; want false", blocked, blockers, err)
			}
			if rig.lookups != 0 {
				t.Fatalf("router lookups = %d, want 0", rig.lookups)
			}
		})
	}
}

// TestCrossPrefixUnroutedOrAbsentTargetDoesNotBlock (GH#4647, AC-7): a target
// with no route, one routed to a store this process cannot read issues from
// (a proxied rig), and one the owning store does not hold are not blockers,
// and none of them prints the unavailable-route warning.
func TestCrossPrefixUnroutedOrAbsentTargetDoesNotBlock(t *testing.T) {
	cases := map[string]struct {
		target string
		rig    *crossPrefixRig
	}{
		"no route":        {target: "zz-target", rig: &crossPrefixRig{store: &fakeStore{}}},
		"unsupported":     {target: "gt-target", rig: &crossPrefixRig{store: &fakeStore{}, openErr: errors.Join(ErrNoIssueRoute, errors.New("proxied rig"))}},
		"absent in owner": {target: "gt-missing", rig: &crossPrefixRig{store: &fakeStore{ready: []*types.Issue{issue("gt-target")}}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			src := issue("hq-src")
			raw := &fakeStore{
				ready: []*types.Issue{src},
				deps:  map[string][]*types.Dependency{src.ID: {externalDep(src.ID, tc.target, types.DepBlocks)}},
			}
			store, warnings := crossPrefixStore(raw, tc.rig)

			if got := readyIDs(t, store); !slices.Equal(got, []string{src.ID}) {
				t.Fatalf("ready = %v, want [%s]", got, src.ID)
			}
			if blocked, blockers, err := store.IsBlocked(t.Context(), src.ID); err != nil || blocked {
				t.Fatalf("IsBlocked = %v, %v, %v; want false", blocked, blockers, err)
			}
			if len(*warnings) != 0 {
				t.Fatalf("route warnings = %v, want none", *warnings)
			}
		})
	}
}
