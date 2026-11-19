package workapi

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestBuildIssueDetails_ConcurrentDeleteIsNotReportedAsUnresolvable pins the
// stale-high half of the GH#5850 disclosure.
//
// A delete landing between the two reads BuildIssueDetails makes leaves the
// aggregate count one higher than the surviving rows: the count query saw two
// edges, and by the time the combined read ran, the second edge was gone —
// deleted, not unrenderable. Its target never lacked a local row; the row
// existed when the edge was written and the edge stopped existing before
// either read of this detail call finished.
//
// Before GH#5850 the outgoing disclosure was the count-minus-rows delta, so
// this window manufactured `unresolvable_dependencies: 1` out of a routine
// delete: a phantom "edge we cannot show you" that pointed at an issue that
// had been perfectly resolvable all along. Since GH#5850 the disclosure comes
// from the combined read's own records, and the deleted edge appears in none
// of them — the stale count is reported as the plain cardinality it is and
// invents no member.
//
// The source is fixed rather than mutating between calls because the skew
// under test is the DATA, not the order: the count is stale-high by
// construction and both reads return it consistently, which is the only way
// to keep the assertion about the delta rather than about read ordering (the
// add-side window is pinned with a mutating source in
// TestBuildIssueDetails_ConcurrentAddIsNotReportedAsUnresolvable, and the
// count-read-failure reversal is a table case in
// TestBuildIssueDetails_UnresolvableDependencies).
func TestBuildIssueDetails_ConcurrentDeleteIsNotReportedAsUnresolvable(t *testing.T) {
	ctx := context.Background()
	issue := &types.Issue{ID: "rp-1"}
	src := skewSource{
		issue: issue,
		// The combined read's answer: one edge survived the delete, and
		// its target resolves. No record of a missing far end exists.
		deps: []*types.IssueWithDependencyMetadata{depRow("rp-2", types.DepBlocks)},
		// The aggregate count, read before the delete landed: two.
		depCount: 2,
	}

	details, err := BuildIssueDetails(ctx, src, issue, false, DetailOptions{})
	if err != nil {
		t.Fatalf("BuildIssueDetails: %v", err)
	}

	// The stale count itself stays visible: dependency_count is a plain
	// cardinality and a concurrent delete is not allowed to rewrite it.
	if details.DependencyCount == nil || *details.DependencyCount != 2 {
		t.Errorf("DependencyCount = %v, want 2 — the aggregate count is reported as read",
			details.DependencyCount)
	}

	// But it must not become a disclosure. Both members are set together
	// from the combined read's records, and there are none.
	if details.UnresolvableDependencies != nil {
		t.Errorf("UnresolvableDependencies = %d, want unset — a count gone stale-high under a concurrent delete is not evidence of an unrenderable edge",
			*details.UnresolvableDependencies)
	}
	if details.UnresolvableDependencyEdges != nil {
		t.Errorf("UnresolvableDependencyEdges = %v, want unset — the combined read answered no missing targets",
			details.UnresolvableDependencyEdges)
	}

	// And the surviving local edge is still shown as the real issue it is.
	if len(details.Dependencies) != 1 {
		t.Fatalf("dependencies = %v, want the one surviving local edge", details.Dependencies)
	}
	if details.Dependencies[0].ID != "rp-2" {
		t.Errorf("dependencies[0].ID = %q, want rp-2", details.Dependencies[0].ID)
	}
}
