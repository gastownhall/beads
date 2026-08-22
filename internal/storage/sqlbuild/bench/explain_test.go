package bench_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlbuild/bench"
)

// TestPlanReferenceCount pins occurrence-based counting (not line-based): the
// PR #5339 story this harness models ("driver filter inlined at 8 references
// vs main's 1") counts how many times a marker appears in the plan text,
// including multiple occurrences on the same line.
func TestPlanReferenceCount(t *testing.T) {
	cases := []struct {
		name          string
		explainOutput string
		marker        string
		want          int
	}{
		{"empty output", "", "dependencies", 0},
		{"marker absent", "Filter\n  TableScan: issues\n", "dependencies", 0},
		{"single occurrence", "Filter\n  TableScan: dependencies\n", "dependencies", 1},
		{"multiple lines", "TableScan: dependencies\nTableScan: dependencies\nTableScan: dependencies\n", "dependencies", 3},
		{"multiple occurrences same line", "Join(dependencies, dependencies)\n", "dependencies", 2},
		{"case sensitive", "TableScan: DEPENDENCIES\n", "dependencies", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bench.PlanReferenceCount(tc.explainOutput, tc.marker)
			if got != tc.want {
				t.Errorf("PlanReferenceCount(%q, %q) = %d, want %d", tc.explainOutput, tc.marker, got, tc.want)
			}
		})
	}
}
