package conformance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract every implementation of
// publicops.ReadyLister must satisfy. Each case asserts what the leaf
// (issueops/readylister.go, ReadyLister and ReadyListing) PROMISES, cited by
// symbol, rather than what any one backend happens to do.
//
// THERE ARE TWO BODIES BEHIND THE THREE WIRINGS. dolt and embeddeddolt share
// internal/workapi/storereadylister, which takes the page and the total from
// ONE store call — the total rides the page's ID query as a window count — and
// reads has-more off that total; the unit-of-work provider pages through the
// domain seam and, only when the page was truncated, counts the unbounded page
// in the same unit of work. So the wirings are one vote plus an engine check on
// the single-pass body and a second, independent vote on the other.
//
// THE ROLE'S WHOLE CONTRACT IS THREE IDENTITIES with its two siblings, which is
// why the fixture carries the SAME backend's Reader and ReadyCounter: Items and
// HasMore are Reader.Ready's, Total is CountReady's over the unpaged request.
// A listing whose total describes a different set than its page is the defect
// this role exists to make impossible, so the identity case walks a grid of
// limits and offsets rather than one page.
//
// Every case is scoped by a label and names a sort policy, for the reasons the
// ReadyCounter contract gives (the ready front is a property of the whole
// database, and the empty sort is not a policy a front door may rely on).

// ReadyListerFixture supplies adapter-specific storage access for the
// ready-listing assertions.
type ReadyListerFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// ReadyLister is the surface under test.
	ReadyLister publicops.ReadyLister
	// Reader and ReadyCounter are the SAME backend's sibling accessors: the
	// listing's promise is an identity with each of them.
	Reader       publicops.Reader
	ReadyCounter publicops.ReadyCounter
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CreateWisp seeds an ephemeral issue in the wisps plane.
	CreateWisp func(context.Context, *types.Issue, string) error
	// AddDependency seeds ONE edge, which is how a case takes a row OFF the
	// ready front without deleting or closing it.
	AddDependency func(context.Context, *types.Dependency, string) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case
	// that needs it SKIPS with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
}

const readyListerSort = "priority"

// RunReadyListerAgreesWithReadyAndCountReady pins the three identities that ARE
// this role (ReadyLister's doc): for every request, Items and HasMore equal
// Reader.Ready's for the same ReadyRequest, and Total equals
// CountReady(same request, Limit and Offset unset).
//
// The grid crosses every page shape a caller can ask for — unlimited, a limit
// smaller than the set, exactly the set, larger than the set — with offsets
// before, inside and past the end of the set. The blocked row and the outsider
// are ready-predicate and label-predicate traps: a total assembled over a
// different predicate than the page counts one of them.
func RunReadyListerAgreesWithReadyAndCountReady(t *testing.T, ctx context.Context, fixture ReadyListerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rleq"
	ids := readyListerIDs(fixture, "rleq", 5)
	for i, id := range ids {
		seedReadyListerIssue(t, ctx, fixture, readyListerIssue(id, i%4, label))
	}
	blocked := fixture.IssuePrefix + "-rleq-blocked"
	seedReadyListerIssue(t, ctx, fixture, readyListerIssue(blocked, 0, label))
	if err := fixture.AddDependency(ctx, &types.Dependency{IssueID: blocked, DependsOnID: ids[0], Type: types.DepBlocks}, "seed"); err != nil {
		t.Fatalf("seed blocker edge: %v", err)
	}
	seedReadyListerIssue(t, ctx, fixture, readyListerIssue(fixture.IssuePrefix+"-rleq-outsider", 0, label+"-other"))

	base := publicops.ReadyRequest{Labels: []string{label}, Sort: readyListerSort}
	count, err := fixture.ReadyCounter.CountReady(ctx, base)
	if err != nil {
		t.Fatalf("CountReady: %v", err)
	}
	if count.Total != int64(len(ids)) {
		t.Fatalf("CountReady = %d for the %d ready rows this case seeded; the identity below would be with the wrong set", count.Total, len(ids))
	}

	for _, limit := range []int{0, 1, 2, len(ids), len(ids) + 2} {
		for _, offset := range []int{0, 1, 3, len(ids), len(ids) + 1} {
			t.Run(fmt.Sprintf("limit=%d/offset=%d", limit, offset), func(t *testing.T) {
				req := base
				req.Limit = &limit
				req.Offset = offset
				page, err := fixture.Reader.Ready(ctx, req)
				if err != nil {
					t.Fatalf("Reader.Ready: %v", err)
				}
				listing, err := fixture.ReadyLister.ListReady(ctx, publicops.ReadyListRequest{ReadyRequest: req})
				if err != nil {
					t.Fatalf("ListReady: %v", err)
				}
				if got, want := readyListerItemIDs(listing.Items), readyListerItemIDs(page.Items); !slices.Equal(got, want) {
					t.Errorf("ListReady items = %v, Reader.Ready items = %v", got, want)
				}
				if listing.HasMore != page.HasMore {
					t.Errorf("ListReady HasMore = %v, Reader.Ready HasMore = %v", listing.HasMore, page.HasMore)
				}
				if listing.Total != count.Total {
					t.Errorf("ListReady Total = %d, CountReady(unpaged) = %d: the total must size the set the page was cut from", listing.Total, count.Total)
				}
				if listing.Items == nil {
					t.Errorf("ListReady Items = nil, want a non-nil slice for a successful call")
				}
			})
		}
	}
}

