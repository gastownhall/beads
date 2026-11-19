package dolt

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// TestSearchCountsByIDsPlanNeverScansADependencyTable is the GH#4370 AC-6 pin
// for the by-IDs counts form's reverse-blocker: with representative populated
// dependency tables and a small ID batch, EXPLAIN (on Dolt server; an
// unrecognized plan format skips rather than guesses) must show the target
// legs seeking an index (IndexedTableAccess on a depends_on_* target), not
// scanning the dependency tables. The pre-fix shape constrained the blocker
// input with `COALESCE(...) IN (...)` — an expression no supported planner can
// seek, so every by-IDs hydration leg paid a full dependency-table scan per
// subquery block; the disjoint UNION ALL legs each constrain a real column.
//
// The second half is the semantics claim the rewrite makes: counts produced by
// the legs equal COALESCE semantics. It runs the full by-IDs mega-query against
// a seeded corpus with issue-, wisp- and external-target blockers (plus
// NULL-target and non-blocks rows) and compares the hydrated counts to a
// COALESCE reference over the same ids, and the whole hydrated row to the
// predicate-form mega-query restricted to the same ids.
func TestSearchCountsByIDsPlanNeverScansADependencyTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedReverseBlockerFixture(ctx, t, store)

	ids := []string{"bd-scbx-a", "bd-scbx-b", "bd-scbx-c", "bd-scbx-d"}
	hyd := sqlbuild.CountsHydration{}
	countsSQL, args := sqlbuild.SearchCountsSQL(sqlbuild.IssuesFilterTables, ids, "", "", "", true, hyd)

	// EXPLAIN the by-IDs form with every placeholder bound to a real page id,
	// in left-to-right injection order: full hydration binds the ids twelve
	// times with wisp dependencies included.
	lits := make([]string, 0, len(ids)*12)
	for i := 0; i < 12; i++ {
		for _, id := range ids {
			lits = append(lits, "'"+id+"'")
		}
	}
	plan := explainPlan(t, ctx, store.db, literalizeParams(countsSQL, lits...))
	if !looksLikeDoltPlan(plan) {
		t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping sargability assertion; plan=\n%s", plan)
	}
	if !strings.Contains(plan, "IndexedTableAccess") || !strings.Contains(plan, "depends_on_issue_id") {
		t.Fatalf("the by-IDs reverse-blocker legs do not seek a depends_on_* target index (want IndexedTableAccess on [dependencies.depends_on_issue_id]) — the legs regressed to full dependency-table scans.\nplan:\n%s", plan)
	}

	// Counts must equal COALESCE semantics: compare the by-IDs mega-query's
	// hydrated rows against the predicate form over the same ids, and its
	// reverse counts against a direct COALESCE reference.
	gotByIDs := canonicalCountsRows(runCountsSQL(ctx, t, store.db, countsSQL, args, hyd))

	whereSQL := "WHERE id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
	predSQL, _ := sqlbuild.SearchCountsSQL(sqlbuild.IssuesFilterTables, nil, whereSQL, "", "", true, hyd)
	want := canonicalCountsRows(runCountsSQL(ctx, t, store.db, predSQL, toAnys(ids), hyd))
	if len(gotByIDs) != len(want) {
		t.Fatalf("by-IDs form returned %d rows, predicate form %d", len(gotByIDs), len(want))
	}
	for i := range want {
		if gotByIDs[i] != want[i] {
			t.Fatalf("row %d differs:\n by-IDs=%+v\n predicate=%+v", i, gotByIDs[i], want[i])
		}
	}

	refCounts := coalesceReferenceReverseCounts(ctx, t, store, ids)
	for _, row := range gotByIDs {
		if row.rdepCount != refCounts[row.id] {
			t.Fatalf("reverse count for %s = %d, want COALESCE semantics value %d", row.id, row.rdepCount, refCounts[row.id])
		}
	}
}

