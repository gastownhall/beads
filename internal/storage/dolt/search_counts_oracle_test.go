package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// TestSearchIssuesWithCountsMatchesThePredicateFormOracle (AC-3, GH#4370) runs
// a filter × order × limit × hydration matrix through the store-backed counted
// search and compares every answer — membership, order, counts, parent,
// labels, dependencies, and the MaxRows verdict — with an oracle that replays
// the pre-change path: the PREDICATE form of the counts query per plane under
// the old WHERE/ORDER BY/LIMIT, the Go-side id sort and trim, the
// wisp-preferring merge, and the terminal sort, Limit trim and MaxRows cap.
//
// The fixture holds more than sqlbuild.QueryBatchSize matching rows, so the
// unlimited and 201-row cases hydrate across a batch boundary.
func TestSearchIssuesWithCountsMatchesThePredicateFormOracle(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const durable = 210
	issues := make([]*types.Issue, 0, durable)
	for i := 0; i < durable; i++ {
		status := types.StatusOpen
		if i%5 == 0 {
			status = types.StatusInProgress
		}
		issues = append(issues, &types.Issue{
			ID: fmt.Sprintf("test-o%03d", i), Title: fmt.Sprintf("oracle row %d", (i*37)%durable),
			Status: status, Priority: i % 4, IssueType: types.TypeTask,
		})
	}
	if err := store.CreateIssues(ctx, issues, "tester"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}
	for _, w := range []string{"test-ow0", "test-ow1", "test-ow2", "test-ow3"} {
		wisp := &types.Issue{ID: w, Title: "oracle wisp " + w, Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Ephemeral: true}
		if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", w, err)
		}
	}
	for i := 0; i < durable; i += 7 {
		if err := store.AddLabel(ctx, fmt.Sprintf("test-o%03d", i), "alpha", "tester"); err != nil {
			t.Fatalf("AddLabel: %v", err)
		}
	}
	for _, d := range []*types.Dependency{
		{IssueID: "test-o001", DependsOnID: "test-o000", Type: types.DepBlocks},
		{IssueID: "test-o002", DependsOnID: "test-o000", Type: types.DepBlocks},
		{IssueID: "test-o003", DependsOnID: "test-o000", Type: types.DepParentChild},
		{IssueID: "test-o205", DependsOnID: "test-o204", Type: types.DepBlocks},
		{IssueID: "test-o206", DependsOnID: "external:other:cap", Type: types.DepBlocks},
		{IssueID: "test-ow1", DependsOnID: "test-ow0", Type: types.DepBlocks},
		{IssueID: "test-ow2", DependsOnID: "test-o000", Type: types.DepBlocks},
		{IssueID: "test-o007", DependsOnID: "test-ow0", Type: types.DepBlocks},
	} {
		if err := store.AddDependency(ctx, d, "tester"); err != nil {
			t.Fatalf("AddDependency %s->%s: %v", d.IssueID, d.DependsOnID, err)
		}
	}
	for _, id := range []string{"test-o000", "test-o000", "test-o201", "test-ow0"} {
		if err := store.AddComment(ctx, id, "tester", "note"); err != nil {
			t.Fatalf("AddComment %s: %v", id, err)
		}
	}

	type variant struct {
		name  string
		query string
		apply func(*types.IssueFilter)
	}
	filters := []variant{
		{"all", "", func(*types.IssueFilter) {}},
		{"open", "", func(f *types.IssueFilter) { f.Statuses = []types.Status{types.StatusOpen} }},
		{"label", "", func(f *types.IssueFilter) { f.Labels = []string{"alpha"} }},
		{"text", "oracle row 1", func(*types.IssueFilter) {}},
		{"labels-any", "", func(f *types.IssueFilter) { f.LabelsAny = []string{"alpha", "nonexistent"} }},
		{"exclude-label", "", func(f *types.IssueFilter) { f.ExcludeLabels = []string{"alpha"} }},
		{"ids", "", func(f *types.IssueFilter) { f.IDs = []string{"test-o000", "test-o007", "test-o201", "test-ow1"} }},
		{"id-prefix", "", func(f *types.IssueFilter) { f.IDPrefix = "test-o1" }},
		{"priority-min", "", func(f *types.IssueFilter) { f.PriorityMin = ptr(2) }},
		{"not-pinned", "", func(f *types.IssueFilter) { f.Pinned = ptr(false) }},
	}
	orders := []variant{
		{"default", "", func(*types.IssueFilter) {}},
		{"created", "", func(f *types.IssueFilter) { f.SortBy = "created" }},
		{"title", "", func(f *types.IssueFilter) { f.SortBy = "title" }},
		{"priority-desc", "", func(f *types.IssueFilter) { f.SortBy, f.SortDesc = "priority", true }},
		{"id-desc", "", func(f *types.IssueFilter) { f.SortBy, f.SortDesc = "id", true }},
		{"status", "", func(f *types.IssueFilter) { f.SortBy = "status" }},
		{"status-desc", "", func(f *types.IssueFilter) { f.SortBy, f.SortDesc = "status", true }},
	}
	windows := []variant{
		{"unlimited", "", func(*types.IssueFilter) {}},
		{"limit1", "", func(f *types.IssueFilter) { f.Limit = 1 }},
		{"limit201", "", func(f *types.IssueFilter) { f.Limit = 201 }},
		{"capped", "", func(f *types.IssueFilter) { f.MaxRows, f.MaxRowsSource = 50, "test" }},
	}
	hydrations := []variant{
		{"full", "", func(*types.IssueFilter) {}},
		{"skip-labels", "", func(f *types.IssueFilter) { f.SkipLabels = true }},
		{"skip-counts", "", func(f *types.IssueFilter) { f.SkipCounts = true }},
		{"lite", "", func(f *types.IssueFilter) { f.Lite = true }},
	}

	crossedBatch := false
	n := 0
	for _, fv := range filters {
		for _, ov := range orders {
			for _, wv := range windows {
				for _, skipWisps := range []bool{true, false} {
					hv := hydrations[n%len(hydrations)]
					n++
					var filter types.IssueFilter
					fv.apply(&filter)
					ov.apply(&filter)
					wv.apply(&filter)
					hv.apply(&filter)
					filter.SkipWisps = skipWisps
					name := fmt.Sprintf("%s/%s/%s/%s/skipWisps=%v", fv.name, ov.name, wv.name, hv.name, skipWisps)

					want, wantErr := oracleSearchCounts(ctx, t, store.db, fv.query, filter)
					got, gotErr := store.SearchIssuesWithCounts(ctx, fv.query, filter)
					var tooMany *issueops.ErrTooManyRows
					if (wantErr != nil) != (gotErr != nil) || (wantErr != nil && !errors.As(gotErr, &tooMany)) {
						t.Fatalf("%s: error = %v, oracle error = %v", name, gotErr, wantErr)
					}
					if wantErr != nil {
						continue
					}
					if len(want) > sqlbuild.QueryBatchSize {
						crossedBatch = true
					}
					gotRows := make([]countsRow, 0, len(got))
					for _, iwc := range got {
						gotRows = append(gotRows, sortedDeps(toCountsRow(iwc)))
					}
					if len(gotRows) != len(want) {
						t.Fatalf("%s: %d rows, oracle %d", name, len(gotRows), len(want))
					}
					for i := range want {
						if gotRows[i] != want[i] {
							t.Fatalf("%s: row %d differs from the oracle:\n got   =%+v\n oracle=%+v", name, i, gotRows[i], want[i])
						}
					}
				}
			}
		}
	}
	if !crossedBatch {
		t.Fatalf("no case returned more than %d rows; the matrix never crossed a hydration batch", sqlbuild.QueryBatchSize)
	}
}