// RunReadyListerTotalCountsTheWispPlaneItAdmits pins the identity across the
// ephemeral gate: a request that admits wisps pages AND counts them, one that
// does not does neither. The single-pass body sums a window count per plane and
// subtracts their overlap, which is arithmetic the page never does, so the wisp
// tier is where the total can come apart from the listing.
func RunReadyListerTotalCountsTheWispPlaneItAdmits(t *testing.T, ctx context.Context, fixture ReadyListerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rlwisp"
	durable := readyListerIDs(fixture, "rlwisp-d", 2)
	wisps := readyListerIDs(fixture, "rlwisp-w", 2)
	for _, id := range durable {
		seedReadyListerIssue(t, ctx, fixture, readyListerIssue(id, 1, label))
	}
	for _, id := range wisps {
		issue := readyListerIssue(id, 1, label)
		issue.Ephemeral = true
		if err := fixture.CreateWisp(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed wisp %s: %v", id, err)
		}
	}
	one := 1
	for _, tc := range []struct {
		includeEphemeral bool
		want             int64
	}{{false, 2}, {true, 4}} {
		req := publicops.ReadyRequest{Labels: []string{label}, Sort: readyListerSort, IncludeEphemeral: tc.includeEphemeral, Limit: &one}
		listing, err := fixture.ReadyLister.ListReady(ctx, publicops.ReadyListRequest{ReadyRequest: req})
		if err != nil {
			t.Fatalf("ListReady(includeEphemeral=%v): %v", tc.includeEphemeral, err)
		}
		if listing.Total != tc.want || len(listing.Items) != 1 || !listing.HasMore {
			t.Errorf("ListReady(includeEphemeral=%v, limit=1) = %d items, HasMore=%v, Total=%d; want 1 item, HasMore, Total=%d",
				tc.includeEphemeral, len(listing.Items), listing.HasMore, listing.Total, tc.want)
		}
	}
}

// RunReadyListerHonorsExcludeIDs pins ReadyRequest.ExcludeIDs on the listing:
// the excluded rows leave the page — from the FRONT of a bounded page, where a
// post-fetch filter would leave it short — AND the total. This is the field the
// external-capability policy narrows every ready role through, so a listing
// that paged the narrowed set but counted the unnarrowed one would publish
// "Showing 1 of 4" over a queue of 2.
func RunReadyListerHonorsExcludeIDs(t *testing.T, ctx context.Context, fixture ReadyListerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rlexcl"
	ids := readyListerIDs(fixture, "rlexcl", 4)
	for i, id := range ids {
		seedReadyListerIssue(t, ctx, fixture, readyListerIssue(id, i, label))
	}
	build := func() publicops.ReadyListRequest {
		one := 1
		return publicops.ReadyListRequest{ReadyRequest: publicops.ReadyRequest{
			Labels:     []string{label},
			Sort:       readyListerSort,
			Limit:      &one,
			ExcludeIDs: []string{ids[0], ids[2], fixture.IssuePrefix + "-rlexcl-absent"},
		}}
	}
	request := build()
	listing, err := fixture.ReadyLister.ListReady(ctx, request)
	if err != nil {
		t.Fatalf("ListReady: %v", err)
	}
	if got := readyListerItemIDs(listing.Items); !slices.Equal(got, []string{ids[1]}) {
		t.Errorf("ListReady with ExcludeIDs and Limit=1 listed %v, want [%s]", got, ids[1])
	}
	if listing.Total != 2 || !listing.HasMore {
		t.Errorf("ListReady with ExcludeIDs: Total=%d HasMore=%v, want Total=2 HasMore=true", listing.Total, listing.HasMore)
	}
	if !reflect.DeepEqual(request, build()) {
		t.Errorf("ListReady mutated the caller's ExcludeIDs: got %v", request.ExcludeIDs)
	}
}

