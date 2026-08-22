package bench

import "strings"

// PlanReferenceCount counts occurrences of marker in explainOutput — the
// harness's stand-in for "how many times does the driver filter appear in
// the plan," the signal that surfaced PR #5339's regression (main referenced
// the driver filter once; the CTE form inlined it at 8 references). Counts
// every occurrence, including more than one on the same line; matching is
// case-sensitive since EXPLAIN table/alias names are. An empty marker counts
// as never present rather than matching every position.
func PlanReferenceCount(explainOutput, marker string) int {
	if marker == "" {
		return 0
	}
	return strings.Count(explainOutput, marker)
}