// oracleSearchCounts replays the pre-GH#4370 SearchIssuesWithCountsInTx for a
// store whose wisp tables exist and hold rows: each plane through the
// predicate-form counts query, then the merge and terminal finish.
func oracleSearchCounts(ctx context.Context, t *testing.T, db *sql.DB, query string, filter types.IssueFilter) ([]countsRow, error) {
	t.Helper()
	hyd := sqlbuild.CountsHydration{SkipLabels: filter.SkipLabels, SkipCounts: filter.SkipCounts, Lite: filter.Lite}
	plane := func(tables sqlbuild.FilterTables) []*types.IssueWithCounts {
		clauses, args, err := sqlbuild.BuildIssueFilterClauses(query, filter, tables)
		if err != nil {
			t.Fatalf("BuildIssueFilterClauses: %v", err)
		}
		whereSQL := ""
		if len(clauses) > 0 {
			whereSQL = "WHERE " + strings.Join(clauses, " AND ")
		}
		goSide := sqlbuild.IsGoSideSort(filter.SortBy)
		eff := issueops.EffectiveSearchLimit(filter.Limit, filter.MaxRows)
		limitSQL := ""
		if eff > 0 && !goSide {
			limitSQL = fmt.Sprintf("LIMIT %d", eff)
		}
		q, _ := sqlbuild.SearchCountsSQL(tables, nil, whereSQL, sqlbuild.OrderBy(filter.SortBy, filter.SortDesc, "i"), limitSQL, true, hyd)
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			t.Fatalf("oracle query: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var out []*types.IssueWithCounts
		seen := map[string]bool{}
		for rows.Next() {
			iwc, err := issueops.ScanReadyWorkRowWithCounts(rows, hyd)
			if err != nil {
				t.Fatalf("oracle scan: %v", err)
			}
			if iwc == nil || iwc.Issue == nil || seen[iwc.Issue.ID] {
				continue
			}
			seen[iwc.Issue.ID] = true
			out = append(out, iwc)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("oracle rows: %v", err)
		}
		if goSide {
			sort.SliceStable(out, func(i, j int) bool { return sqlbuild.LessID(out[i].Issue.ID, out[j].Issue.ID, filter.SortDesc) })
			if eff > 0 && len(out) > eff {
				out = out[:eff]
			}
		}
		return out
	}

	merged := plane(sqlbuild.IssuesFilterTables)
	if !filter.SkipWisps {
		wisps := plane(sqlbuild.WispsFilterTables)
		inWisps := map[string]bool{}
		for _, w := range wisps {
			inWisps[w.Issue.ID] = true
		}
		kept := merged[:0:0]
		for _, iwc := range merged {
			if !inWisps[iwc.Issue.ID] {
				kept = append(kept, iwc)
			}
		}
		merged = append(kept, wisps...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return sqlbuild.Less(merged[i].Issue, merged[j].Issue, filter.SortBy, filter.SortDesc)
	})
	if filter.Limit > 0 && len(merged) > filter.Limit {
		merged = merged[:filter.Limit]
	}
	if err := issueops.EnforceMaxRowsCap(len(merged), filter.MaxRows, filter.MaxRowsSource); err != nil {
		return nil, err
	}
	out := make([]countsRow, 0, len(merged))
	for _, iwc := range merged {
		out = append(out, sortedDeps(toCountsRow(iwc)))
	}
	return out, nil
}

// toCountsRow flattens one IssueWithCounts the way runCountsSQL does, with
// labels in sorted order (sortedDeps orders the dependencies).
func toCountsRow(iwc *types.IssueWithCounts) countsRow {
	labels := append([]string(nil), iwc.Issue.Labels...)
	sort.Strings(labels)
	parent := ""
	if iwc.Parent != nil {
		parent = *iwc.Parent
	}
	deps := make([]string, 0, len(iwc.Issue.Dependencies))
	for _, d := range iwc.Issue.Dependencies {
		deps = append(deps, string(d.Type)+":"+d.DependsOnID)
	}
	return countsRow{
		id:           iwc.Issue.ID,
		depCount:     iwc.DependencyCount,
		rdepCount:    iwc.DependentCount,
		commentCount: iwc.CommentCount,
		parent:       parent,
		labels:       strings.Join(labels, ","),
		deps:         strings.Join(deps, "|"),
	}
}