// RunReadyListerRefusesPastTheRowCap pins ReadyListRequest.MaxRows: a listing
// that would materialize more rows than the cap allows is
// *ErrTooManyRows and no listing, while a page the cap does not bound —
// a Limit at or under it — is an ordinary truncated page whose Total still
// sizes the whole set. The cap bounds rows MATERIALIZED, never the count.
func RunReadyListerRefusesPastTheRowCap(t *testing.T, ctx context.Context, fixture ReadyListerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rlcap"
	for i, id := range readyListerIDs(fixture, "rlcap", 4) {
		seedReadyListerIssue(t, ctx, fixture, readyListerIssue(id, i, label))
	}
	unlimited := 0
	_, err := fixture.ReadyLister.ListReady(ctx, publicops.ReadyListRequest{
		ReadyRequest:  publicops.ReadyRequest{Labels: []string{label}, Sort: readyListerSort, Limit: &unlimited},
		MaxRows:       2,
		MaxRowsSource: "--max-rows",
	})
	var tooMany *storageops.ErrTooManyRows
	if !errors.As(err, &tooMany) {
		t.Fatalf("ListReady(limit=0, MaxRows=2) over 4 ready rows: error = %v (%T), want *ErrTooManyRows", err, err)
	}

	two := 2
	listing, err := fixture.ReadyLister.ListReady(ctx, publicops.ReadyListRequest{
		ReadyRequest: publicops.ReadyRequest{Labels: []string{label}, Sort: readyListerSort, Limit: &two},
		MaxRows:      2,
	})
	if err != nil {
		t.Fatalf("ListReady(limit=2, MaxRows=2): %v — a page the cap does not bound must not be refused", err)
	}
	if len(listing.Items) != 2 || !listing.HasMore || listing.Total != 4 {
		t.Errorf("ListReady(limit=2, MaxRows=2) = %d items, HasMore=%v, Total=%d; want 2 items, HasMore, Total=4",
			len(listing.Items), listing.HasMore, listing.Total)
	}
}

// RunReadyListerRejectsWhatReadyRejects pins the validation half: a request
// Reader.Ready refuses is ErrValidation here too, before any row is read.
func RunReadyListerRejectsWhatReadyRejects(t *testing.T, ctx context.Context, fixture ReadyListerFixture) {
	t.Helper()
	for name, req := range map[string]publicops.ReadyRequest{
		"unknown sort": {Sort: "bogus"},
	} {
		if _, err := fixture.ReadyLister.ListReady(ctx, publicops.ReadyListRequest{ReadyRequest: req}); !errors.Is(err, storage.ErrValidation) {
			t.Errorf("ListReady(%s): error = %v, want ErrValidation", name, err)
		}
	}
}

// RunReadyListerEmptyFrontIsEmptyAndZero pins the drained-queue answer: no
// rows, a non-nil empty page, HasMore false, Total 0 and a NIL error. The decoy
// makes the zero the filter's doing rather than an empty database's.
func RunReadyListerEmptyFrontIsEmptyAndZero(t *testing.T, ctx context.Context, fixture ReadyListerFixture) {
	t.Helper()
	label := fixture.IssuePrefix + "-rlempty"
	seedReadyListerIssue(t, ctx, fixture, readyListerIssue(fixture.IssuePrefix+"-rlempty-decoy", 0, label+"-other"))
	one := 1
	listing, err := fixture.ReadyLister.ListReady(ctx, publicops.ReadyListRequest{
		ReadyRequest: publicops.ReadyRequest{Labels: []string{label}, Sort: readyListerSort, Limit: &one},
	})
	if err != nil {
		t.Fatalf("ListReady over an empty ready front: error = %v, want nil", err)
	}
	if listing.Items == nil || len(listing.Items) != 0 || listing.HasMore || listing.Total != 0 {
		t.Errorf("ListReady over an empty ready front = %+v, want non-nil empty Items, HasMore=false, Total=0", listing)
	}
	decoy, err := fixture.ReadyLister.ListReady(ctx, publicops.ReadyListRequest{
		ReadyRequest: publicops.ReadyRequest{Labels: []string{label + "-other"}, Sort: readyListerSort, Limit: &one},
	})
	if err != nil || decoy.Total != 1 {
		t.Errorf("ListReady for the decoy's own label: Total=%d err=%v, want 1 and nil", decoy.Total, err)
	}
}

