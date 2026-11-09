package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
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
		// The recovery pointer follows the OUTBOUND direction alone, because
		// `bd dep list <id> <id>` is outbound-only (dep.go:1101/1144). An
		// issue short only on dependent edges gets the inbound note instead,
		// and these two fields are what stop a later edit from quietly
		// pointing a reader at a command that cannot answer.
		wantPointer    bool
		wantInboundNak bool
	}{
		{
			name:        "one unrenderable outgoing edge",
			counter:     fakeDepCounter{depCount: 1},
			deps:        depListing{rows: 0},
			wantWarn:    true,
			wantText:    "1 dependency edge(s)",
			wantPointer: true,
		},
		{
			name:           "one unrenderable incoming edge",
			counter:        fakeDepCounter{rdepCount: 3},
			dependents:     depListing{rows: 1},
			wantWarn:       true,
			wantText:       "2 dependent edge(s)",
			wantPointer:    false,
			wantInboundNak: true,
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
			// #5272: the ids the render printed are shown, so the notice
			// leaves them out; the rest are still reported.
			name:     "printed unresolved ids cover the whole gap",
			counter:  fakeDepCounter{depCount: 3},
			deps:     depListing{rows: 1, printedUnresolved: 2},
			wantWarn: false,
		},
		{
			name:        "printed unresolved ids cover part of the gap",
			counter:     fakeDepCounter{depCount: 4},
			deps:        depListing{rows: 1, printedUnresolved: 1},
			wantWarn:    true,
			wantText:    "2 dependency edge(s)",
			wantPointer: true,
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
			// half must still be reported. It is the INBOUND half here, so the
			// outbound-only pointer must stay away even though the outbound
			// direction is the one that failed.
			name:           "outgoing failed, incoming still reportable",
			counter:        fakeDepCounter{depCount: 4, rdepCount: 2},
			deps:           depListing{rows: 0, err: boom},
			dependents:     depListing{rows: 0},
			wantWarn:       true,
			wantText:       "2 dependent edge(s)",
			wantPointer:    false,
			wantInboundNak: true,
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
				wantPointers := 0
				if tc.wantPointer {
					wantPointers = 1
					if !strings.Contains(out, "bd dep list rp-1 rp-1") {
						t.Errorf("warning did not name the recovery command, got:\n%q", out)
					}
				}
				// The pointer is printed once however many directions warn,
				// and not at all when only the inbound direction did.
				if n := strings.Count(out, "For raw edge records"); n != wantPointers {
					t.Errorf("recovery pointer printed %d times, want %d:\n%q", n, wantPointers, out)
				}
				wantNaks := 0
				if tc.wantInboundNak {
					wantNaks = 1
				}
				if n := strings.Count(out, "bd dep list is outbound-only"); n != wantNaks {
					t.Errorf("inbound no-listing note printed %d times, want %d:\n%q", n, wantNaks, out)
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
		// Both facts are true at once and both are said: the pointer covers
		// the outbound edges it can list, the note covers the inbound ones no
		// command can.
		if n := strings.Count(out, "bd dep list is outbound-only"); n != 1 {
			t.Errorf("inbound no-listing note printed %d times, want 1:\n%q", n, out)
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
	// stale-low count lives in show.go, show_display.go and
	// show_proxied_server.go, none of which this test exercises. The split
	// into readDepCounts plus warnUnresolvableDepEdges makes that order
	// READABLE at those call sites and does not pin it — reversing it at all
	// three leaves this package green, and
	// TestBuildIssueDetails_ConcurrentAddIsNotReportedAsUnresolvable, one tier
	// down on BuildIssueDetails, is the only case that actually fails.
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

// TestShowUnresolvableDepLines pins how #5272's unresolved outbound edges
// join the text sections: after the local lines of their relationship's
// section, a section created in the usual order when only unresolved edges
// have its type, related edges diverted to RELATED and deduplicated against
// local ones, a muted classification label, an escaped id, and the distinct
// id count the outbound notice subtracts.
func TestShowUnresolvableDepLines(t *testing.T) {
	local := &types.IssueWithDependencyMetadata{
		Issue:          types.Issue{ID: "rp-2", Title: "Local", Status: types.StatusOpen, Priority: 2},
		DependencyType: types.DepBlocks,
	}
	localRelated := &types.IssueWithDependencyMetadata{
		Issue:          types.Issue{ID: "rp-3", Title: "Local related", Status: types.StatusOpen, Priority: 2},
		DependencyType: types.DepRelated,
	}
	const escID = "liveop-\x1b[31mred"
	edge := func(target string, typ types.DependencyType) *types.Dependency {
		return &types.Dependency{IssueID: "rp-1", DependsOnID: target, Type: typ}
	}
	unresolved := []*types.Dependency{
		edge("external:x:cap", types.DepTracks),
		edge("liveop-a", types.DepBlocks),
		edge("liveop-a", "custom-z"), // duplicate far end: only fakes can store one
		edge(escID, types.DepBlocks),
		edge("liveop-r", types.DepRelated),
		edge("rp-3", types.DepRelatesTo),
		edge("rp-gone", types.DepWaitsFor),
	}

	relatedSeen := map[string]*types.IssueWithDependencyMetadata{}
	relatedUnresolved := map[string]*types.Dependency{}
	sections := groupOutgoingDepSections([]*types.IssueWithDependencyMetadata{local, localRelated},
		unresolved, relatedSeen, relatedUnresolved)

	type shape struct {
		heading string
		local   []string
		ids     []string
	}
	var got []shape
	for _, sec := range sections {
		sh := shape{heading: sec.Heading}
		for _, d := range sec.Deps {
			sh.local = append(sh.local, d.ID)
		}
		for _, e := range sec.Unresolved {
			sh.ids = append(sh.ids, e.DependsOnID)
		}
		got = append(got, sh)
	}
	want := []shape{
		{heading: "DEPENDS ON", local: []string{"rp-2"}, ids: []string{"liveop-a", escID}},
		{heading: "WAITS FOR", ids: []string{"rp-gone"}},
		{heading: "TRACKS", ids: []string{"external:x:cap"}},
		{heading: "CUSTOM-Z", ids: []string{"liveop-a"}},
	}
	if !slices.EqualFunc(got, want, func(a, b shape) bool {
		return a.heading == b.heading && slices.Equal(a.local, b.local) && slices.Equal(a.ids, b.ids)
	}) {
		t.Errorf("sections = %+v\nwant %+v", got, want)
	}
	if _, ok := relatedSeen["rp-3"]; !ok || len(relatedUnresolved) != 2 {
		t.Errorf("related split: seen %v, unresolved %v; want rp-3 local and two unresolved", relatedSeen, relatedUnresolved)
	}

	for _, tc := range []struct {
		edge *types.Dependency
		want string
	}{
		{edge("external:x:cap", types.DepTracks), "  →   external:x:cap " + ui.RenderMuted("(external)")},
		{edge("liveop-a", types.DepBlocks), "  →   liveop-a " + ui.RenderMuted("(cross-repo)")},
		{edge("rp-gone", types.DepWaitsFor), "  →   rp-gone " + ui.RenderMuted("(missing)")},
	} {
		if got := formatUnresolvedDependencyLine("→", tc.edge); got != tc.want {
			t.Errorf("formatUnresolvedDependencyLine(%s) = %q, want %q", tc.edge.DependsOnID, got, tc.want)
		}
	}
	if line := formatUnresolvedDependencyLine("→", edge(escID, types.DepBlocks)); strings.Contains(line, "\x1b") ||
		!strings.Contains(line, printedIssueID(escID)) {
		t.Errorf("unresolved id not escaped: %q", line)
	}

	related := captureStdout(t, func() error {
		printRelatedSection(relatedSeen, relatedUnresolved)
		return nil
	})
	if strings.Count(related, "rp-3") != 1 || !strings.Contains(related, "  ↔   liveop-r "+ui.RenderMuted("(cross-repo)")+"\n") {
		t.Errorf("RELATED section must list rp-3 once (local) then liveop-r unresolved:\n%s", related)
	}

	// liveop-a twice, the escaped id, rp-gone, external:x:cap, liveop-r; rp-3
	// is printed as a local line, so it is not an unresolved id.
	if n := printedUnresolvedIDs(sections, relatedSeen, relatedUnresolved); n != 5 {
		t.Errorf("printedUnresolvedIDs = %d, want 5 distinct ids", n)
	}
}
