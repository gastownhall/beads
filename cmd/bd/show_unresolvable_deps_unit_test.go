package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
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
			// The bd-hkpas7 subtraction: every gap id the render printed as a
			// raw line comes off the gap, so a fully covered gap says nothing.
			name:       "printed unresolved ids cover the whole gap",
			counter:    fakeDepCounter{depCount: 2},
			deps:       depListing{rows: 0, printedUnresolved: 2},
			dependents: depListing{rows: 0},
			wantWarn:   false,
		},
		{
			// Partial coverage: one of two gap ids printed, so the warning
			// names the one that is still missing — and the pointer stays,
			// because one unshown edge is still one `bd dep list` can show.
			name:        "printed unresolved ids cover part of the gap",
			counter:     fakeDepCounter{depCount: 2},
			deps:        depListing{rows: 0, printedUnresolved: 1},
			wantWarn:    true,
			wantText:    "1 dependency edge(s)",
			wantPointer: true,
		},
		{
			// Distinct ids, not raw rows: two edges to the SAME printed id
			// count once, leaving the other edge's gap to announce. Matches
			// how a reader counts (one id on screen is one mystery gone).
			name:        "two edges to one printed id subtract once",
			counter:     fakeDepCounter{depCount: 3},
			deps:        depListing{rows: 1, printedUnresolved: 1},
			wantWarn:    true,
			wantText:    "1 dependency edge(s)",
			wantPointer: true,
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

// fakeUnresolvedEdgeSource is the raw-read half of gateUnresolvedDepEdges:
// it hands back fixed rows (or an error) and counts every call, because the
// gate's no-gap-must-not-read rule is about the CALL, not just the result.
type fakeUnresolvedEdgeSource struct {
	raw    []*types.Dependency
	rawErr error
	isWisp bool
	calls  int
}

func (f *fakeUnresolvedEdgeSource) DependencyRecords(_ context.Context, _ string, isWisp bool) ([]*types.Dependency, error) {
	f.calls++
	f.isWisp = isWisp
	return f.raw, f.rawErr
}

// localDepRow builds one hydrated local line the way the listing paths do.
func localDepRow(id, title string, depType types.DependencyType) *types.IssueWithDependencyMetadata {
	return &types.IssueWithDependencyMetadata{
		Issue:          types.Issue{ID: id, Title: title, Status: types.StatusOpen, Priority: 2, IssueType: "task"},
		DependencyType: depType,
	}
}

// TestShowUnresolvableDepLines covers the RENDER half of bd-hkpas7 (#5272) at
// the unit level: how raw unresolved edge rows become printed lines — the
// gate that decides whether to read them at all, the merge that folds them
// into the grouped sections, the line format with its classification labels,
// and the distinct-id count the warning subtracts. The embedded and proxied
// tests prove real commands render this end to end; these pin the pieces no
// CLI test can reach (a failed read here, a negative gap there).
func TestShowUnresolvableDepLines(t *testing.T) {
	boom := errors.New("backend down")

	// The gate: read raw rows ONLY when the outbound gap is measurable and
	// positive, then keep exactly the rows the hydrated listing dropped.
	t.Run("gateUnresolvedDepEdges", func(t *testing.T) {
		gate := func(src *fakeUnresolvedEdgeSource, counts depCounts, deps depListing, hydrated []*types.IssueWithDependencyMetadata) []*types.Dependency {
			return gateUnresolvedDepEdges(context.Background(), src, "rp-1", true, counts, deps, hydrated)
		}
		positiveGap := depCounts{deps: depCount{n: 2}}
		hydrated := []*types.IssueWithDependencyMetadata{localDepRow("rp-2", "local blocker", types.DepBlocks)}

		t.Run("positive gap reads and filters the raw rows", func(t *testing.T) {
			src := &fakeUnresolvedEdgeSource{raw: []*types.Dependency{
				{IssueID: "rp-1", DependsOnID: "rp-2", Type: types.DepBlocks},       // hydrated: not unresolved
				{IssueID: "rp-1", DependsOnID: "liveop-9", Type: types.DepBlocks},   // dropped by the listing
				{IssueID: "rp-1", DependsOnID: "rp-1", Type: types.DepRelatesTo},    // self is not a real edge; still raw
			}}
			got := gate(src, positiveGap, depListing{rows: 1}, hydrated)
			if len(got) != 2 {
				t.Fatalf("unresolved edges = %v, want the 2 rows the listing dropped (liveop-9, rp-1)", got)
			}
			if src.calls != 1 {
				t.Errorf("raw read called %d times, want 1", src.calls)
			}
			if !src.isWisp {
				t.Errorf("gate did not pass isWisp through to the source")
			}
		})
		t.Run("no gap means no raw read", func(t *testing.T) {
			src := &fakeUnresolvedEdgeSource{raw: []*types.Dependency{{DependsOnID: "liveop-9"}}}
			if got := gate(src, depCounts{deps: depCount{n: 1}}, depListing{rows: 1}, nil); got != nil {
				t.Errorf("no gap must yield no edges, got %v", got)
			}
			if src.calls != 0 {
				t.Errorf("a fully local issue must not pay for the raw read, which ran %d times", src.calls)
			}
		})
		t.Run("failed listing read means no raw read", func(t *testing.T) {
			src := &fakeUnresolvedEdgeSource{raw: []*types.Dependency{{DependsOnID: "liveop-9"}}}
			if got := gate(src, positiveGap, depListing{rows: 0, err: boom}, nil); got != nil {
				t.Errorf("a failed listing must yield no edges, got %v", got)
			}
			if src.calls != 0 {
				t.Errorf("a failed listing must not trigger the raw read, which ran %d times", src.calls)
			}
		})
		t.Run("failed count read means no raw read", func(t *testing.T) {
			src := &fakeUnresolvedEdgeSource{raw: []*types.Dependency{{DependsOnID: "liveop-9"}}}
			if got := gate(src, depCounts{deps: depCount{err: boom}}, depListing{rows: 0}, nil); got != nil {
				t.Errorf("a failed count must yield no edges, got %v", got)
			}
			if src.calls != 0 {
				t.Errorf("a failed count must not trigger the raw read, which ran %d times", src.calls)
			}
		})
		t.Run("failed raw read yields nil, keeping the full gap for the warning", func(t *testing.T) {
			src := &fakeUnresolvedEdgeSource{rawErr: boom}
			if got := gate(src, positiveGap, depListing{rows: 0}, nil); got != nil {
				t.Errorf("a failed raw read must yield no edges, got %v", got)
			}
			if src.calls != 1 {
				t.Errorf("the raw read should have been attempted once, ran %d times", src.calls)
			}
		})
		t.Run("nil source yields nil", func(t *testing.T) {
			if got := gateUnresolvedDepEdges(context.Background(), nil, "rp-1", false, positiveGap, depListing{rows: 0}, nil); got != nil {
				t.Errorf("a nil source must yield no edges, got %v", got)
			}
		})
	})

	// The merge: fold raw rows into the grouped local sections without
	// reordering the local ones.
	t.Run("mergeUnresolvedDepEdges", func(t *testing.T) {
		unresolvedBlocks := []*types.Dependency{
			{IssueID: "rp-1", DependsOnID: "liveop-9", Type: types.DepBlocks},
		}
		t.Run("folds after the local lines of the same section", func(t *testing.T) {
			sections := groupDepSections([]*types.IssueWithDependencyMetadata{
				localDepRow("rp-2", "local blocker", types.DepBlocks),
			}, true, nil)
			merged, related := mergeUnresolvedDepEdges(sections, unresolvedBlocks, "rp-1", true)
			if len(related) != 0 {
				t.Errorf("related = %v, want none", related)
			}
			if len(merged) != 1 || merged[0].Type != types.DepBlocks {
				t.Fatalf("merged sections = %+v, want the one DEPENDS ON section", merged)
			}
			if len(merged[0].Deps) != 1 {
				t.Errorf("local rows = %v, want the one hydrated local edge", merged[0].Deps)
			}
			if len(merged[0].Unresolved) != 1 || merged[0].Unresolved[0].DependsOnID != "liveop-9" {
				t.Errorf("unresolved rows = %v, want liveop-9", merged[0].Unresolved)
			}
			if merged[0].AnchorID != "rp-1" {
				t.Errorf("AnchorID = %q, want the shown issue rp-1", merged[0].AnchorID)
			}
		})
		t.Run("creates the missing section in the fixed order, not at the end", func(t *testing.T) {
			// Local TRACKS section only; the unresolved edge is BLOCKS, which
			// depSectionOrder prints BEFORE tracks. Appending the created
			// section at the end would reorder the local sections.
			sections := groupDepSections([]*types.IssueWithDependencyMetadata{
				localDepRow("rp-3", "tracked", types.DepTracks),
			}, true, nil)
			merged, _ := mergeUnresolvedDepEdges(sections, unresolvedBlocks, "rp-1", true)
			if len(merged) != 2 {
				t.Fatalf("merged sections = %+v, want DEPENDS ON (created) then TRACKS (local)", merged)
			}
			if merged[0].Type != types.DepBlocks || merged[0].Heading != "DEPENDS ON" {
				t.Errorf("first merged section = %+v, want the created outbound DEPENDS ON one", merged[0])
			}
			if merged[1].Type != types.DepTracks {
				t.Errorf("second merged section = %+v, want the untouched local TRACKS one", merged[1])
			}
			if len(merged[0].Deps) != 0 {
				t.Errorf("created section must carry no invented local rows, got %v", merged[0].Deps)
			}
		})
		t.Run("inbound unresolved rows create the inbound section", func(t *testing.T) {
			merged, _ := mergeUnresolvedDepEdges(nil, unresolvedBlocks, "rp-1", false)
			if len(merged) != 1 || merged[0].Heading != "BLOCKS" {
				t.Errorf("merged sections = %+v, want one inbound BLOCKS section", merged)
			}
		})
		t.Run("related rows divert and dedup, never fold into a section", func(t *testing.T) {
			relatedRows := []*types.Dependency{
				{IssueID: "rp-1", DependsOnID: "liveop-a", Type: types.DepRelated},
				{IssueID: "rp-1", DependsOnID: "liveop-a", Type: types.DepRelatesTo}, // same far id: one line
				{IssueID: "rp-1", DependsOnID: "liveop-b", Type: types.DepRelatesTo},
			}
			merged, related := mergeUnresolvedDepEdges(nil, relatedRows, "rp-1", true)
			if len(merged) != 0 {
				t.Errorf("related rows must not create their own sections, got %+v", merged)
			}
			if len(related) != 2 {
				t.Fatalf("diverted related rows = %v, want liveop-a and liveop-b, deduped", related)
			}
		})
		t.Run("empty unresolved leaves the sections untouched", func(t *testing.T) {
			sections := groupDepSections([]*types.IssueWithDependencyMetadata{
				localDepRow("rp-2", "local blocker", types.DepBlocks),
			}, true, nil)
			merged, related := mergeUnresolvedDepEdges(sections, nil, "rp-1", true)
			if len(merged) != 1 || merged[0].Type != sections[0].Type || merged[0].Deps[0] != sections[0].Deps[0] || len(merged[0].Unresolved) != 0 {
				t.Errorf("sections = %+v, want the input sections unchanged", merged)
			}
			if related != nil {
				t.Errorf("related = %v, want nil", related)
			}
		})
		t.Run("input sections are not mutated", func(t *testing.T) {
			sections := groupDepSections([]*types.IssueWithDependencyMetadata{
				localDepRow("rp-2", "local blocker", types.DepBlocks),
			}, true, nil)
			mergeUnresolvedDepEdges(sections, unresolvedBlocks, "rp-1", true)
			if len(sections[0].Unresolved) != 0 || sections[0].AnchorID != "" {
				t.Errorf("merge must rebuild, not mutate: %+v", sections[0])
			}
		})
	})

	// The line: glyph, BLANK status slot, the id as printedIssueID spells it,
	// and the heuristic label.
	t.Run("formatUnresolvedDependencyLine", func(t *testing.T) {
		line := func(target string) string {
			return formatUnresolvedDependencyLine("→", &types.Dependency{IssueID: "rp-1", DependsOnID: target, Type: types.DepBlocks}, "rp-1")
		}
		t.Run("external reference is labeled external", func(t *testing.T) {
			out := line("external:liveop:liveop-9")
			if !strings.Contains(out, "external:liveop:liveop-9") || !strings.Contains(out, "(external)") {
				t.Errorf("line %q must show the id as stored and label it external", out)
			}
		})
		t.Run("other-prefix id is labeled cross-repo", func(t *testing.T) {
			out := line("liveop-9")
			if !strings.Contains(out, "(cross-repo)") {
				t.Errorf("line %q must label a foreign-prefix id cross-repo", out)
			}
		})
		t.Run("own-prefix id is labeled missing", func(t *testing.T) {
			out := line("rp-gone")
			if !strings.Contains(out, "(missing)") {
				t.Errorf("line %q must label an own-prefix id missing", out)
			}
		})
		t.Run("the status slot stays blank", func(t *testing.T) {
			// The local line is "  → <icon> <id>: ..." — a blank icon slot
			// leaves exactly three spaces between glyph and id.
			if !strings.Contains(line("liveop-9"), "→   liveop-9") {
				t.Errorf("line %q must keep the status slot blank where a local line renders an icon", line("liveop-9"))
			}
		})
		t.Run("the id goes through printedIssueID", func(t *testing.T) {
			out := line(`liveop$(touch /tmp/pwned)`)
			if strings.Contains(out, "$(touch") {
				t.Errorf("line %q must escape the id rather than print it raw", out)
			}
		})
	})

	// The printed sections: heading, local lines, then the raw lines.
	t.Run("printed sections", func(t *testing.T) {
		hydrated := []*types.IssueWithDependencyMetadata{
			localDepRow("rp-2", "local blocker", types.DepBlocks),
			localDepRow("rp-4", "a related local issue", types.DepRelated),
		}
		unresolved := []*types.Dependency{
			{IssueID: "rp-1", DependsOnID: "liveop-9", Type: types.DepBlocks},
			{IssueID: "rp-1", DependsOnID: "liveop-z", Type: types.DepRelatesTo},
		}
		relatedSeen := make(map[string]*types.IssueWithDependencyMetadata)
		sections := groupDepSections(hydrated, true, relatedSeen)
		sections, relatedUnresolved := mergeUnresolvedDepEdges(sections, unresolved, "rp-1", true)
		out := captureStdout(t, func() error {
			for _, sec := range sections {
				printDepSection(sec)
			}
			printRelatedSection(relatedSeen, relatedUnresolved, "rp-1")
			return nil
		})
		if !strings.Contains(out, "DEPENDS ON") {
			t.Errorf("output missing the section heading:\n%s", out)
		}
		if localAt, errAt := strings.Index(out, "local blocker"), strings.Index(out, "liveop-9"); localAt < 0 || errAt < localAt {
			t.Errorf("the raw line must print after the local line of its section:\n%s", out)
		}
		if !strings.Contains(out, "(cross-repo)") {
			t.Errorf("the raw line must carry its classification label:\n%s", out)
		}
		if relatedAt, localRelatedAt := strings.Index(out, "liveop-z"), strings.Index(out, "a related local issue"); relatedAt < 0 || relatedAt < localRelatedAt {
			t.Errorf("the RELATED section must print its local line then its raw line:\n%s", out)
		}
	})

	t.Run("distinctUnresolvedTargets", func(t *testing.T) {
		rows := []*types.Dependency{
			{DependsOnID: "liveop-a"},
			{DependsOnID: "liveop-a"}, // same id: one
			{DependsOnID: "liveop-b"},
			nil, // a nil row is skipped, not counted
		}
		if n := distinctUnresolvedTargets(rows); n != 2 {
			t.Errorf("distinctUnresolvedTargets = %d, want 2", n)
		}
		if n := distinctUnresolvedTargets(nil); n != 0 {
			t.Errorf("distinctUnresolvedTargets(nil) = %d, want 0", n)
		}
	})

	// The failure rules that tie the gate and the warning together (AC-5,
	// bd-hkpas7, #5272). The three renders compose exactly these calls —
	// gate, then warn with what the render printed — so these compose them
	// too, at the seam no CLI test can reach: a backend that fails only the
	// raw read, or prints only part of the gap.
	t.Run("render failure composition", func(t *testing.T) {
		t.Run("a failed raw read leaves the whole gap to the warning", func(t *testing.T) {
			src := &fakeUnresolvedEdgeSource{rawErr: boom}
			counts := depCounts{deps: depCount{n: 2}}
			unresolved := gateUnresolvedDepEdges(context.Background(), src, "rp-1", false, counts, depListing{rows: 0}, nil)
			if unresolved != nil {
				t.Fatalf("a failed raw read must publish no edges, got %v", unresolved)
			}
			if src.calls != 1 {
				t.Errorf("the raw read should have been attempted once, ran %d times", src.calls)
			}
			out := captureStderr(t, func() {
				warnUnresolvableDepEdges("rp-1", counts,
					depListing{rows: 0, printedUnresolved: distinctUnresolvedTargets(unresolved)},
					depListing{rows: 0})
			})
			// The render disclosed less, not nothing: the warning still names
			// the full measured gap and the command that can show it.
			if !strings.Contains(out, "2 dependency edge(s)") || !strings.Contains(out, "For raw edge records") {
				t.Errorf("a failed raw read must leave the whole gap to the warning, got:\n%s", out)
			}
		})
		t.Run("a printed gap goes silent end to end", func(t *testing.T) {
			src := &fakeUnresolvedEdgeSource{raw: []*types.Dependency{
				{IssueID: "rp-1", DependsOnID: "liveop-9", Type: types.DepBlocks},
			}}
			counts := depCounts{deps: depCount{n: 1}}
			unresolved := gateUnresolvedDepEdges(context.Background(), src, "rp-1", false, counts, depListing{rows: 0}, nil)
			if len(unresolved) != 1 {
				t.Fatalf("the gap edge must come back for printing, got %v", unresolved)
			}
			out := captureStderr(t, func() {
				warnUnresolvableDepEdges("rp-1", counts,
					depListing{rows: 0, printedUnresolved: distinctUnresolvedTargets(unresolved)},
					depListing{rows: 0})
			})
			if out != "" {
				t.Errorf("a fully printed gap must leave the warning silent, got:\n%s", out)
			}
		})
	})
}
