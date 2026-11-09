package doctor

import (
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPollutionCandidateFilter pins the load filter both pollution checks
// share: open, non-epic, Ephemeral=false issues, with no row cap of its own.
func TestPollutionCandidateFilter(t *testing.T) {
	f := PollutionCandidateFilter()
	if f.Status == nil || *f.Status != types.StatusOpen {
		t.Errorf("Status = %v, want open", f.Status)
	}
	if !slices.Equal(f.ExcludeTypes, []types.IssueType{types.TypeEpic}) {
		t.Errorf("ExcludeTypes = %v, want [epic]", f.ExcludeTypes)
	}
	if f.Ephemeral == nil || *f.Ephemeral {
		t.Errorf("Ephemeral = %v, want false (NoHistory rows stay eligible)", f.Ephemeral)
	}
	if f.MaxRows != 0 {
		t.Errorf("MaxRows = %d, want 0: the gate counts every candidate", f.MaxRows)
	}
}
