package externaldeps

import (
	"context"
	"reflect"
	"slices"
	"testing"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestWrapUOWProviderBlockedListerNamesExternalBlockers pins the blocked
// listing on the composition every `bd serve` answers from: the unit-of-work
// provider wrapped by this package (cmd/bd's wireExternalDependencyUOWProvider)
// handing out BlockedLister through its own accessor. The server advertises
// policy.external_dependencies for this composition, so its listing is the one
// a client stands down to, and it must already carry the external refs:
//
//   - a row blocked ONLY by an unsatisfied external ref is listed, with the
//     ref as its blocker (the undecorated listing never names it, because the
//     stored blocked flag knows nothing of other projects);
//   - a row with a local blocker AND an external ref names both.
//
// A lister built over the inner provider rather than this one would lose
// both, and gc reads the missing row as unblocked.
func TestWrapUOWProviderBlockedListerNamesExternalBlockers(t *testing.T) {
	local, mixed, extOnly := issue("be-local"), issue("be-mixed"), issue("be-ext")
	mixed.Priority, extOnly.Priority = 1, 2
	inner := &fakeUOW{
		issues: &fakeIssueUseCase{
			ready: []*types.Issue{local, mixed, extOnly},
			blocked: []*types.BlockedIssue{
				{Issue: *mixed, BlockedByCount: 1, BlockedBy: []string{local.ID}},
			},
		},
		deps: &fakeDependencyUseCase{
			external: map[string][]*types.Dependency{
				mixed.ID:   {externalDep(mixed.ID, "external:remote:billing", types.DepBlocks)},
				extOnly.ID: {externalDep(extOnly.ID, "external:remote:payments", types.DepBlocks)},
			},
		},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	source, ok := provider.(uow.BlockedListerSource)
	if !ok {
		t.Fatalf("wrapped provider %T does not offer BlockedLister", provider)
	}
	lister, err := source.BlockedLister()
	if err != nil {
		t.Fatalf("BlockedLister: %v", err)
	}
	result, err := lister.ListBlocked(t.Context(), publicops.BlockedRequest{})
	if err != nil {
		t.Fatalf("ListBlocked: %v", err)
	}
	got := map[string][]string{}
	var order []string
	for _, item := range result.Items {
		blockers := slices.Clone(item.BlockedBy)
		slices.Sort(blockers)
		got[item.ID] = blockers
		order = append(order, item.ID)
		if item.BlockedByCount != len(item.BlockedBy) {
			t.Errorf("%s: BlockedByCount = %d, len(BlockedBy) = %d", item.ID, item.BlockedByCount, len(item.BlockedBy))
		}
	}
	want := map[string][]string{
		mixed.ID:   {local.ID, "external:remote:billing"},
		extOnly.ID: {"external:remote:payments"},
	}
	slices.Sort(want[mixed.ID])
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listing = %v, want %v", got, want)
	}
	if !slices.Equal(order, []string{mixed.ID, extOnly.ID}) {
		t.Errorf("order = %v, want priority order [%s %s]", order, mixed.ID, extOnly.ID)
	}
}

// externalOnlyLabelCases are label filters over two rows blocked ONLY by an
// unsatisfied external ref (externalOnlyLabelRows): be-tagged carries lane:a
// and team:x, be-bare carries no label. The undecorated listing, whose SQL
// applies the label clauses, never sees either row; the decoration adds them
// after that query ran, so each want is what those clauses answer for a row
// the query lists itself.
var externalOnlyLabelCases = []struct {
	name string
	req  publicops.BlockedRequest
	want []string
}{
	{"no filter", publicops.BlockedRequest{}, []string{"be-tagged", "be-bare"}},
	{"label", publicops.BlockedRequest{Labels: []string{"lane:a"}}, []string{"be-tagged"}},
	{"labels are all required", publicops.BlockedRequest{Labels: []string{"lane:a", "team:y"}}, nil},
	{"label matches exactly", publicops.BlockedRequest{Labels: []string{"Lane:A"}}, nil},
	{"label_any", publicops.BlockedRequest{LabelsAny: []string{"lane:b", "team:x"}}, []string{"be-tagged"}},
	{"label_any matching none", publicops.BlockedRequest{LabelsAny: []string{"lane:b"}}, nil},
	{"exclude_label", publicops.BlockedRequest{ExcludeLabels: []string{"lane:a"}}, []string{"be-bare"}},
	{"exclude_label matching none", publicops.BlockedRequest{ExcludeLabels: []string{"lane:b"}}, []string{"be-tagged", "be-bare"}},
	{"label and exclude_label", publicops.BlockedRequest{Labels: []string{"lane:a"}, ExcludeLabels: []string{"team:x"}}, nil},
}

func externalOnlyLabelRows() ([]*types.Issue, map[string][]*types.Dependency) {
	tagged, bare := issue("be-tagged"), issue("be-bare")
	tagged.Priority, bare.Priority = 1, 2
	tagged.Labels = []string{"lane:a", "team:x"}
	return []*types.Issue{tagged, bare}, map[string][]*types.Dependency{
		tagged.ID: {externalDep(tagged.ID, "external:remote:payments", types.DepBlocks)},
		bare.ID:   {externalDep(bare.ID, "external:remote:payments", types.DepBlocks)},
	}
}

// TestBlockedListingFiltersExternalOnlyRowsByLabel pins the label filters on
// the rows the decoration adds, on both of this package's compositions, each
// read through the role and through the legacy GetBlockedIssues door: the
// store decorator (the direct and embedded routes, a store-shaped bd serve,
// and the http client's own decoration, which shares decorateBlocked) and the
// unit-of-work decorator (the proxied route and a provider-shaped bd serve,
// whose plain by-id reads hydrate no labels at all).
func TestBlockedListingFiltersExternalOnlyRowsByLabel(t *testing.T) {
	t.Run("store", func(t *testing.T) {
		rows, deps := externalOnlyLabelRows()
		store := testStore(&fakeStore{ready: rows, deps: deps}, &fakeStore{}, false)
		lister, err := store.BlockedLister()
		if err != nil {
			t.Fatalf("BlockedLister: %v", err)
		}
		runExternalOnlyLabelCases(t, lister, store.GetBlockedIssues)
	})
	t.Run("unit of work", func(t *testing.T) {
		rows, deps := externalOnlyLabelRows()
		provider := WrapUOWProvider(&fakeUOWProvider{uw: &fakeUOW{
			issues: &fakeIssueUseCase{ready: rows},
			deps:   &fakeDependencyUseCase{external: deps},
		}}, func(ProjectName) (string, bool) { return "", false }, nil)
		source, ok := provider.(uow.BlockedListerSource)
		if !ok {
			t.Fatalf("wrapped provider %T does not offer BlockedLister", provider)
		}
		lister, err := source.BlockedLister()
		if err != nil {
			t.Fatalf("BlockedLister: %v", err)
		}
		uw, err := provider.NewUOW(t.Context())
		if err != nil {
			t.Fatalf("NewUOW: %v", err)
		}
		runExternalOnlyLabelCases(t, lister, uw.IssueUseCase().GetBlockedIssues)
	})
}

func runExternalOnlyLabelCases(t *testing.T, lister publicops.BlockedLister, legacy func(context.Context, types.WorkFilter) ([]*types.BlockedIssue, error)) {
	t.Helper()
	for _, tc := range externalOnlyLabelCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := lister.ListBlocked(t.Context(), tc.req)
			if err != nil {
				t.Fatalf("ListBlocked: %v", err)
			}
			if ids := blockedIssueIDs(result.Items); !slices.Equal(ids, tc.want) {
				t.Errorf("ListBlocked = %v, want %v", ids, tc.want)
			}
			items, err := legacy(t.Context(), storageissueops.BlockedFilter(tc.req))
			if err != nil {
				t.Fatalf("GetBlockedIssues: %v", err)
			}
			if ids := blockedIssueIDs(items); !slices.Equal(ids, tc.want) {
				t.Errorf("GetBlockedIssues = %v, want %v", ids, tc.want)
			}
		})
	}
}
