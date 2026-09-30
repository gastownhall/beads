package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestBdTypesListsEveryBuiltIn pins that `bd types` (core + system sections)
// lists exactly the types that validate without configuration, i.e. the set
// IssueType.IsBuiltIn accepts, each with a description.
func TestBdTypesListsEveryBuiltIn(t *testing.T) {
	listed := map[types.IssueType]bool{}
	for _, ct := range coreWorkTypes {
		if listed[ct.Type] {
			t.Errorf("core type %q listed twice", ct.Type)
		}
		listed[ct.Type] = true
	}
	for _, st := range systemWorkTypes() {
		it := types.IssueType(st.Name)
		if listed[it] {
			t.Errorf("type %q listed as both core and system", it)
		}
		if st.Description == "" {
			t.Errorf("system type %q has no description", it)
		}
		listed[it] = true
	}
	for it := range listed {
		if !it.IsBuiltIn() {
			t.Errorf("bd types lists %q as built-in, but IsBuiltIn rejects it", it)
		}
	}
	for _, it := range append(append([]types.IssueType{}, types.AllIssueTypes...), types.TypeEvent) {
		if !listed[it] {
			t.Errorf("built-in type %q validates without configuration but bd types does not list it", it)
		}
	}
}
