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

// TestSearchIssuesWithCountsMatchesPredicateOracle is the GH#4370 AC-3 pin on
// the direct store seam: after the select-IDs-then-hydrate rewrite, the
// optimized counts search must answer the same filter/order/hydration matrix
// as the predicate-form mega-query did — same ordered page, same relational
// content (labels, dep/rdep/comment counts, parent, dependency collection).
// The predicate form is kept in sqlbuild for exactly this comparison; the
// oracle below is the pre-change pipeline (BuildIssueFilterClauses →
// SearchCountsSQL(predicate) → scan → Go-side sort-before-bound → terminal
// sort/trim/cap), reconstructed beside the changed path so a divergence fails
// rather than silently redefining the contract.
func TestSearchIssuesWithCountsMatchesPredicateOracle(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedSearchCountsFixture(ctx, t, store)

	cases := []struct {
		name   string
		query  string
		filter types.IssueFilter
		// capErr: the MaxRows overage must be refused identically on both
		// paths — an ErrTooManyRows on each side, and no rows compared.
		capErr bool
	}{
		{name: "no filter, default order"},
		{name: "status open", filter: types.IssueFilter{Statuses: []types.Status{types.StatusOpen}}},
		{name: "priority 1", filter: types.IssueFilter{Priority: ptr(1)}},
		{name: "priority min 2", filter: types.IssueFilter{PriorityMin: ptr(2)}},
		{name: "label alpha (AND-set)", filter: types.IssueFilter{Labels: []string{"alpha"}}},
		{name: "labels any", filter: types.IssueFilter{LabelsAny: []string{"alpha", "nonexistent"}}},
		{name: "exclude label", filter: types.IssueFilter{ExcludeLabels: []string{"alpha"}}},
		{name: "substring query", query: "counts-parity"},
		{name: "ids", filter: types.IssueFilter{IDs: []string{"counts-parity-a", "counts-parity-c", "counts-parity-d"}}},
		{name: "id prefix", filter: types.IssueFilter{IDPrefix: "counts-parity-"}},
		{name: "limit 1 default order", filter: types.IssueFilter{Limit: 1}},
		{name: "limit 3 order created", filter: types.IssueFilter{Limit: 3, SortBy: "created"}},
		{name: "limit 2 order created desc default (newest first)", filter: types.IssueFilter{Limit: 2, SortBy: "created", SortDesc: true}},
		{name: "order status asc with ties", filter: types.IssueFilter{SortBy: "status"}},
		{name: "order status desc with ties", filter: types.IssueFilter{SortBy: "status", SortDesc: true}},
		{name: "order title", filter: types.IssueFilter{SortBy: "title"}},
		{name: "order updated", filter: types.IssueFilter{SortBy: "updated"}},
		{name: "sort id (Go side) with limit", filter: types.IssueFilter{SortBy: "id", Limit: 2}},
		{name: "sort id (Go side) desc unl", filter: types.IssueFilter{SortBy: "id", SortDesc: true}},
		{name: "unbounded limit 0", filter: types.IssueFilter{}},
		{name: "limit 201 over a 5-row corpus", filter: types.IssueFilter{Limit: 201}},
		{name: "skip labels", filter: types.IssueFilter{SkipLabels: true}},
		{name: "skip counts", filter: types.IssueFilter{SkipCounts: true}},
		{name: "lite", filter: types.IssueFilter{Lite: true}},
		{name: "skip labels + lite + limit 2 created", filter: types.IssueFilter{SkipLabels: true, Lite: true, Limit: 2, SortBy: "created"}},
		{name: "pinned false filter", filter: types.IssueFilter{Pinned: ptr(false)}},
		{name: "max rows cap overage is refused", filter: types.IssueFilter{MaxRows: 3, MaxRowsSource: "ac-3 oracle"}, capErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			filter := tc.filter
			filter.SkipWisps = true // one plane: the fixture is issues-only

			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			defer func() { _ = tx.Rollback() }()

			got, err := issueops.SearchIssuesWithCountsInTx(ctx, tx, tc.query, filter)
			var tooMany *issueops.ErrTooManyRows
			if tc.capErr {
				if err == nil {
					t.Fatalf("expected an ErrTooManyRows overage, got %d rows", len(got))
				}
				if !errors.As(err, &tooMany) {
					t.Fatalf("expected an ErrTooManyRows overage, got %v", err)
				}
				if tooMany.Found != 4 || tooMany.Cap != 3 {
					t.Fatalf("cap accounting = found %d cap %d, want found 4 cap 3", tooMany.Found, tooMany.Cap)
				}
			} else if err != nil {
				t.Fatalf("SearchIssuesWithCountsInTx: %v", err)
			}

			want, oracleErr := predicateOracleCounts(ctx, t, tx, tc.query, filter)
			if tc.capErr {
				var oracleTooMany *issueops.ErrTooManyRows
				if oracleErr == nil {
					t.Fatalf("seam refused the overage but the oracle did not")
				}
				if !errors.As(oracleErr, &oracleTooMany) {
					t.Fatalf("oracle returned %v, want ErrTooManyRows", oracleErr)
				}
				if oracleTooMany.Found != tooMany.Found || oracleTooMany.Cap != tooMany.Cap {
					t.Fatalf("oracle cap accounting = found %d cap %d, want found %d cap %d",
						oracleTooMany.Found, oracleTooMany.Cap, tooMany.Found, tooMany.Cap)
				}
				return
			}
			if oracleErr != nil {
				t.Fatalf("oracle: %v", oracleErr)
			}
			gotRows := flattenIWC(got)
			wantRows := flattenIWC(want)
			if len(gotRows) != len(wantRows) {
				t.Fatalf("page has %d rows, oracle %d:\n got=%+v\n oracle=%+v", len(gotRows), len(wantRows), gotRows, wantRows)
			}
			for i := range wantRows {
				if gotRows[i] != wantRows[i] {
					t.Fatalf("row %d differs:\n got=%+v\n oracle=%+v", i, gotRows[i], wantRows[i])
				}
			}
		})
	}
}

