package importer

import (
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// GetHierarchyDepth returns the depth of a hierarchical issue ID.
// Depth is determined by the number of dots in the ID.
// Examples:
//   - "bd-abc123" → 0 (top-level)
//   - "bd-abc123.1" → 1 (one level deep)
//   - "bd-abc123.1.2" → 2 (two levels deep)
func GetHierarchyDepth(id string) int {
	return strings.Count(id, ".")
}

// SortByDepth sorts issues by hierarchy depth (shallow to deep) with stable sorting.
// Issues at the same depth are sorted by ID for deterministic ordering.
// This ensures parent issues are processed before their children.
func SortByDepth(issues []*types.Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		depthI := GetHierarchyDepth(issues[i].ID)
		depthJ := GetHierarchyDepth(issues[j].ID)
		if depthI != depthJ {
			return depthI < depthJ
		}
		return issues[i].ID < issues[j].ID
	})
}

// GroupByDepth groups issues into buckets by hierarchy depth.
// Returns a map where keys are depth levels and values are slices of issues at that depth.
// Maximum supported depth is 3 (as per beads spec).
func GroupByDepth(issues []*types.Issue) map[int][]*types.Issue {
	groups := make(map[int][]*types.Issue)
	for _, issue := range issues {
		depth := GetHierarchyDepth(issue.ID)
		groups[depth] = append(groups[depth], issue)
	}
	return groups
}
