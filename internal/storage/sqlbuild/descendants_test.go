package sqlbuild_test

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

// TestDescendantWalkQueryRecursesOffBaseTables pins the shape that makes the
// walk index-friendly on Dolt (be-qfm): no materialized edge wrapper, and every
// recursive member joining a base table on a bare target column so
// idx_dep_type_* applies. A `parent_edges` wrapper re-scans ~4.6k rows per
// recursion row (7.46s for 483 descendants on the field database).
func TestDescendantWalkQueryRecursesOffBaseTables(t *testing.T) {
	t.Parallel()

	for _, includeWisps := range []bool{false, true} {
		q := sqlbuild.DescendantWalkQuery(includeWisps)

		if strings.Contains(q, "parent_edges") {
			t.Errorf("includeWisps=%v: query still materializes a parent_edges wrapper:\n%s", includeWisps, q)
		}
		if strings.Contains(q, "JOIN (") {
			t.Errorf("includeWisps=%v: query joins a derived table, not a base table:\n%s", includeWisps, q)
		}
		if strings.Contains(q, "COALESCE") {
			t.Errorf("includeWisps=%v: COALESCE on the join key defeats idx_dep_type_*:\n%s", includeWisps, q)
		}
		if !strings.Contains(q, "JOIN dependencies e ON e.depends_on_issue_id = d.id AND e.type = 'parent-child'") {
			t.Errorf("includeWisps=%v: missing the indexed issue-target recursive member:\n%s", includeWisps, q)
		}
		if got := strings.Count(q, "LOCATE(CONCAT(',', e.issue_id, ','), d.path) = 0"); got != recursiveMembers(includeWisps) {
			t.Errorf("includeWisps=%v: cycle guard on %d of %d recursive members", includeWisps, got, recursiveMembers(includeWisps))
		}
		if got := strings.Count(q, "(? <= 0 OR d.depth < ?)"); got != recursiveMembers(includeWisps) {
			t.Errorf("includeWisps=%v: maxDepth guard on %d of %d recursive members", includeWisps, got, recursiveMembers(includeWisps))
		}
		if got := strings.Count(q, "SELECT /*+ JOIN_ORDER(d,e) LOOKUP_JOIN(d,e) */ e.issue_id"); got != recursiveMembers(includeWisps) {
			t.Errorf("includeWisps=%v: lookup-join hints on %d of %d recursive members", includeWisps, got, recursiveMembers(includeWisps))
		}
		if got := strings.Count(q, "type = 'parent-child'"); got != 2*recursiveMembers(includeWisps) {
			t.Errorf("includeWisps=%v: dep-type filter on %d members, want %d", includeWisps, got, 2*recursiveMembers(includeWisps))
		}

		wispRefs := strings.Contains(q, "wisp_dependencies")
		if wispRefs != includeWisps {
			t.Errorf("includeWisps=%v: wisp_dependencies present=%v", includeWisps, wispRefs)
		}
	}
}

// TestDescendantWalkArgsMatchPlaceholders keeps the binding aligned with the
// generated member list; a drift here silently shifts rootID onto a maxDepth
// slot and returns the wrong subtree.
func TestDescendantWalkArgsMatchPlaceholders(t *testing.T) {
	t.Parallel()

	for _, includeWisps := range []bool{false, true} {
		q := sqlbuild.DescendantWalkQuery(includeWisps)
		args := sqlbuild.DescendantWalkArgs("root-1", 7, includeWisps)

		if want, got := strings.Count(q, "?"), len(args); want != got {
			t.Fatalf("includeWisps=%v: %d placeholders, %d args", includeWisps, want, got)
		}

		n := recursiveMembers(includeWisps)
		for i := 0; i < 2*n; i++ {
			if args[i] != "root-1" {
				t.Errorf("includeWisps=%v: anchor arg %d = %v, want root-1", includeWisps, i, args[i])
			}
		}
		for i := 2 * n; i < 4*n; i++ {
			if args[i] != 7 {
				t.Errorf("includeWisps=%v: recursive arg %d = %v, want maxDepth 7", includeWisps, i, args[i])
			}
		}
		if args[len(args)-1] != "root-1" {
			t.Errorf("includeWisps=%v: tail arg = %v, want root-1", includeWisps, args[len(args)-1])
		}
	}
}

// recursiveMembers is one member per (dependency table, target column) pair.
func recursiveMembers(includeWisps bool) int {
	if includeWisps {
		return 6
	}
	return 3
}
