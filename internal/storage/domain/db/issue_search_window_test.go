package db

import (
	"fmt"
	"testing"
)

// TestSearchWindowForCountsTheOffsetIntoTheCap pins the one decision
// searchWindowFor makes that a caller cannot see any other way: WHO CARRIES THE
// SKIP, and therefore which rows EnforceMaxRowsCap counts.
//
// The callers that reach this seam with both an offset and a cap consume the
// FILTER as a value — proxied `bd list --watch`, with and without --ready, and
// the hierarchical --parent walk — so they run no page epilogue and there is
// nothing above them to re-decide it. issueops.Reader's own callers never get
// here with an offset at all; they widen the filter first. That makes this
// table the only place the watch route's window is stated, and the first row is
// the one it exists for: an engine-side OFFSET hands EnforceMaxRowsCap the
// survivors of the skip, which is a page for a request the paged route refuses.
func TestSearchWindowForCountsTheOffsetIntoTheCap(t *testing.T) {
	for _, tc := range []struct {
		what     string
		limit    int
		offset   int
		maxRows  int
		wantSQL  string
		wantSkip int
		wantCap  int
	}{
		{
			// The divergence this table exists for. `--limit 10 --offset 2
			// --max-rows 3` under --watch used to render OFFSET 2, so a
			// five-row result set delivered three survivors against a cap of
			// three and never fired — while the paged route, which reaches this
			// seam at limit 12, refused the identical request.
			what:     "a cap keeps the skip here and covers it with the bound",
			limit:    10,
			offset:   2,
			maxRows:  3,
			wantSQL:  "LIMIT 4",
			wantSkip: 2,
			wantCap:  3,
		},
		{
			// What issueops.Reader hands this seam for that same request,
			// having already widened its filter. Same bound, same cap: the two
			// windows are one window.
			what:     "the paged route reaches the same window with the skip already folded in",
			limit:    12,
			offset:   0,
			maxRows:  3,
			wantSQL:  "LIMIT 4",
			wantSkip: 0,
			wantCap:  3,
		},
		{
			// Limit+Offset == MaxRows. The probe row must not fire a cap the
			// touched window fits inside, so the bound and the cap move
			// together — and the bump keys off the WIDENED window, not the
			// page, or this seam would refuse one row of offset early.
			what:     "at Limit+Offset == MaxRows the probe row moves the cap with it",
			limit:    2,
			offset:   1,
			maxRows:  3,
			wantSQL:  "LIMIT 4",
			wantSkip: 1,
			wantCap:  4,
		},
		{
			what:     "one more row of offset, and the same request is over the cap",
			limit:    2,
			offset:   2,
			maxRows:  3,
			wantSQL:  "LIMIT 4",
			wantSkip: 2,
			wantCap:  3,
		},
		{
			what:     "under the cap the bound is the touched window plus the probe row",
			limit:    1,
			offset:   1,
			maxRows:  3,
			wantSQL:  "LIMIT 3",
			wantSkip: 1,
			wantCap:  3,
		},
		{
			// An unlimited page needs no widening: it already touches every
			// matching row, and cap+1 is what detects the overage.
			what:     "an unlimited page behind an offset still counts the skipped rows",
			limit:    0,
			offset:   2,
			maxRows:  3,
			wantSQL:  "LIMIT 4",
			wantSkip: 2,
			wantCap:  3,
		},
		{
			// NO CAP, so nothing counts rows and the engine does the skipping —
			// the shape the unit-of-work Querier runs, whose request carries no
			// cap at all.
			what:     "without a cap the engine keeps the OFFSET",
			limit:    2,
			offset:   1,
			maxRows:  0,
			wantSQL:  "LIMIT 3 OFFSET 1",
			wantSkip: 0,
			wantCap:  0,
		},
		{
			// No LIMIT to hang an OFFSET on. MySQL has none, and the sentinel
			// this used to render panicked the embedded engine.
			what:     "an unbounded scan has no clause to carry an offset",
			limit:    0,
			offset:   1,
			maxRows:  0,
			wantSQL:  "",
			wantSkip: 1,
			wantCap:  0,
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			w := searchWindowFor(tc.limit, tc.offset, tc.maxRows, "--max-rows")
			where := fmt.Sprintf("Limit=%d Offset=%d MaxRows=%d", tc.limit, tc.offset, tc.maxRows)
			if w.sql != tc.wantSQL {
				t.Errorf("%s rendered %q, want %q", where, w.sql, tc.wantSQL)
			}
			if w.skip != tc.wantSkip {
				t.Errorf("%s left skip=%d for this package, want %d", where, w.skip, tc.wantSkip)
			}
			if w.rowCap != tc.wantCap {
				t.Errorf("%s enforces rowCap=%d, want %d", where, w.rowCap, tc.wantCap)
			}
			if w.limit != tc.limit {
				t.Errorf("%s trims to %d, want the page the caller asked for", where, w.limit)
			}
		})
	}
}
