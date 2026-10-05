//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_readylister_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"fmt"
	"slices"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The ReadyLister contract, run through the composition: the page from
// listReadyWork and, when the page does not settle it, the total from
// countReadyWork — two operations, two encodings, one question.
//
// HonorsExcludeIDs is NOT run here, and on purpose: the client never sends an
// id exclusion (E-ReadyRequest.ExcludeIDs refuses it). The only producer of
// that set is the external-dependency policy, and over http the server applies
// it itself (policy.external_dependencies), so the exclusion is computed and
// honored on the far side of the wire, where the served backend's own
// ReadyLister contract pins it.
//
// Two contract cases park on the ready wire's refusals rather than on anything
// this role decides. The identity grid crosses every limit with non-zero
// offsets, and listReadyWork publishes no offset (E-ReadyRequest.Offset) — the
// grid's offset-zero half runs bespoke below. The no-mutation case sends a
// fully populated request, ExcludeIDs and Offset included, which refuses before
// any role code runs.

func servedReadyListerFixture(t *testing.T, name string) conformance.ReadyListerFixture {
	t.Helper()
	c := composition(t)
	f := c.fixture(t)
	return conformance.ReadyListerFixture{
		IssuePrefix:   servedIssuePrefix + "-" + name,
		ReadyLister:   bindRole(t, f, func(s *Store) (issueops.ReadyLister, error) { return s.ReadyLister() }),
		Reader:        bindRole(t, f, func(s *Store) (issueops.Reader, error) { return s.IssueReader() }),
		ReadyCounter:  bindRole(t, f, func(s *Store) (issueops.ReadyCounter, error) { return s.ReadyCounter() }),
		CreateIssue:   c.seedIssue,
		CreateWisp:    c.seedIssue,
		AddDependency: c.seedDependency,
		CountHistory:  c.countHistory,
	}
}

func TestServedReadyListerAgreesWithReadyAndCountReady(t *testing.T) {
	skipKnownDivergence(t, "E-ReadyRequest.Offset", readParkBead,
		"the identity grid crosses every limit with non-zero offsets, which listReadyWork publishes no parameter for; "+
			"its offset-zero half is TestServedReadyListerAgreesAtOffsetZero")
	conformance.RunReadyListerAgreesWithReadyAndCountReady(t, t.Context(), servedReadyListerFixture(t, "rl"))
}

// TestServedReadyListerAgreesAtOffsetZero is the contract grid's offset-zero
// half, run bespoke because the grid's other offsets refuse on this wire: for
// every page size — unlimited, smaller than the set, exactly the set, larger —
// the listing's items and has_more are Reader.Ready's and its total is
// ReadyCounter's, over a set with a blocked row and a label outsider as traps.
func TestServedReadyListerAgreesAtOffsetZero(t *testing.T) {
	f := servedReadyListerFixture(t, "rlz")
	ctx := t.Context()
	label := f.IssuePrefix + "-z"
	var ids []string
	seed := func(id string, priority int, labels ...string) {
		t.Helper()
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: priority,
			IssueType: types.TypeTask, Labels: labels}
		if err := f.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	for i := range 5 {
		id := fmt.Sprintf("%s-z%d", f.IssuePrefix, i)
		seed(id, i%4, label)
		ids = append(ids, id)
	}
	blocked := f.IssuePrefix + "-zblocked"
	seed(blocked, 0, label)
	if err := f.AddDependency(ctx, &types.Dependency{IssueID: blocked, DependsOnID: ids[0], Type: types.DepBlocks}, "seed"); err != nil {
		t.Fatalf("seed the blocker edge: %v", err)
	}
	seed(f.IssuePrefix+"-zoutsider", 0, label+"-other")

	base := issueops.ReadyRequest{Labels: []string{label}, Sort: "priority"}
	count, err := f.ReadyCounter.CountReady(ctx, base)
	if err != nil {
		t.Fatalf("CountReady: %v", err)
	}
	if count.Total != int64(len(ids)) {
		t.Fatalf("CountReady = %d for the %d ready rows seeded", count.Total, len(ids))
	}
	itemIDs := func(items []*types.IssueWithCounts) []string {
		out := make([]string, 0, len(items))
		for _, row := range items {
			out = append(out, row.ID)
		}
		return out
	}
	for _, limit := range []int{0, 1, 2, len(ids), len(ids) + 2} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			req := base
			req.Limit = &limit
			page, err := f.Reader.Ready(ctx, req)
			if err != nil {
				t.Fatalf("Reader.Ready: %v", err)
			}
			listing, err := f.ReadyLister.ListReady(ctx, issueops.ReadyListRequest{ReadyRequest: req})
			if err != nil {
				t.Fatalf("ListReady: %v", err)
			}
			if got, want := itemIDs(listing.Items), itemIDs(page.Items); !slices.Equal(got, want) {
				t.Errorf("ListReady items = %v, Reader.Ready items = %v", got, want)
			}
			if listing.HasMore != page.HasMore {
				t.Errorf("ListReady HasMore = %v, Reader.Ready HasMore = %v", listing.HasMore, page.HasMore)
			}
			if listing.Total != count.Total {
				t.Errorf("ListReady Total = %d, CountReady = %d", listing.Total, count.Total)
			}
		})
	}
}

func TestServedReadyListerTotalCountsTheWispPlaneItAdmits(t *testing.T) {
	conformance.RunReadyListerTotalCountsTheWispPlaneItAdmits(t, t.Context(), servedReadyListerFixture(t, "rl"))
}

func TestServedReadyListerRefusesPastTheRowCap(t *testing.T) {
	conformance.RunReadyListerRefusesPastTheRowCap(t, t.Context(), servedReadyListerFixture(t, "rl"))
}

func TestServedReadyListerRejectsWhatReadyRejects(t *testing.T) {
	conformance.RunReadyListerRejectsWhatReadyRejects(t, t.Context(), servedReadyListerFixture(t, "rl"))
}

func TestServedReadyListerEmptyFrontIsEmptyAndZero(t *testing.T) {
	conformance.RunReadyListerEmptyFrontIsEmptyAndZero(t, t.Context(), servedReadyListerFixture(t, "rl"))
}

func TestServedReadyListerWritesNothing(t *testing.T) {
	conformance.RunReadyListerWritesNothing(t, t.Context(), servedReadyListerFixture(t, "rl"))
}

func TestServedReadyListerDoesNotMutateTheCallerRequest(t *testing.T) {
	skipKnownDivergence(t, "E-ReadyRequest.ExcludeIDs", readParkBead,
		"the case sends a fully populated request, and ExcludeIDs and Offset refuse before any role code runs; "+
			"the listing builds its count request from a copy (role_readylister.go), which TestHTTPReadyListerTotal pins")
	conformance.RunReadyListerDoesNotMutateTheCallerRequest(t, t.Context(), servedReadyListerFixture(t, "rl"))
}
