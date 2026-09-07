package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeDepCounter answers the two aggregate queries readDepCounts makes,
// independently of any listing.
type fakeDepCounter struct {
	depCount  int64
	depErr    error
	rdepCount int64
	rdepErr   error
}

func (f fakeDepCounter) CountDependencies(_ context.Context, _ string) (int64, error) {
	return f.depCount, f.depErr
}
func (f fakeDepCounter) CountDependents(_ context.Context, _ string) (int64, error) {
	return f.rdepCount, f.rdepErr
}

// TestWarnUnresolvableDepEdges covers the text-mode disclosure, and in
// particular the case a codex review caught on the first cut of this change:
// `bd show` renders dependencies best effort and discarded the listing error,
// so a FAILED listing beside a SUCCEEDING count reported every edge the issue
// has as cross-repo/external. That turns a transient backend error into a
// claim about the data, which is worse than staying quiet — the same
// distinction BuildIssueDetails draws with depsErr on the JSON side, and the
// text path did not draw at all.
func TestWarnUnresolvableDepEdges(t *testing.T) {
	boom := errors.New("backend down")

	tests := []struct {
		name       string
		counter    fakeDepCounter
		deps       depListing
		dependents depListing
		wantWarn   bool
		wantText   string
	}{
		{
			name:     "one unrenderable outgoing edge",
			counter:  fakeDepCounter{depCount: 1},
			deps:     depListing{rows: 0},
			wantWarn: true,
			wantText: "1 dependency edge(s)",
		},
		{
			name:       "one unrenderable incoming edge",
			counter:    fakeDepCounter{rdepCount: 3},
			dependents: depListing{rows: 1},
			wantWarn:   true,
			wantText:   "2 dependent edge(s)",
		},
		{
			// The negative control. Without it every assertion above is
			// satisfied by a build that warns unconditionally.
			name:       "fully local, counts match the rows",
			counter:    fakeDepCounter{depCount: 2, rdepCount: 1},
			deps:       depListing{rows: 2},
			dependents: depListing{rows: 1},
			wantWarn:   false,
		},
		{
			name:     "nothing at all",
			counter:  fakeDepCounter{},
			wantWarn: false,
		},
		{
			// The codex finding. The count is honest and the listing never
			// ran; staying silent is the only correct answer.
			name:     "listing read FAILED, count succeeded",
			counter:  fakeDepCounter{depCount: 5},
			deps:     depListing{rows: 0, err: boom},
			wantWarn: false,
		},
		{
			name:       "dependents listing FAILED, count succeeded",
			counter:    fakeDepCounter{rdepCount: 5},
			dependents: depListing{rows: 0, err: boom},
			wantWarn:   false,
		},
		{
			// Symmetric: the count is what failed. It yields 0, which cannot
			// exceed any row total, but assert it rather than lean on the sign.
			name:     "count read FAILED",
			counter:  fakeDepCounter{depErr: boom},
			deps:     depListing{rows: 0},
			wantWarn: false,
		},
		{
			// One direction is diagnosable and the other is not: the healthy
			// half must still be reported.
			name:       "outgoing failed, incoming still reportable",
			counter:    fakeDepCounter{depCount: 4, rdepCount: 2},
			deps:       depListing{rows: 0, err: boom},
			dependents: depListing{rows: 0},
			wantWarn:   true,
			wantText:   "2 dependent edge(s)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			counts := readDepCounts(context.Background(), tc.counter, "rp-1")
			out := captureStderr(t, func() {
				warnUnresolvableDepEdges("rp-1", counts, tc.deps, tc.dependents)
			})
			if tc.wantWarn {
				if !strings.Contains(out, "no row in this database") {
					t.Errorf("expected a warning, got:\n%q", out)
				}
				if tc.wantText != "" && !strings.Contains(out, tc.wantText) {
					t.Errorf("warning did not mention %q, got:\n%q", tc.wantText, out)
				}
				if !strings.Contains(out, "bd dep list rp-1 rp-1") {
					t.Errorf("warning did not name the recovery command, got:\n%q", out)
				}
				// The pointer is printed once however many directions warn.
				if n := strings.Count(out, "For raw edge records"); n != 1 {
					t.Errorf("recovery pointer printed %d times, want 1:\n%q", n, out)
				}
			} else if out != "" {
				t.Errorf("expected silence, got:\n%q", out)
			}
		})
	}

	t.Run("both directions warn but the pointer is printed once", func(t *testing.T) {
		counts := readDepCounts(context.Background(), fakeDepCounter{depCount: 1, rdepCount: 1}, "rp-1")
		out := captureStderr(t, func() {
			warnUnresolvableDepEdges("rp-1", counts, depListing{rows: 0}, depListing{rows: 0})
		})
		if n := strings.Count(out, "no row in this database"); n != 2 {
			t.Errorf("expected two warnings, got %d:\n%q", n, out)
		}
		if n := strings.Count(out, "For raw edge records"); n != 1 {
			t.Errorf("recovery pointer printed %d times, want 1:\n%q", n, out)
		}
	})

	// A nil store yields counts carrying errNoDepCounter in both directions,
	// which the report gate treats exactly like any other failed count read.
	t.Run("nil store is a no-op", func(t *testing.T) {
		counts := readDepCounts(context.Background(), nil, "rp-1")
		out := captureStderr(t, func() {
			warnUnresolvableDepEdges("rp-1", counts, depListing{rows: 0}, depListing{rows: 0})
		})
		if out != "" {
			t.Errorf("expected silence, got:\n%q", out)
		}
	})

	// The arithmetic half of the concurrency mitigation: a count that is
	// stale-LOW relative to the listing — what a count read BEFORE a
	// concurrent add looks like — must be suppressed rather than reported.
	//
	// SCOPE, stated because it would otherwise read as more than it is: this
	// pins the SUBTRACTION only. The call-site ORDER that produces the
	// stale-low count lives in show.go and show_display.go, which this test
	// does not exercise; the split into readDepCounts plus
	// warnUnresolvableDepEdges is what makes that order visible at those call
	// sites, and TestBuildIssueDetails_ConcurrentAddIsNotReportedAsUnresolvable
	// is the case that actually fails when an order is reversed.
	t.Run("a stale-low count is suppressed, not reported", func(t *testing.T) {
		counts := readDepCounts(context.Background(), fakeDepCounter{depCount: 1}, "rp-1")
		out := captureStderr(t, func() {
			// The listing observed the edge the count predates, plus one more
			// added in the window.
			warnUnresolvableDepEdges("rp-1", counts, depListing{rows: 2}, depListing{})
		})
		if out != "" {
			t.Errorf("a concurrent add must not be reported as unresolvable, got:\n%q", out)
		}
	})
}
