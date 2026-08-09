package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// TestSearchCountsFilterBeforeJoinParity proves that the filter-before-join
// shape of sqlbuild.SearchCountsSQL returns exactly the same rows (ids, order,
// dep/rdep/comment counts, parent, labels, and dependency JSON) as the prior
// filter-after-join shape, across a representative set of filters, orderings,
// and limits. The WHERE clause only references issue columns, so pushing it
// into an inner main-table subquery is result-equivalent (ORDER BY and LIMIT
// stay after the joins); this test is the contract that keeps it so.
func TestSearchCountsFilterBeforeJoinParity(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedSearchCountsFixture(ctx, t, store)

	cases := []struct {
		name   string
		query  string
		filter types.IssueFilter
	}{
		{name: "no filter, default order"},
		{name: "status open", filter: types.IssueFilter{Statuses: []types.Status{types.StatusOpen}}},
		{name: "priority 1", filter: types.IssueFilter{Priority: ptr(1)}},
		{name: "label alpha", filter: types.IssueFilter{Labels: []string{"alpha"}}},
		{name: "substring search", query: "counts-parity"},
		{name: "limit 2 default order", filter: types.IssueFilter{Limit: 2}},
		{name: "limit 3 order created", filter: types.IssueFilter{Limit: 3, SortBy: "created"}},
		{name: "order title", filter: types.IssueFilter{SortBy: "title"}},
		{name: "order status desc", filter: types.IssueFilter{SortBy: "status", SortDesc: true}},
		{name: "skip labels", filter: types.IssueFilter{SkipLabels: true}},
		{name: "limit 1 priority", filter: types.IssueFilter{Limit: 1, SortBy: "priority"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			whereClauses, args, err := sqlbuild.BuildIssueFilterClauses(tc.query, tc.filter, sqlbuild.IssuesFilterTables)
			if err != nil {
				t.Fatalf("BuildIssueFilterClauses: %v", err)
			}
			whereSQL := ""
			if len(whereClauses) > 0 {
				whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
			}
			orderBy := sqlbuild.OrderBy(tc.filter.SortBy, tc.filter.SortDesc, "i")
			limitSQL := ""
			if tc.filter.Limit > 0 {
				limitSQL = fmt.Sprintf("LIMIT %d", tc.filter.Limit)
			}

			// Predicate form (nil ids): the form this PR reshapes. The by-IDs
			// form keeps the plain driver and is unaffected.
			newSQL, genArgs := sqlbuild.SearchCountsSQL(sqlbuild.IssuesFilterTables, nil, whereSQL, orderBy, limitSQL, true, sqlbuild.CountsHydration{SkipLabels: tc.filter.SkipLabels})
			if genArgs != nil {
				t.Fatalf("predicate form must not generate args, got %d", len(genArgs))
			}
			oldSQL := oldSearchCountsSQL(sqlbuild.IssuesFilterTables, whereSQL, orderBy, limitSQL, true, tc.filter.SkipLabels)

			gotNew := runCountsSQL(ctx, t, store.db, newSQL, args)
			gotOld := runCountsSQL(ctx, t, store.db, oldSQL, args)

			if len(gotNew) != len(gotOld) {
				t.Fatalf("row count: new=%d old=%d", len(gotNew), len(gotOld))
			}
			for i := range gotOld {
				if gotNew[i] != gotOld[i] {
					t.Fatalf("row %d differs:\n new=%+v\n old=%+v", i, gotNew[i], gotOld[i])
				}
			}
		})
	}
}

// countsRow is a flattened, comparable projection of one IssueWithCounts row.
type countsRow struct {
	id           string
	depCount     int
	rdepCount    int
	commentCount int
	parent       string
	labels       string
	deps         string
}

