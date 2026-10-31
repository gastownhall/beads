package types

import "testing"

func TestDependencyTypeRelation(t *testing.T) {
	tests := []struct {
		dt      DependencyType
		out, in string
	}{
		{DepBlocks, "depends_on", "blocks"},
		{DepParentChild, "parent", "children"},
		// `bd duplicate dup --of canonical` stores (dup, canonical, duplicates):
		// the copy's dependency row is "duplicate_of" the canonical.
		{DepDuplicates, "duplicate_of", "duplicated_by"},
		// `bd supersede old --with new` stores (old, new, supersedes).
		{DepSupersedes, "superseded_by", "supersedes"},
		{DepRelated, "related", "related"},
		{DepRelatesTo, "related", "related"},
		{DepDiscoveredFrom, "discovered_from", "discovered"},
		{DepWaitsFor, "waits_for", "awaited_by"},
		{DepTracks, "tracks", "tracked_by"},
	}
	for _, tt := range tests {
		if got := tt.dt.Relation(true); got != tt.out {
			t.Errorf("%s outgoing = %q, want %q", tt.dt, got, tt.out)
		}
		if got := tt.dt.Relation(false); got != tt.in {
			t.Errorf("%s incoming = %q, want %q", tt.dt, got, tt.in)
		}
	}
}

func TestDependencyTypeRelation_CustomType(t *testing.T) {
	dt := DependencyType("bikeshed-color")
	if got := dt.Relation(true); got != "bikeshed-color" {
		t.Errorf("custom outgoing = %q, want %q", got, "bikeshed-color")
	}
	if got := dt.Relation(false); got != "bikeshed-color_inbound" {
		t.Errorf("custom incoming = %q, want %q", got, "bikeshed-color_inbound")
	}
}

// Every well-known type has its own dependencyRelations entry and names both
// ends, so no row ever ships an empty role word or one with a space in it. The
// presence check comes first: DependencyRelationFor and Relation fall back to
// a custom type's words for a type with no entry, and those pass the checks
// below.
func TestDependencyTypeRelation_WellKnownComplete(t *testing.T) {
	for _, dt := range WellKnownDependencyTypes() {
		if _, ok := dependencyRelations[dt]; !ok {
			t.Errorf("%s: well-known type has no dependencyRelations entry, so it reads as a custom type", dt)
			continue
		}
		rel := DependencyRelationFor(dt)
		if rel.OutHeading == "" || rel.InHeading == "" || rel.Phrase == "" {
			t.Errorf("%s: incomplete relation %+v", dt, rel)
		}
		for _, outgoing := range []bool{true, false} {
			word := dt.Relation(outgoing)
			for _, r := range word {
				if r != '_' && (r < 'a' || r > 'z') {
					t.Errorf("%s(outgoing=%v) = %q, want lower-case letters and underscores", dt, outgoing, word)
					break
				}
			}
		}
	}
}