// predicateOracleCounts IS the pre-GH#4370 pipeline, reconstructed as the
// parity oracle: the predicate-form counts mega-query with the caller's WHERE,
// ORDER BY and parameter-free LIMIT, scanned and deduped exactly the way the
// old runFilterSearchQueryInTx (issueops/search_counts.go) did, then the same
// Go-side sort-before-bound for a Go-side sort key, then the unchanged terminal
// (merge sort, Limit trim, MaxRows cap). It is deliberately a copy of retired
// code, not a call into the new seam — its job is to fail when the new path
// answers a different page.
func predicateOracleCounts(ctx context.Context, t *testing.T, tx *sql.Tx, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	t.Helper()

	hyd := sqlbuild.CountsHydration{SkipLabels: filter.SkipLabels, SkipCounts: filter.SkipCounts, Lite: filter.Lite}

	whereClauses, args, err := sqlbuild.BuildIssueFilterClauses(query, filter, sqlbuild.IssuesFilterTables)
	if err != nil {
		t.Fatalf("oracle BuildIssueFilterClauses: %v", err)
	}
	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}
	goSideSort := sqlbuild.IsGoSideSort(filter.SortBy)
	eff := issueops.EffectiveSearchLimit(filter.Limit, filter.MaxRows)
	limitSQL := ""
	if eff > 0 && !goSideSort {
		limitSQL = fmt.Sprintf("LIMIT %d", eff)
	}
	orderBy := sqlbuild.OrderBy(filter.SortBy, filter.SortDesc, "i")
	predSQL, _ := sqlbuild.SearchCountsSQL(sqlbuild.IssuesFilterTables, nil, whereSQL, orderBy, limitSQL, true, hyd)

	rows, err := tx.QueryContext(ctx, predSQL, args...)
	if err != nil {
		t.Fatalf("oracle query: %v\nSQL:\n%s", err, predSQL)
	}
	defer func() { _ = rows.Close() }()

	var out []*types.IssueWithCounts
	seen := map[string]bool{}
	for rows.Next() {
		iwc, scanErr := issueops.ScanReadyWorkRowWithCounts(rows, hyd)
		if scanErr != nil {
			t.Fatalf("oracle scan: %v", scanErr)
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

	if goSideSort {
		sort.SliceStable(out, func(i, j int) bool {
			return sqlbuild.LessID(out[i].Issue.ID, out[j].Issue.ID, filter.SortDesc)
		})
		if eff > 0 && len(out) > eff {
			out = out[:eff]
		}
	}

	// The unchanged terminal: sort the page the way finishSearchIssuesWithCounts
	// does, trim to Limit, then enforce the MaxRows cap on the delivered count.
	sort.SliceStable(out, func(i, j int) bool {
		return sqlbuild.Less(out[i].Issue, out[j].Issue, filter.SortBy, filter.SortDesc)
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	if err := issueops.EnforceMaxRowsCap(len(out), filter.MaxRows, filter.MaxRowsSource); err != nil {
		return nil, err
	}
	return out, nil
}

// flattenIWC renders hydrated rows comparably: id order is the delivered page
// order; the dependency collection is canonicalized (unordered in the engine)
// and labels are sorted the way ScanReadyWorkRowWithCounts sorts them.
func flattenIWC(items []*types.IssueWithCounts) []countsRowLike {
	out := make([]countsRowLike, 0, len(items))
	for _, iwc := range items {
		iss := iwc.Issue
		deps := make([]string, 0, len(iss.Dependencies))
		for _, d := range iss.Dependencies {
			deps = append(deps, string(d.Type)+":"+d.DependsOnID)
		}
		sort.Strings(deps)
		parent := ""
		if iwc.Parent != nil {
			parent = *iwc.Parent
		}
		labels := append([]string(nil), iss.Labels...)
		sort.Strings(labels)
		out = append(out, countsRowLike{
			id:     iss.ID,
			dep:    iwc.DependencyCount,
			rdep:   iwc.DependentCount,
			cmt:    iwc.CommentCount,
			parent: parent,
			labels: strings.Join(labels, ","),
			deps:   strings.Join(deps, "|"),
			lite:   iss.IsLitePartial,
		})
	}
	return out
}

type countsRowLike struct {
	id     string
	dep    int
	rdep   int
	cmt    int
	parent string
	labels string
	deps   string
	lite   bool
}
