package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The bug this guards: bd show used to group every type it did not name
// explicitly into the blocks bucket, so a supersedes edge printed under
// "DEPENDS ON" one way and "BLOCKS" the other — a blocking claim that is false
// for every non-blocking type.
func TestDepRelation_OnlyBlocksClaimsBlocking(t *testing.T) {
	for _, dt := range types.WellKnownDependencyTypes() {
		rel := depRelationFor(dt)
		if rel.outHeading == "" || rel.inHeading == "" || rel.phrase == "" {
			t.Errorf("%s: incomplete relation %+v", dt, rel)
		}
		if dt == types.DepBlocks {
			continue
		}
		if rel.outHeading == "DEPENDS ON" || rel.inHeading == "BLOCKS" {
			t.Errorf("%s borrows blocks' verbiage: out=%q in=%q", dt, rel.outHeading, rel.inHeading)
		}
	}
}

func TestDepRelation_Supersedes(t *testing.T) {
	// `bd supersede old --with new` stores (old, new, supersedes), so the
	// source end is the replaced issue and the target end is the replacement.
	rel := depRelationFor(types.DepSupersedes)
	if rel.outHeading != "SUPERSEDED BY" || rel.inHeading != "SUPERSEDES" {
		t.Errorf("supersedes reads backwards: out=%q in=%q", rel.outHeading, rel.inHeading)
	}
}

func TestDepRelationFor_CustomTypeKeepsItsName(t *testing.T) {
	rel := depRelationFor(types.DependencyType("bikeshed-color"))
	if rel.outHeading != "BIKESHED-COLOR" || rel.phrase != "bikeshed-color" {
		t.Errorf("custom type lost its name: %+v", rel)
	}
}

