package externaldeps

import (
	"context"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
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