func runCountsSQL(ctx context.Context, t *testing.T, db *sql.DB, query string, args []any) []countsRow {
	t.Helper()
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("query: %v\nSQL:\n%s", err, query)
	}
	defer func() { _ = rows.Close() }()

	var out []countsRow
	seen := make(map[string]bool)
	for rows.Next() {
		iwc, err := issueops.ScanReadyWorkRowWithCounts(rows)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		if iwc == nil || iwc.Issue == nil || seen[iwc.Issue.ID] {
			continue
		}
		seen[iwc.Issue.ID] = true
		parent := ""
		if iwc.Parent != nil {
			parent = *iwc.Parent
		}
		deps := make([]string, 0, len(iwc.Issue.Dependencies))
		for _, d := range iwc.Issue.Dependencies {
			deps = append(deps, string(d.Type)+":"+d.DependsOnID)
		}
		out = append(out, countsRow{
			id:           iwc.Issue.ID,
			depCount:     iwc.DependencyCount,
			rdepCount:    iwc.DependentCount,
			commentCount: iwc.CommentCount,
			parent:       parent,
			labels:       strings.Join(iwc.Issue.Labels, ","),
			deps:         strings.Join(deps, "|"),
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}

func seedSearchCountsFixture(ctx context.Context, t *testing.T, store *DoltStore) {
	t.Helper()

	issues := []*types.Issue{
		{ID: "counts-parity-a", Title: "alpha root", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "counts-parity-b", Title: "beta child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "counts-parity-c", Title: "gamma blocker", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeBug},
		{ID: "counts-parity-d", Title: "delta closed", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask},
		{ID: "counts-parity-e", Title: "epsilon leaf", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	if err := store.CloseIssue(ctx, "counts-parity-d", "done", "tester", "s1"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	// Labels: exercises the labels_json aggregate and the DISTINCT-driving filter.
	for _, lb := range []struct{ id, label string }{
		{"counts-parity-a", "alpha"},
		{"counts-parity-a", "shared"},
		{"counts-parity-c", "alpha"},
		{"counts-parity-e", "shared"},
	} {
		if err := store.AddLabel(ctx, lb.id, lb.label, "tester"); err != nil {
			t.Fatalf("AddLabel %s/%s: %v", lb.id, lb.label, err)
		}
	}

	// Dependencies: blocks (dep_count / rdep_count) and parent-child (parent_id).
	deps := []*types.Dependency{
		{IssueID: "counts-parity-b", DependsOnID: "counts-parity-a", Type: types.DepParentChild},
		{IssueID: "counts-parity-b", DependsOnID: "counts-parity-c", Type: types.DepBlocks},
		{IssueID: "counts-parity-e", DependsOnID: "counts-parity-c", Type: types.DepBlocks},
		{IssueID: "counts-parity-a", DependsOnID: "counts-parity-c", Type: types.DepBlocks},
	}
	for _, d := range deps {
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", d.IssueID, d.DependsOnID, err)
		}
	}

	// Comments: exercises the comment_count aggregate.
	for _, c := range []struct{ id, body string }{
		{"counts-parity-a", "first"},
		{"counts-parity-a", "second"},
		{"counts-parity-c", "only"},
	} {
		if err := store.AddComment(ctx, c.id, "tester", c.body); err != nil {
			t.Fatalf("AddComment %s: %v", c.id, err)
		}
	}
}

// oldSearchCountsSQL renders the pre-refactor counts mega-query shape, with the
// WHERE/ORDER BY/LIMIT applied AFTER the aggregate LEFT JOINs. It is kept here,
// in test code only, as the reference shape that the production
// filter-before-join query in sqlbuild.SearchCountsSQL must stay equivalent to.
func oldSearchCountsSQL(tables sqlbuild.FilterTables, whereSQL, orderBySQL, limitSQL string, includeWispReverseDeps, skipLabels bool) string {
	reverseBlockerSelect := `
				SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS dep_id
				FROM dependencies WHERE type = 'blocks'
	`
	if includeWispReverseDeps {
		reverseBlockerSelect += `
				UNION ALL
				SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS dep_id
				FROM wisp_dependencies WHERE type = 'blocks'
		`
	}

	labelsSelect := "l.labels_json AS labels_json"
	labelsJoin := fmt.Sprintf(`
		LEFT JOIN (
			SELECT issue_id, JSON_ARRAYAGG(label) AS labels_json
			FROM %s
			GROUP BY issue_id
		) l ON l.issue_id = i.id`, tables.Labels)
	if skipLabels {
		labelsSelect = "NULL AS labels_json"
		labelsJoin = ""
	}

	return fmt.Sprintf(`
		SELECT %s,
			%s,
			COALESCE(dc.cnt, 0) AS dep_count,
			COALESCE(rc.cnt, 0) AS rdep_count,
			COALESCE(cc.cnt, 0) AS comment_count,
			pc.parent_id     AS parent_id,
			d.deps_json      AS deps_json
		FROM %s i
		%s
		%s
		LEFT JOIN (
			SELECT issue_id, COUNT(*) AS cnt
			FROM %s
			WHERE type = 'blocks'
			GROUP BY issue_id
		) dc ON dc.issue_id = i.id
		LEFT JOIN (
			SELECT dep_id, COUNT(*) AS cnt FROM (
				%s
			) all_blockers GROUP BY dep_id
		) rc ON rc.dep_id = i.id
		LEFT JOIN (
			SELECT issue_id, COUNT(*) AS cnt
			FROM %s
			GROUP BY issue_id
		) cc ON cc.issue_id = i.id
		LEFT JOIN (
			SELECT issue_id,
			       MIN(COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)) AS parent_id
			FROM %s
			WHERE type = 'parent-child'
			GROUP BY issue_id
		) pc ON pc.issue_id = i.id
		LEFT JOIN (
			SELECT issue_id, JSON_ARRAYAGG(%s) AS deps_json
			FROM %s
			GROUP BY issue_id
		) d ON d.issue_id = i.id
		%s
		%s
		%s
	`,
		sqlbuild.ReadyWorkIssueColumns,
		labelsSelect,
		tables.Main,
		sqlbuild.LeaseJoin("i"),
		labelsJoin,
		tables.Dependencies,
		reverseBlockerSelect,
		tables.Comments,
		tables.Dependencies,
		sqlbuild.DepJSONObject,
		tables.Dependencies,
		whereSQL,
		orderBySQL,
		limitSQL,
	)
}