func TestGroupDepSections(t *testing.T) {
	dep := func(id string, dt types.DependencyType) *types.IssueWithDependencyMetadata {
		return &types.IssueWithDependencyMetadata{
			Issue:          types.Issue{ID: id},
			DependencyType: dt,
		}
	}

	t.Run("one section per type, legacy order first", func(t *testing.T) {
		deps := []*types.IssueWithDependencyMetadata{
			dep("a-sup", types.DepSupersedes),
			dep("a-disc", types.DepDiscoveredFrom),
			dep("a-blk", types.DepBlocks),
			dep("a-custom", types.DependencyType("zz-custom")),
			dep("a-parent", types.DepParentChild),
		}
		got := groupDepSections(deps, nil, true, map[string]*types.IssueWithDependencyMetadata{})
		want := []string{"PARENT", "DEPENDS ON", "DISCOVERED FROM", "SUPERSEDED BY", "ZZ-CUSTOM"}
		if len(got) != len(want) {
			t.Fatalf("got %d sections, want %d: %+v", len(got), len(want), got)
		}
		for i, sec := range got {
			if sec.Heading != want[i] {
				t.Errorf("section %d = %q, want %q", i, sec.Heading, want[i])
			}
			if len(sec.Deps) != 1 {
				t.Errorf("section %q holds %d edges, want 1", sec.Heading, len(sec.Deps))
			}
		}
	})

	t.Run("incoming direction flips the heading", func(t *testing.T) {
		got := groupDepSections([]*types.IssueWithDependencyMetadata{
			dep("b-sup", types.DepSupersedes),
		}, nil, false, map[string]*types.IssueWithDependencyMetadata{})
		if len(got) != 1 || got[0].Heading != "SUPERSEDES" {
			t.Fatalf("got %+v, want one SUPERSEDES section", got)
		}
	})

	// bd show --refs reads one direction only, so it takes RELATED in place
	// rather than deduplicating it against a second pass.
	t.Run("nil relatedSeen keeps RELATED as a trailing section", func(t *testing.T) {
		got := groupDepSections([]*types.IssueWithDependencyMetadata{
			dep("d-rel", types.DepRelatesTo),
			dep("d-blk", types.DepBlocks),
			dep("d-rel2", types.DepRelated),
		}, nil, false, nil)
		want := []string{"BLOCKS", "RELATED"}
		if len(got) != len(want) {
			t.Fatalf("got %d sections, want %d: %+v", len(got), len(want), got)
		}
		for i, sec := range got {
			if sec.Heading != want[i] {
				t.Errorf("section %d = %q, want %q", i, sec.Heading, want[i])
			}
		}
		if n := len(got[1].Deps); n != 2 {
			t.Errorf("RELATED holds %d edges, want both spellings merged into 2", n)
		}
	})

	t.Run("related edges collapse across both directions", func(t *testing.T) {
		relatedSeen := map[string]*types.IssueWithDependencyMetadata{}
		out := groupDepSections([]*types.IssueWithDependencyMetadata{
			dep("c-rel", types.DepRelatesTo),
		}, nil, true, relatedSeen)
		in := groupDepSections([]*types.IssueWithDependencyMetadata{
			dep("c-rel", types.DepRelated),
		}, nil, false, relatedSeen)
		if len(out) != 0 || len(in) != 0 {
			t.Errorf("related edges should not form their own sections: out=%+v in=%+v", out, in)
		}
		if len(relatedSeen) != 1 {
			t.Errorf("relatedSeen = %d entries, want 1 (deduplicated)", len(relatedSeen))
		}
	})

	// GH#5850: the stored records of edges whose far end has no row in this
	// database group into the section for their own type, beside the hydrated
	// rows — and alone when the type has none, which is the lone-section case
	// that used to render as no dependency at all.
	t.Run("missing far ends join their type's section", func(t *testing.T) {
		got := groupDepSections([]*types.IssueWithDependencyMetadata{
			dep("e-blk", types.DepBlocks),
		}, []*types.Dependency{
			{IssueID: "anchor", DependsOnID: "liveop-x", Type: types.DepBlocks, CreatedBy: "porter"},
			{IssueID: "anchor", DependsOnID: "external:liveop:y", Type: types.DepTracks, CreatedBy: "porter"},
		}, true, map[string]*types.IssueWithDependencyMetadata{})
		if len(got) != 2 {
			t.Fatalf("got %d sections, want DEPENDS ON and TRACKS: %+v", len(got), got)
		}
		byHeading := map[string]depSection{}
		for _, sec := range got {
			byHeading[sec.Heading] = sec
		}
		blk := byHeading["DEPENDS ON"]
		if len(blk.Deps) != 1 || len(blk.Missing) != 1 || blk.Missing[0] != "liveop-x" {
			t.Errorf("DEPENDS ON = %+v, want one hydrated row and the cross-repo far end", blk)
		}
		trk := byHeading["TRACKS"]
		if len(trk.Deps) != 0 || len(trk.Missing) != 1 || trk.Missing[0] != "external:liveop:y" {
			t.Errorf("TRACKS = %+v, want a lone missing entry and no hydrated rows", trk)
		}
	})

	// The far end of a stored edge depends on the direction: DependsOnID when
	// the anchor is the source, IssueID when it is the target. A caller
	// grouping the inbound direction with missing records must see the
	// DEPENDENT's id, not the anchor's own.
	t.Run("incoming missing far ends print the edge's source", func(t *testing.T) {
		got := groupDepSections(nil, []*types.Dependency{
			{IssueID: "liveop-z", DependsOnID: "anchor", Type: types.DepBlocks, CreatedBy: "porter"},
		}, false, nil)
		if len(got) != 1 || got[0].Heading != "BLOCKS" {
			t.Fatalf("got %+v, want one BLOCKS section", got)
		}
		if len(got[0].Missing) != 1 || got[0].Missing[0] != "liveop-z" {
			t.Errorf("Missing = %v, want the dependent liveop-z", got[0].Missing)
		}
	})

	// A missing related edge is NOT diverted into relatedSeen — the map holds
	// hydrated issues, the far end here has none — so it keeps a section of
	// its own rather than vanishing a second time.
	t.Run("missing related edge keeps its section", func(t *testing.T) {
		relatedSeen := map[string]*types.IssueWithDependencyMetadata{}
		got := groupDepSections(nil, []*types.Dependency{
			{IssueID: "anchor", DependsOnID: "liveop-w", Type: types.DepRelated, CreatedBy: "porter"},
		}, true, relatedSeen)
		if len(got) != 1 || got[0].Heading != "RELATED" {
			t.Fatalf("got %+v, want one RELATED section for the half-edge", got)
		}
		if len(relatedSeen) != 0 {
			t.Errorf("relatedSeen = %v, want empty: a half-edge cannot dedup against ids this database cannot resolve", relatedSeen)
		}
	})

	// A missing edge of a custom type trails the built-ins, sorted by name,
	// exactly like a hydrated one.
	t.Run("custom-type missing edge forms its own section", func(t *testing.T) {
		got := groupDepSections([]*types.IssueWithDependencyMetadata{
			dep("f-blk", types.DepBlocks),
		}, []*types.Dependency{
			{IssueID: "anchor", DependsOnID: "liveop-v", Type: types.DependencyType("aa-missing"), CreatedBy: "porter"},
		}, true, map[string]*types.IssueWithDependencyMetadata{})
		if len(got) != 2 || got[0].Heading != "DEPENDS ON" || got[1].Heading != "AA-MISSING" {
			t.Fatalf("got %+v, want DEPENDS ON then AA-MISSING", got)
		}
		if len(got[1].Missing) != 1 || got[1].Missing[0] != "liveop-v" {
			t.Errorf("AA-MISSING Missing = %v, want liveop-v", got[1].Missing)
		}
	})
}
