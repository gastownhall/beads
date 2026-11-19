package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
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

// AC-3 (bd-uschhs, #5850): an outgoing edge whose far end has no row here
// joins the section of its own type, after the resolved rows and sorted by
// target, and a type holding nothing else still gets its section. The
// symmetric related records are handed back for the one deduplicated RELATED
// section, the way resolved related edges are.
func TestWithUnresolvedSections(t *testing.T) {
	rec := func(target string, typ types.DependencyType) *types.Dependency {
		return &types.Dependency{IssueID: "rp-1", DependsOnID: target, Type: typ}
	}
	deps := []*types.IssueWithDependencyMetadata{
		{Issue: types.Issue{ID: "rp-2"}, DependencyType: types.DepBlocks},
	}
	unresolved := []*types.Dependency{
		rec("liveop-b", types.DepBlocks),
		rec("external:liveop:liveop-kmf", types.DepTracks),
		rec("liveop-a", types.DepBlocks),
		rec("external:other:x", types.DepRelated),
		rec("other-9", types.DependencyType("spawned")),
	}

	relatedSeen := map[string]*types.IssueWithDependencyMetadata{}
	sections, related := withUnresolvedSections(groupDepSections(deps, true, relatedSeen), unresolved, true)

	type shape struct {
		heading    string
		glyph      string
		resolved   []string
		unresolved []string
	}
	var got []shape
	for _, sec := range sections {
		s := shape{heading: sec.Heading, glyph: sec.Glyph}
		for _, d := range sec.Deps {
			s.resolved = append(s.resolved, d.ID)
		}
		for _, d := range sec.Unresolved {
			s.unresolved = append(s.unresolved, d.DependsOnID)
		}
		got = append(got, s)
	}
	want := []shape{
		{heading: "DEPENDS ON", glyph: "→", resolved: []string{"rp-2"}, unresolved: []string{"liveop-a", "liveop-b"}},
		{heading: "TRACKS", glyph: "→", unresolved: []string{"external:liveop:liveop-kmf"}},
		{heading: "SPAWNED", glyph: "→", unresolved: []string{"other-9"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sections = %+v\nwant       %+v", got, want)
	}
	if len(related) != 1 || related[0].DependsOnID != "external:other:x" {
		t.Errorf("related unresolved = %+v, want the one related record", related)
	}
	if len(relatedSeen) != 0 {
		t.Errorf("relatedSeen = %v, want no fabricated issue rows", relatedSeen)
	}

	// Without a relatedSeen map, related records get an ordinary trailing
	// section like resolved related edges do.
	sections, related = withUnresolvedSections(groupDepSections(nil, true, nil), unresolved[3:4], false)
	if len(related) != 0 || len(sections) != 1 || sections[0].Heading != "RELATED" || len(sections[0].Unresolved) != 1 {
		t.Errorf("nil relatedSeen: sections=%+v related=%+v, want one RELATED section holding the record", sections, related)
	}
}

// AC-3: the rendered line is the section glyph, the exact stored target and a
// muted "(not in this database)" — no status, title or priority, which the
// edge does not have — and a section or RELATED block holding only such a
// line still prints its heading.
func TestPrintUnresolvedDependencyLines(t *testing.T) {
	sec := depSection{
		Type: types.DepTracks, Heading: "TRACKS", Glyph: "→",
		Unresolved: []*types.Dependency{{IssueID: "rp-1", DependsOnID: "external:liveop:liveop-kmf", Type: types.DepTracks}},
	}
	out := captureStdout(t, func() error { printDepSection(sec); return nil })
	if !strings.Contains(out, "TRACKS") || !strings.Contains(out, "→ external:liveop:liveop-kmf") || !strings.Contains(out, "(not in this database)") {
		t.Errorf("TRACKS section with only an unresolved record printed:\n%q", out)
	}

	out = captureStdout(t, func() error {
		printRelatedSection(map[string]*types.IssueWithDependencyMetadata{},
			[]*types.Dependency{{IssueID: "rp-1", DependsOnID: "external:other:x", Type: types.DepRelated}})
		return nil
	})
	if !strings.Contains(out, "RELATED") || !strings.Contains(out, "↔ external:other:x") || !strings.Contains(out, "(not in this database)") {
		t.Errorf("RELATED with only an unresolved record printed:\n%q", out)
	}

	out = captureStdout(t, func() error { printRelatedSection(nil, nil); return nil })
	if out != "" {
		t.Errorf("RELATED with nothing printed %q, want nothing", out)
	}
}

// AC-3: a listing that named its unresolvable edges itself has rendered them,
// so the outgoing count warning stays silent however the count reads; the
// inbound warning, which has no records, is unchanged.
func TestWarnUnresolvableDepEdgesSkipsANamedListing(t *testing.T) {
	counts := readDepCounts(context.Background(), fakeDepCounter{depCount: 3, rdepCount: 2}, "rp-1")
	out := captureStderr(t, func() {
		warnUnresolvableDepEdges("rp-1", counts, depListing{rows: 1, named: true}, depListing{rows: 1})
	})
	if strings.Contains(out, "dependency edge(s)") || strings.Contains(out, "For raw edge records") {
		t.Errorf("outgoing warning printed for a named listing:\n%q", out)
	}
	if !strings.Contains(out, "1 dependent edge(s)") {
		t.Errorf("inbound warning lost:\n%q", out)
	}
}

// fakeOutgoingReader answers show's two outgoing reads; combinedErr makes the
// combined read refuse as a backend that cannot name its records does.
type fakeOutgoingReader struct {
	combinedErr  error
	resolved     []*types.IssueWithDependencyMetadata
	unresolvable []*types.Dependency
}

func (f fakeOutgoingReader) GetDependenciesWithUnresolvable(context.Context, string) ([]*types.IssueWithDependencyMetadata, []*types.Dependency, error) {
	if f.combinedErr != nil {
		return nil, nil, f.combinedErr
	}
	return f.resolved, f.unresolvable, nil
}

func (f fakeOutgoingReader) GetDependenciesWithMetadata(context.Context, string) ([]*types.IssueWithDependencyMetadata, error) {
	return f.resolved, nil
}

// AC-7 (bd-uschhs): an http workspace on a server older than
// unresolvable_dependency_edges refuses the combined read with a typed
// *ErrUnsupported. bd show falls back to the issue-only listing as an UNNAMED
// listing, so the count-only warning still discloses the edge; a server that
// names its records keeps the named listing and the warning stays silent.
func TestReadOutgoingDepsFallsBackWhenTheRecordsAreUnsupported(t *testing.T) {
	ctx := context.Background()
	local := []*types.IssueWithDependencyMetadata{{Issue: types.Issue{ID: "ud-b"}, DependencyType: types.DepBlocks}}
	record := []*types.Dependency{{IssueID: "ud-a", DependsOnID: "liveop-kmf", Type: types.DepBlocks}}
	counts := depCounts{deps: depCount{n: 2}}

	deps, unresolved, named, err := readOutgoingDeps(ctx, fakeOutgoingReader{
		combinedErr: &issueops.ErrUnsupported{Op: "GetDependenciesWithUnresolvable", Backend: "http"},
		resolved:    local,
	}, "ud-a")
	if err != nil || named || len(deps) != 1 || unresolved != nil {
		t.Fatalf("legacy server: got (%v, %v, named=%v, %v), want (one local row, nil, false, nil)", deps, unresolved, named, err)
	}
	stderr := captureStderr(t, func() {
		warnUnresolvableDepEdges("ud-a", counts, depListing{rows: len(deps), err: err, named: named}, depListing{})
	})
	if !strings.Contains(stderr, "has 1 dependency edge(s)") {
		t.Errorf("legacy server: the count-only warning is gone:\n%s", stderr)
	}

	deps, unresolved, named, err = readOutgoingDeps(ctx, fakeOutgoingReader{resolved: local, unresolvable: record}, "ud-a")
	if err != nil || !named || len(deps) != 1 || len(unresolved) != 1 {
		t.Fatalf("current server: got (%v, %v, named=%v, %v), want (one local row, one record, true, nil)", deps, unresolved, named, err)
	}
	stderr = captureStderr(t, func() {
		warnUnresolvableDepEdges("ud-a", counts, depListing{rows: len(deps), err: err, named: named}, depListing{})
	})
	if stderr != "" {
		t.Errorf("current server: a named listing still warned:\n%s", stderr)
	}

	other := errors.New("boom")
	if _, _, _, err := readOutgoingDeps(ctx, fakeOutgoingReader{combinedErr: other}, "ud-a"); !errors.Is(err, other) {
		t.Errorf("a non-capability error = %v, want it returned unchanged", err)
	}
}
