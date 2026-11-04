// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/order.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"cmp"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// The client-side display order (ledger row L2).
//
// A local list is ordered TWICE and the second pass is not redundant: the query
// renders sqlbuild's ORDER BY, and the shared page epilogue then stable-sorts
// the rows it returned by the single display key. The two disagree on purpose —
// the SQL clause carries the tie-break tail (created DESC, id ASC) and the
// epilogue carries the orders SQL cannot express — so an order applied here has
// to be BOTH of them, in that sequence, or the tie order comes out different
// from a local run of the same request.
//
// sqlbuild.Less is imported rather than reimplemented. It is already the
// Go-side mirror of that ORDER BY — the storage layer merges the issue and wisp
// planes with it — so borrowing it is what makes "the client comparator and the
// SQL order cannot drift" true by construction instead of by a golden file.
// Only the epilogue's own comparator is written out below, because it lives in
// internal/workapi, which depguard denies to this package.

// sortListRows applies the display order a ListRequest asked for to a page the
// wire returned in created order.
func sortListRows(rows []*types.IssueWithCounts, sortBy string, reverse bool) {
	slices.SortStableFunc(rows, func(a, b *types.IssueWithCounts) int {
		ai, bi := rowIssue(a), rowIssue(b)
		switch {
		case ai == nil && bi == nil:
			return 0
		case ai == nil:
			return 1
		case bi == nil:
			return -1
		}
		switch {
		case sqlbuild.Less(ai, bi, sortBy, reverse):
			return -1
		case sqlbuild.Less(bi, ai, sortBy, reverse):
			return 1
		default:
			return 0
		}
	})
	if sortBy == "" {
		// The epilogue leaves an unnamed order alone, so the SQL order above is
		// the whole of it — which is the flagless default no one names and
		// everyone sees.
		return
	}
	slices.SortStableFunc(rows, func(a, b *types.IssueWithCounts) int {
		ai, bi := rowIssue(a), rowIssue(b)
		switch {
		case ai == nil && bi == nil:
			return 0
		case ai == nil:
			return 1
		case bi == nil:
			return -1
		}
		r := compareIssuesBy(ai, bi, sortBy)
		if reverse {
			return -r
		}
		return r
	})
}

func rowIssue(row *types.IssueWithCounts) *types.Issue {
	if row == nil {
		return nil
	}
	return row.Issue
}

// compareIssuesBy orders two issues by one of `bd list --sort`'s fields. An
// unknown field compares equal, which leaves the underlying order intact.
//
// It is a copy of internal/workapi.CompareIssuesBy, kept honest by
// TestCompareIssuesByMatchesTheSharedComparator rather than by review: the
// depguard rule that keeps the work-query builders out of this package takes
// the comparator with them, and a display order that disagreed with the local
// one by a single tie would show up as a reordered page and nothing else.
func compareIssuesBy(a, b *types.Issue, sortBy string) int {
	switch sortBy {
	case "priority":
		return cmp.Compare(a.Priority, b.Priority)
	case "created":
		return b.CreatedAt.Compare(a.CreatedAt)
	case "updated":
		return b.UpdatedAt.Compare(a.UpdatedAt)
	case "closed":
		switch {
		case a.ClosedAt == nil && b.ClosedAt == nil:
			return 0
		case a.ClosedAt == nil:
			return 1
		case b.ClosedAt == nil:
			return -1
		}
		return b.ClosedAt.Compare(*a.ClosedAt)
	case "status":
		return cmp.Compare(a.Status, b.Status)
	case "id":
		return utils.NaturalCompareIDs(a.ID, b.ID)
	case "title":
		return cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	case "type":
		return cmp.Compare(a.IssueType, b.IssueType)
	case "assignee":
		return cmp.Compare(a.Assignee, b.Assignee)
	}
	return 0
}