// seedReverseBlockerFixture writes a corpus whose blocker edges resolve through
// all three target columns, so every leg of the by-IDs reverse-blocker has a
// row to find: issue-target rows toward a/c, wisp-target and external-target
// rows toward b/d, a row whose targets are all NULL (which must count for
// nothing anywhere), and non-blocks rows the type guard must exclude.
func seedReverseBlockerFixture(ctx context.Context, t *testing.T, store *DoltStore) {
	t.Helper()

	for _, id := range []string{"bd-scbx-a", "bd-scbx-b", "bd-scbx-c", "bd-scbx-d", "bd-scbx-outer1", "bd-scbx-outer2", "bd-scbx-outer3", "bd-scbx-outer4", "bd-scbx-outer5"} {
		if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: "scbx " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	// Wisp-plane sources for wisp_dependencies rows: its issue_id FK points at
	// the wisps table, so the rows must originate as real wisps.
	for _, id := range []string{"bd-scbx-w1", "bd-scbx-w2", "bd-scbx-w3"} {
		if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: "scbx wisp " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}, "tester"); err != nil {
			t.Fatalf("CreateIssue wisp %s: %v", id, err)
		}
	}

	ins := func(table string, args ...any) {
		t.Helper()
		var cols, vals string
		switch len(args) {
		case 2: // issue-target
			cols, vals = "(id, issue_id, depends_on_issue_id, type, created_by)", "(UUID(), ?, ?, 'blocks', 'tester')"
		case 3: // wisp-target, computed
			cols, vals = "(id, issue_id, depends_on_wisp_id, type, created_by)", "(UUID(), ?, ?, 'blocks', 'tester')"
		default:
			t.Fatalf("unsupported insert arity %d", len(args))
		}
		if _, err := store.db.ExecContext(ctx, "INSERT INTO "+table+" "+cols+" VALUES "+vals, args...); err != nil {
			t.Fatalf("insert into %s: %v", table, err)
		}
	}

	// Issue-target blockers (plane-rooted and outer).
	ins("dependencies", "bd-scbx-outer1", "bd-scbx-a")
	ins("dependencies", "bd-scbx-outer2", "bd-scbx-a")
	ins("dependencies", "bd-scbx-outer3", "bd-scbx-c")
	// Wisp-target blockers (issue target NULL): resolve via the second leg.
	ins("dependencies", "bd-scbx-outer4", "bd-scbx-b")
	// External-target blockers (both prior targets NULL): resolve via the
	// third leg. The page ids are legitimate strings, so the external column
	// can carry them the way a cross-prefix external dep would.
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO dependencies (id, issue_id, depends_on_external, type, created_by) VALUES (UUID(), ?, ?, 'blocks', 'tester')",
		"bd-scbx-outer5", "bd-scbx-d"); err != nil {
		t.Fatalf("insert external-target blocker: %v", err)
	}
	// (A row with every target NULL cannot exist: ck_dep_one_target forbids
	// it. The legs do NOT lean on that CHECK — their guards come from the
	// COALESCE precedence itself — which is what keeps them result-identical
	// on this schema and on any future one that relaxes the constraint.)
	// Non-blocks rows must be excluded by the type guard in both forms.
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (UUID(), ?, ?, 'parent-child', 'tester')",
		"bd-scbx-outer1", "bd-scbx-b"); err != nil {
		t.Fatalf("insert parent-child row: %v", err)
	}
	// And the same three target shapes on the wisp plane's dependency table.
	ins("wisp_dependencies", "bd-scbx-w1", "bd-scbx-b")
	ins("wisp_dependencies", "bd-scbx-w2", "bd-scbx-c")
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_external, type, created_by) VALUES (UUID(), ?, ?, 'blocks', 'tester')",
		"bd-scbx-w3", "bd-scbx-d"); err != nil {
		t.Fatalf("insert wisp-plane external-target blocker: %v", err)
	}
}

// coalesceReferenceReverseCounts computes the expected reverse counts straight
// from the pre-fix COALESCE semantics: one grouped query per dependency table
// over `type='blocks' AND COALESCE(...) IN (ids)`, summed. It is the oracle the
// disjoint legs claim to equal.
func coalesceReferenceReverseCounts(ctx context.Context, t *testing.T, store *DoltStore, ids []string) map[string]int {
	t.Helper()
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	out := map[string]int{}
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		q := "SELECT " + sqlbuild.DepTargetExpr + " AS dep_id, COUNT(*) FROM " + table +
			" WHERE type = 'blocks' AND " + sqlbuild.DepTargetExpr + " IN (" + ph + ") GROUP BY dep_id"
		rows, err := store.db.QueryContext(ctx, q, toAnys(ids)...)
		if err != nil {
			t.Fatalf("COALESCE reference query: %v", err)
		}
		for rows.Next() {
			var depID string
			var n int
			if err := rows.Scan(&depID, &n); err != nil {
				t.Fatalf("scan COALESCE reference: %v", err)
			}
			out[depID] += n
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("COALESCE reference rows: %v", err)
		}
	}
	// Ids with no blockers at all must exist in the map as zero so the caller
	// can compare directly.
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			out[id] = 0
		}
	}
	return out
}

// canonicalCountsRows sorts a countsRow slice by id and canonicalizes the
// unordered dependency collection within each row, so two scans that agree on
// content but not on engine emission order compare equal.
func canonicalCountsRows(rows []countsRow) []countsRow {
	out := make([]countsRow, len(rows))
	copy(out, rows)
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	for i := range out {
		if out[i].deps != "" {
			parts := strings.Split(out[i].deps, "|")
			sort.Strings(parts)
			out[i].deps = strings.Join(parts, "|")
		}
	}
	return out
}

func toAnys(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}
