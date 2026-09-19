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
		got := groupDepSections(deps, true, map[string]*types.IssueWithDependencyMetadata{})
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
		}, false, map[string]*types.IssueWithDependencyMetadata{})
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
		}, false, nil)
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
		}, true, relatedSeen)
		in := groupDepSections([]*types.IssueWithDependencyMetadata{
			dep("c-rel", types.DepRelated),
		}, false, relatedSeen)
		if len(out) != 0 || len(in) != 0 {
			t.Errorf("related edges should not form their own sections: out=%+v in=%+v", out, in)
		}
		if len(relatedSeen) != 1 {
			t.Errorf("relatedSeen = %d entries, want 1 (deduplicated)", len(relatedSeen))
		}
	})
}
