package workapi

import (
	"slices"

	"github.com/steveyegge/beads/internal/types"
)

// PageRow is the two row shapes a work page can carry: the bare issue the text
// renderings want, and the counted issue every --json and every wire body
// carries. They are listed here rather than behind an interface because the
// epilogue below has to reach types.Issue's own fields to order on them, and an
// interface would put a method on the canonical model to say so.
type PageRow interface {
	*types.Issue | *types.IssueWithCounts
}

// rowIssue projects either row shape onto the issue the display order compares.
func rowIssue[T PageRow](row T) *types.Issue {
	switch v := any(row).(type) {
	case *types.Issue:
		return v
	case *types.IssueWithCounts:
		if v == nil {
			return nil
		}
		return v.Issue
	}
	return nil
}

// SortRows applies a display order to a page of either row shape. An empty
// order leaves the rows in the order storage returned them.
//
// The sort is STABLE, which matters for exactly one caller: a keyset-paged read
// orders by (created_at DESC, id ASC), and CompareIssuesBy("created") reports a
// same-second pair as equal. An unstable sort would permute those rows out of
// the id order the cursor predicate resumes from, so a page boundary landing
// inside a same-second group could skip or repeat rows. Stability makes the
// tie-break fall back to the order storage returned, which is the order the
// cursor assumes.
func SortRows[T PageRow](rows []T, sortBy string, reverse bool) {
	if sortBy == "" {
		return
	}
	slices.SortStableFunc(rows, func(a, b T) int {
		ai, bi := rowIssue(a), rowIssue(b)
		if ai == nil {
			if bi == nil {
				return 0
			}
			return 1
		}
		if bi == nil {
			return -1
		}
		r := CompareIssuesBy(ai, bi, sortBy)
		if reverse {
			return -r
		}
		return r
	})
}

// SortIssues applies the display order to a page of issues.
func SortIssues(issues []*types.Issue, sortBy string, reverse bool) {
	SortRows(issues, sortBy, reverse)
}

// SortIssuesWithCounts applies the display order to a page of counted issues.
func SortIssuesWithCounts(items []*types.IssueWithCounts, sortBy string, reverse bool) {
	SortRows(items, sortBy, reverse)
}

// FinishPage is the execution epilogue every list and ready read shares: apply
// the display order, cut the rows back to the number the caller asked to
// RECEIVE, and report whether the cut hid anything. It returns the page and
// that verdict.
//
// ONE FUNCTION, FOUR CALLERS, and that is the point. The two issueops.Reader
// implementations and both of `bd list`'s routes reach this; before it existed
// each of them had written the tail of the same query out longhand, and they
// had already drifted twice — a --ready page under a sort the database cannot
// express came back untrimmed from one implementation, and the proxied CLI
// route answered `--sort id --limit 5` with every row while the direct route
// answered with five. Both were the same bug in the same three lines, written
// in two places.
//
// THE ORDER OF THE TWO STEPS IS LOAD-BEARING. The sort runs first because it
// decides which rows the cut keeps. For most queries the cut is a no-op: the
// row limit the query ran under and the page limit are the same number, and the
// seam either reported hasMore itself or was asked for one row past the page
// (WithFetchOneExtra) so that the extra row IS the answer. They come apart for
// a sort SQL cannot express — SQLLimit zeroes the query's limit for `--sort id`,
// which needs natural-numeric comparison (bd-9 < bd-10) — so that query returns
// the entire result set, this is where the requested order first exists, and
// this cut is the only thing bounding the page.
//
// hasMore seeds the verdict for a seam that reports it natively; a seam that
// does not passes false and lets the over-fetched row speak. Either way a cut
// that removes a row wins over a seam that said there was nothing more.
//
// A limit of 0 skips the cut entirely: every row passes through in the
// requested order, and the verdict returned is the seed the caller passed in,
// unchanged. It is NOT forced to false. An unlimited request cannot be
// truncated here, but a seam that already reported more rows behind it — a
// backend page bound, a cap applied before this point — is still saying
// something true, and this function is in no position to contradict it.
//
// Rows are never nil on the way out — an empty page is an empty array on every
// surface that serializes one, and a caller must not have to tell null from
// empty to learn that nothing matched.
func FinishPage[T PageRow](rows []T, sortBy string, reverse bool, limit int, hasMore bool) ([]T, bool) {
	return FinishPageAt(rows, sortBy, reverse, 0, limit, hasMore)
}

// FinishPageAt is FinishPage for a request that also carries an OFFSET: the
// display order, the skip, the cut, and the verdict.
//
// THE SKIP IS THE EPILOGUE'S ON EVERY BACKEND, and not because no seam can
// render OFFSET — one of them can. It is here for the same reason SQLLimit
// sends `--sort id` to the database unbounded: the rows have to be skipped in
// the ORDER THE CALLER ASKED FOR, and a sort SQL cannot express first exists
// on the line above. A seam that skipped rows itself under that sort would
// hand back a page cut out of the middle of storage order.
//
// It also decides what a MaxRows cap counts. A row skipped by the database is
// a row the query matched, and the store-backed seam — which renders LIMIT
// without OFFSET — has always counted it. Both implementations of
// issueops.Reader therefore ask their seam for the rows BEFORE the page too
// (see WithRowsBeforeThePage) and skip them here, so the same request trips
// the same circuit breaker on either one.
//
// An offset past the end of the result set is an empty page, not an error: a
// pager that walks off the end has its answer.
func FinishPageAt[T PageRow](rows []T, sortBy string, reverse bool, offset, limit int, hasMore bool) ([]T, bool) {
	SortRows(rows, sortBy, reverse)
	switch {
	case offset >= len(rows):
		rows = rows[:0]
	case offset > 0:
		rows = rows[offset:]
	}
	if limit > 0 && len(rows) > limit {
		rows, hasMore = rows[:limit], true
	}
	if rows == nil {
		rows = []T{}
	}
	return rows, hasMore
}