// RunReadyListerWritesNothing pins "LISTING IS A READ": neither a listing nor a
// refused one records a history entry. The advisory defer-wake sweep has
// nothing to wake here, so any history movement is the listing's own.
func RunReadyListerWritesNothing(t *testing.T, ctx context.Context, fixture ReadyListerFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("fixture cannot observe history: CountHistory is nil, so the listing-is-a-read clause is unpinned on this backend")
	}
	label := fixture.IssuePrefix + "-rlread"
	seedReadyListerIssue(t, ctx, fixture, readyListerIssue(fixture.IssuePrefix+"-rlread-a", 1, label))
	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("count history entries: %v", err)
	}
	listing, err := fixture.ReadyLister.ListReady(ctx, publicops.ReadyListRequest{
		ReadyRequest: publicops.ReadyRequest{Labels: []string{label}, Sort: readyListerSort},
	})
	if err != nil || listing.Total != 1 {
		t.Fatalf("ListReady: Total=%d err=%v, want 1 and nil", listing.Total, err)
	}
	if _, err := fixture.ReadyLister.ListReady(ctx, publicops.ReadyListRequest{
		ReadyRequest: publicops.ReadyRequest{Labels: []string{label}, Sort: "bogus"},
	}); !errors.Is(err, storage.ErrValidation) {
		t.Fatalf("ListReady with a bad sort: error = %v, want ErrValidation", err)
	}
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("count history entries: %v", err)
	}
	if after != before {
		t.Errorf("history entries went %d -> %d across a listing and a refusal, want no change: listing is a read", before, after)
	}
}

// RunReadyListerDoesNotMutateTheCallerRequest is the request-snapshot tripwire:
// every reference member of the embedded ReadyRequest is populated with a value
// normalization would want to touch, and the request must come back equal to a
// second copy built by the same function.
func RunReadyListerDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture ReadyListerFixture) {
	t.Helper()
	build := func() publicops.ReadyListRequest {
		priority, limit := 2, 3
		return publicops.ReadyListRequest{
			ReadyRequest: publicops.ReadyRequest{
				IssueType:      " Task ",
				Labels:         []string{fixture.IssuePrefix + "-rlsnap ", fixture.IssuePrefix + "-rlsnap "},
				LabelsAny:      []string{" " + fixture.IssuePrefix + "-rlsnap-any"},
				ExcludeLabels:  []string{fixture.IssuePrefix + "-rlsnap-not "},
				ExcludeTypes:   []string{"mr,epic", " chore "},
				MetadataFields: map[string]string{"team": "conformance"},
				HasMetadataKey: "team",
				Priority:       &priority,
				Sort:           readyListerSort,
				Limit:          &limit,
				ExcludeIDs:     []string{fixture.IssuePrefix + "-rlsnap-x"},
			},
			MaxRows:       50,
			MaxRowsSource: "--max-rows",
		}
	}
	request := build()
	if _, err := fixture.ReadyLister.ListReady(ctx, request); err != nil {
		t.Fatalf("ListReady with a fully populated request: %v", err)
	}
	if want := build(); !reflect.DeepEqual(request, want) {
		t.Errorf("ListReady mutated the caller's request:\n got %+v\nwant %+v", request, want)
	}
}

func readyListerIssue(id string, priority int, labels ...string) *types.Issue {
	return &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: priority, IssueType: types.TypeTask, Labels: labels}
}

func readyListerIDs(fixture ReadyListerFixture, name string, n int) []string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("%s-%s-%d", fixture.IssuePrefix, name, i))
	}
	return ids
}

func seedReadyListerIssue(t *testing.T, ctx context.Context, fixture ReadyListerFixture, issue *types.Issue) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
}

func readyListerItemIDs(items []*types.IssueWithCounts) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil && item.Issue != nil {
			ids = append(ids, item.ID)
		}
	}
	return ids
}
