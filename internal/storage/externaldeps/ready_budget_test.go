package externaldeps

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestProxiedReadyListingUnitOfWorkBudget pins how many units of work the
// proxied `bd ready` listing opens through the policy provider bd composes
// (WrapUOWProvider over the proxied provider): THREE, whether or not the page
// is truncated —
//
//  1. the policy's read-only read of the workspace's external edges, before
//     the listing, so the foreign projects are resolved with no transaction
//     open and the listing runs on a role that applies no policy of its own;
//  2. the advisory defer-wake sweep ReadyLister runs before its read;
//  3. the listing itself: the page and, when rows were hidden, its total, in
//     one read-only unit of work.
//
// gc's `bd ready --json --include-ephemeral --limit 0` ran two before the
// policy moved to the role wrappers (the wake, then a listing that applied
// the policy inside it), so this is one MORE, not one fewer; the CHANGELOG
// says so. A change that folds the edge read into the listing must lower this
// budget deliberately, not by accident.
func TestProxiedReadyListingUnitOfWorkBudget(t *testing.T) {
	zero, one := 0, 1
	for _, tc := range []struct {
		name string
		req  publicops.ReadyRequest
	}{
		{"gc: --include-ephemeral --limit 0", publicops.ReadyRequest{Sort: "priority", IncludeEphemeral: true, Limit: &zero}},
		{"truncated page with its total", publicops.ReadyRequest{Sort: "priority", Limit: &one}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocked, a, b := issue("be-blocked"), issue("be-a"), issue("be-b")
			deps := &countingDependencyUseCase{fakeDependencyUseCase: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
				blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
			}}}
			counting := &countingUOWProvider{openCountingProvider: &openCountingProvider{uw: &fakeUOW{
				issues: &fakeIssueUseCase{ready: []*types.Issue{blocked, a, b}},
				deps:   deps,
			}}}
			provider := WrapUOWProvider(counting, func(ProjectName) (string, bool) { return "", false }, nil)
			lister, err := provider.(uow.ReadyListerSource).ReadyLister()
			if err != nil {
				t.Fatal(err)
			}
			listing, err := lister.ListReady(t.Context(), publicops.ReadyListRequest{ReadyRequest: tc.req})
			if err != nil {
				t.Fatalf("ListReady: %v", err)
			}
			for _, item := range listing.Items {
				if item.ID == blocked.ID {
					t.Fatalf("listing includes externally blocked %s", blocked.ID)
				}
			}
			if listing.Total != 2 {
				t.Errorf("total = %d, want 2 (the blocked row excluded)", listing.Total)
			}
			if counting.opened != 3 {
				t.Errorf("units of work opened = %d, want 3 (edge read, defer wake, listing)", counting.opened)
			}
			if counting.open != 0 {
				t.Errorf("%d units of work left open", counting.open)
			}
			assertReads(t, "ListReady", deps, 1)
		})
	}
}
