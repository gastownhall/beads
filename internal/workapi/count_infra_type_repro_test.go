package workapi

import (
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestReproCountInfraTypeMatchesListPlane: `bd count --type agent` must read
// the same plane as `bd list --type agent` (wisps tier, narrowed to ephemeral).
func TestReproCountInfraTypeMatchesListPlane(t *testing.T) {
	cfg := ListConfig{CustomTypes: []string{"agent", "role", "message"}}
	listFilter, err := BuildListFilter(issueops.ListRequest{IssueType: "agent"}, cfg)
	if err != nil {
		t.Fatalf("BuildListFilter: %v", err)
	}
	countFilter, err := BuildCountFilter(issueops.CountRequest{IssueType: "agent"}, cfg)
	if err != nil {
		t.Fatalf("BuildCountFilter: %v", err)
	}
	if countFilter.SkipWisps != listFilter.SkipWisps {
		t.Errorf("count SkipWisps=%v, list SkipWisps=%v: count of an infra type skips the wisps tier list reads",
			countFilter.SkipWisps, listFilter.SkipWisps)
	}
	listEph := listFilter.Ephemeral != nil && *listFilter.Ephemeral
	countEph := countFilter.Ephemeral != nil && *countFilter.Ephemeral
	if countEph != listEph {
		t.Errorf("count Ephemeral narrowing=%v, list=%v", countEph, listEph)
	}
}
