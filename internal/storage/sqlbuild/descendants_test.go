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

// TestDescendantWalkQueryReproducesDepTargetPrecedence pins the per-column
// IS NULL guards, the one place the reshaped walk's semantics differ textually
// from the COALESCE it replaces, and binds the walk's column set to
// DepTargetExpr.
//
// Neither of the other new tests sees these guards: the shape test above
// asserts on parent_edges / JOIN ( / COALESCE and on member counts, never on a
// guard, and the real-Dolt reference test seeds every edge through exactly one
// target column, so no fixture row has two targets set for the precedence to
// resolve. Deleting nullGuards' arguments therefore leaves both green.
//
// The expected counts are derived, not restated: a member keyed on the column
// at index j must exclude every column ahead of it in DepTargetExpr's
// precedence order, so column j carries len(cols)-1-j guards per dependency
// table — 2 for the first column, 1 for the second, 0 for the last. That
// derivation is also what binds the two lists: if DepTargetExpr gains a target
// column and descendantWalkTargetCols does not, both the guard counts and the
// member count below disagree with the query.
func TestDescendantWalkQueryReproducesDepTargetPrecedence(t *testing.T) {
	t.Parallel()

	cols := depTargetColumns(t)

	for _, includeWisps := range []bool{false, true} {
		q := sqlbuild.DescendantWalkQuery(includeWisps)
		tables := recursiveMembers(includeWisps) / len(cols)
		if tables*len(cols) != recursiveMembers(includeWisps) {
			t.Fatalf("includeWisps=%v: %d members is not one per (table, column) over %d columns %v",
				includeWisps, recursiveMembers(includeWisps), len(cols), cols)
		}

		// One anchor member per (table, column): the anchor is the only place
		// the target column is compared to the bound root id unqualified.
		for _, col := range cols {
			if got := strings.Count(q, "WHERE type = 'parent-child' AND "+col+" = ?"); got != tables {
				t.Errorf("includeWisps=%v: %q anchored on %d of %d dependency tables:\n%s",
					includeWisps, col, got, tables, q)
			}
		}

		// nullGuards emits " AND <prefix><col> IS NULL"; the leading " AND " is
		// what keeps the unqualified count from also matching the "e."-prefixed
		// recursive form.
		for j, col := range cols {
			wantPerTable := len(cols) - 1 - j
			for _, prefix := range []string{"", "e."} {
				guard := " AND " + prefix + col + " IS NULL"
				if got, want := strings.Count(q, guard), wantPerTable*tables; got != want {
					t.Errorf("includeWisps=%v: %q appears %d times, want %d — the members after %s no longer exclude it, so the walk does not reproduce DepTargetExpr's precedence:\n%s",
						includeWisps, guard, got, want, col, q)
				}
			}
		}
	}
}

// depTargetColumns is the column list inside DepTargetExpr, in its precedence
// order. descendantWalkTargetCols is unexported, so the exported expression it
// claims to mirror is what the tests can bind it to.
func depTargetColumns(t *testing.T) []string {
	t.Helper()

	inner, ok := strings.CutPrefix(sqlbuild.DepTargetExpr, "COALESCE(")
	if !ok {
		t.Fatalf("DepTargetExpr is no longer a COALESCE expression: %q", sqlbuild.DepTargetExpr)
	}
	inner, ok = strings.CutSuffix(inner, ")")
	if !ok {
		t.Fatalf("DepTargetExpr is not parenthesised: %q", sqlbuild.DepTargetExpr)
	}
	cols := strings.Split(inner, ", ")
	if len(cols) < 2 {
		t.Fatalf("DepTargetExpr resolves %d column(s), expected the typed target set: %q", len(cols), sqlbuild.DepTargetExpr)
	}
	return cols
}

// recursiveMembers is one member per (dependency table, target column) pair.
func recursiveMembers(includeWisps bool) int {
	if includeWisps {
		return 6
	}
	return 3
}
