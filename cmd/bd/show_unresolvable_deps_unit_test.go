package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// fakeDepCounter answers the dependents aggregate readDependentCount makes,
// independently of any listing.
type fakeDepCounter struct {
	rdepCount int64
	rdepErr   error
}

func (f fakeDepCounter) CountDependents(_ context.Context, _ string) (int64, error) {
	return f.rdepCount, f.rdepErr
}

// TestWarnUnresolvableDependentEdges covers the INBOUND text disclosure — the
// one direction that still warns. Since GH#5850 the outbound direction
// renders its unrenderable far ends in their relation sections from the
// combined read's records (see TestPrintDepSectionRendersMissingFarEnds), so
// it has no count-minus-rows warning left to test; the inbound direction has
// no combined read yet and keeps the count-minus-rows notice, with the case
// a codex review caught on the first cut of this change: `bd show` renders
// dependencies best effort and can discard the listing error, so a FAILED
// listing beside a SUCCEEDING count would report every edge the issue has as
// cross-repo/external. That turns a transient backend error into a claim
// about the data, which is worse than staying quiet — the same distinction
// BuildIssueDetails draws with depsErr on the JSON side.
func TestWarnUnresolvableDependentEdges(t *testing.T) {
	boom := errors.New("backend down")

	tests := []struct {
		name           string
		counter        fakeDepCounter
		dependents     depListing
		wantWarn       bool
		wantText       string
		wantInboundNak bool
	}{
		{
			name:           "one unrenderable incoming edge",
			counter:        fakeDepCounter{rdepCount: 3},
			dependents:     depListing{rows: 1},
			wantWarn:       true,
			wantText:       "2 dependent edge(s)",
			wantInboundNak: true,
		},
		{
			// The negative control. Without it every assertion above is
			// satisfied by a build that warns unconditionally.
			name:       "fully local, count matches the rows",
			counter:    fakeDepCounter{rdepCount: 1},
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
			name:       "listing read FAILED, count succeeded",
			counter:    fakeDepCounter{rdepCount: 5},
			dependents: depListing{rows: 0, err: boom},
			wantWarn:   false,
		},
		{
			// Symmetric: the count is what failed. It yields 0, which cannot
			// exceed any row total, but assert it rather than lean on the sign.
			name:       "count read FAILED",
			counter:    fakeDepCounter{rdepErr: boom},
			dependents: depListing{rows: 0},
			wantWarn:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			count := readDependentCount(context.Background(), tc.counter, "rp-1")
			out := captureStderr(t, func() {
				warnUnresolvableDependentEdges("rp-1", count, tc.dependents)
			})
			if tc.wantWarn {
				if !strings.Contains(out, "no row in this database") {
					t.Errorf("expected a warning, got:\n%q", out)
				}
				if tc.wantText != "" && !strings.Contains(out, tc.wantText) {
					t.Errorf("warning did not mention %q, got:\n%q", tc.wantText, out)
				}
				// The note, not a recovery pointer: `bd dep list <id> <id>`
				// is outbound-only and cannot show dependent edges, so
				// pointing the reader at it would be a lie.
				wantNaks := 0
				if tc.wantInboundNak {
					wantNaks = 1
				}
				if n := strings.Count(out, "bd dep list is outbound-only"); n != wantNaks {
					t.Errorf("inbound no-listing note printed %d times, want %d:\n%q", n, wantNaks, out)
				}
				if strings.Contains(out, "For raw edge records") {
					t.Errorf("outbound recovery pointer printed for a dependent-only gap:\n%q", out)
				}
			} else if out != "" {
				t.Errorf("expected silence, got:\n%q", out)
			}
		})
	}

	// A nil store yields a count carrying errNoDepCounter, which the report
	// gate treats exactly like any other failed count read.
	t.Run("nil store is a no-op", func(t *testing.T) {
		count := readDependentCount(context.Background(), nil, "rp-1")
		out := captureStderr(t, func() {
			warnUnresolvableDependentEdges("rp-1", count, depListing{rows: 0})
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
	// stale-low count lives in show.go, show_display.go and
	// show_proxied_server.go, none of which this test exercises. The split
	// into readDependentCount plus warnUnresolvableDependentEdges makes that
	// order READABLE at those call sites and does not pin it — reversing it
	// at all three leaves this package green, and
	// TestBuildIssueDetails_ConcurrentAddIsNotReportedAsUnresolvable, one
	// tier down on BuildIssueDetails, is the only case that actually fails.
	t.Run("a stale-low count is suppressed, not reported", func(t *testing.T) {
		count := readDependentCount(context.Background(), fakeDepCounter{rdepCount: 1}, "rp-1")
		out := captureStderr(t, func() {
			// The listing observed the edge the count predates, plus one more
			// added in the window.
			warnUnresolvableDependentEdges("rp-1", count, depListing{rows: 2})
		})
		if out != "" {
			t.Errorf("a concurrent add must not be reported as unresolvable, got:\n%q", out)
		}
	})
}

// TestPrintDepSectionRendersMissingFarEnds pins the GH#5850 outbound text
// render: each edge whose far end has no row in this database prints in its
// relation section, with the section's direction glyph, the id exactly as
// stored, and "(not in this database)" — including when it is the section's
// only entry, which is the lone-section case that used to render as no
// dependency at all. The old outbound stderr warning is gone with it; these
// assertions would pass trivially if the render regressed to silence but the
// warning came back, so the second half asserts the warning stays away.
func TestPrintDepSectionRendersMissingFarEnds(t *testing.T) {
	sec := depSection{
		Type:    types.DepBlocks,
		Heading: "DEPENDS ON",
		Glyph:   "→",
		Deps: []*types.IssueWithDependencyMetadata{
			{Issue: types.Issue{ID: "rp-2", Title: "Local blocker"}, DependencyType: types.DepBlocks},
		},
		Missing: []string{"external:liveop:liveop-kmf", "liveop-x"},
	}
	out := captureStdout(t, func() error {
		printDepSection(sec)
		return nil
	})
	if !strings.Contains(out, "DEPENDS ON") {
		t.Errorf("section heading missing:\n%s", out)
	}
	if !strings.Contains(out, "rp-2") {
		t.Errorf("hydrated row missing:\n%s", out)
	}
	// The lone marker renders per far end, with the stored id, and the
	// section's own glyph — no invented status or title beside them.
	for _, id := range []string{"external:liveop:liveop-kmf", "liveop-x"} {
		if !strings.Contains(out, id+" (not in this database)") {
			t.Errorf("far end %q not rendered with the marker:\n%s", id, out)
		}
	}

	// The lone-section case: no hydrated rows at all, the section still
	// prints. This is the convoy symptom — one edge to
	// external:liveop:liveop-kmf rendering as no dependency section.
	lone := captureStdout(t, func() error {
		printDepSection(depSection{
			Type:    types.DepTracks,
			Heading: "TRACKS",
			Glyph:   "→",
			Missing: []string{"external:liveop:liveop-kmf"},
		})
		return nil
	})
	if !strings.Contains(lone, "TRACKS") || !strings.Contains(lone, "external:liveop:liveop-kmf (not in this database)") {
		t.Errorf("lone missing entry did not render in its section:\n%s", lone)
	}

	// And a section with nothing to print prints nothing — the guard keeps a
	// bare heading from appearing if a caller ever builds one by hand.
	nothing := captureStdout(t, func() error {
		printDepSection(depSection{Type: types.DepBlocks, Heading: "DEPENDS ON", Glyph: "→"})
		return nil
	})
	if nothing != "" {
		t.Errorf("empty section printed %q, want nothing", nothing)
	}
}
