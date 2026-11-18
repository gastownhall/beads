package dolt

import (
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// TestSearchCountsByIDsPlanNeverScansADependencyTable (AC-6, GH#4370) pins the
// reverse-blocker (rdep_count) subquery of the by-IDs counts form on Dolt.
//
// Semantics: the by-IDs form splits the COALESCE(depends_on_issue_id,
// depends_on_wisp_id, depends_on_external) target into three disjoint UNION ALL
// legs per dependency table. Over a graph whose blocks edges reach issue, wisp
// and external targets from both dependencies and wisp_dependencies, every
// by-IDs row (rdep_count included) must equal the predicate form's row, whose
// rc join still keys on the COALESCE.
//
// Plan: each leg filters one target column with IN (…), which the planner can
// seek through the target indexes of migrations 0041/0043 (dependencies) and
// 0021/0047 (wisp_dependencies). The old by-IDs rc filtered the COALESCE
// expression, which no index covers, so it read the whole dependency table on
// every bounded page. The EXPLAIN assertion requires an IndexedTableAccess on
// every target column of every dependency table the statement reads.
func TestSearchCountsByIDsPlanNeverScansADependencyTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issues := []*types.Issue{
		{ID: "rb-a", Title: "issue blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "rb-b", Title: "issue blocked twice", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "rb-c", Title: "issue without edges", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "rb-w1", Title: "wisp blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		{ID: "rb-w2", Title: "wisp blocked", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	}
	for _, iss := range issues {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}
	// blocks edges reaching every target column from both dependency tables,
	// plus a parent-child edge the reverse-blocker legs must not count.
	for _, d := range []*types.Dependency{
		{IssueID: "rb-b", DependsOnID: "rb-a", Type: types.DepBlocks},                // dependencies → issue
		{IssueID: "rb-b", DependsOnID: "rb-w1", Type: types.DepBlocks},               // dependencies → wisp
		{IssueID: "rb-c", DependsOnID: "external:other:cap", Type: types.DepBlocks},  // dependencies → external
		{IssueID: "rb-w2", DependsOnID: "rb-a", Type: types.DepBlocks},               // wisp_dependencies → issue
		{IssueID: "rb-w2", DependsOnID: "rb-w1", Type: types.DepBlocks},              // wisp_dependencies → wisp
		{IssueID: "rb-w2", DependsOnID: "external:other:cap", Type: types.DepBlocks}, // wisp_dependencies → external
		{IssueID: "rb-c", DependsOnID: "rb-a", Type: types.DepParentChild},           // not a blocker
	} {
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", d.IssueID, d.DependsOnID, err)
		}
	}

	hyd := sqlbuild.CountsHydration{}
	for _, fam := range []struct {
		name   string
		tables sqlbuild.FilterTables
		ids    []string
		rdeps  map[string]int
	}{
		{"issues", sqlbuild.IssuesFilterTables, []string{"rb-a", "rb-b", "rb-c"}, map[string]int{"rb-a": 2, "rb-b": 0, "rb-c": 0}},
		{"wisps", sqlbuild.WispsFilterTables, []string{"rb-w1", "rb-w2"}, map[string]int{"rb-w1": 2, "rb-w2": 0}},
	} {
		t.Run(fam.name, func(t *testing.T) {
			byIDsSQL, args := sqlbuild.SearchCountsSQL(fam.tables, fam.ids, "", "", "", true, hyd)
			predSQL, _ := sqlbuild.SearchCountsSQL(fam.tables, nil, "", "", "", true, hyd)

			want := make(map[string]countsRow)
			for _, r := range runCountsSQL(ctx, t, store.db, predSQL, nil, hyd) {
				want[r.id] = sortedDeps(r)
			}
			got := runCountsSQL(ctx, t, store.db, byIDsSQL, args, hyd)
			for i := range got {
				got[i] = sortedDeps(got[i])
			}
			if len(got) != len(fam.ids) {
				t.Fatalf("by-IDs form returned %d rows, want %d", len(got), len(fam.ids))
			}
			for _, r := range got {
				if r != want[r.id] {
					t.Fatalf("by-IDs row differs from the COALESCE predicate form:\n by-IDs=%+v\n pred  =%+v", r, want[r.id])
				}
				if r.rdepCount != fam.rdeps[r.id] {
					t.Fatalf("%s rdep_count = %d, want %d", r.id, r.rdepCount, fam.rdeps[r.id])
				}
			}

			lits := make([]string, len(args))
			for i, a := range args {
				lits[i] = "'" + a.(string) + "'"
			}
			plan := explainPlan(t, ctx, store.db, literalizeParams(byIDsSQL, lits...))
			if !looksLikeDoltPlan(plan) {
				t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping index assertion; plan=\n%s", plan)
			}
			for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
				for _, col := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
					if !indexedAccessOn(plan, depTable, col) {
						t.Errorf("no IndexedTableAccess on %s.%s: the reverse-blocker leg for that target scans the table.\nplan:\n%s", depTable, col, plan)
					}
				}
			}
		})
	}
}

// sortedDeps orders a row's dependency list: JSON_ARRAYAGG carries no ORDER
// BY, so two statements may emit the same dependency set in different orders.
func sortedDeps(r countsRow) countsRow {
	deps := strings.Split(r.deps, "|")
	sort.Strings(deps)
	r.deps = strings.Join(deps, "|")
	return r
}

// indexedAccessOn reports whether plan holds an IndexedTableAccess on table
// whose index column list includes col. Dolt prints an index by its column
// list, e.g. "index: [dependencies.type,dependencies.depends_on_issue_id]".
func indexedAccessOn(plan, table, col string) bool {
	lines := strings.Split(plan, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "IndexedTableAccess("+table+")") {
			continue
		}
		for _, next := range lines[i+1:] {
			if strings.Contains(next, "TableAccess(") {
				break
			}
			if strings.Contains(next, "index: [") && strings.Contains(next, table+"."+col) {
				return true
			}
		}
	}
	return false
}
