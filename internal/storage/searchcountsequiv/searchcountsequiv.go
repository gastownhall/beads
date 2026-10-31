// Package searchcountsequiv checks that the store-backed counts search,
// issueops.SearchIssuesWithCountsInTx, returns exactly what the predicate form
// of sqlbuild.SearchCountsSQL returns for the same request (#7635). A bounded
// page is answered ids-first — an id scan, then the by-IDs form in batches —
// and the predicate form is the oracle it must reproduce row for row: the same
// rows in the same order, every column, labels and dependencies compared as
// sets, the same issues+wisps merge and the same MaxRows refusal.
//
// It also checks the by-IDs form on its own against the predicate form over
// every id, because `bd ready`'s counts hydration and the domain/db union path
// hydrate through it too. The fixture plants dependency rows with TWO targets,
// which the one-target CHECKs forbid but a store can still hold (a drifted
// schema merge or hand-repair SQL; the id backfill leaves such rows alone), so
// the by-IDs reverse-blocker match has to be exact for those rows as well.
//
// Each backend's test package runs Run against its own engine; the fixture,
// the cases and the oracle live here once.
package searchcountsequiv

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// Open returns a *sql.DB on a fresh, migrated database. Run calls it once per
// scenario.
type Open func(t *testing.T) *sql.DB

// Run seeds each scenario through a fresh database from open and compares
// the counts search with the predicate-form oracle for every case.
func Run(t *testing.T, open Open) {
	t.Helper()
	t.Run("full", func(t *testing.T) {
		db := open(t)
		seed(t, db, true)
		plantTwoTargetEdges(t, db)
		checkByIDsForm(t, db)
		checkCases(t, db, cases(), true)
	})
	// No wisps at all: every read is issues-only, ids-first or not.
	t.Run("no wisps", func(t *testing.T) {
		db := open(t)
		seed(t, db, false)
		checkCases(t, db, cases(), false)
	})
	// A store without wisp_dependencies: the wisps leg is skipped and the
	// issues leg's reverse blockers come from dependencies alone.
	t.Run("no wisp_dependencies", func(t *testing.T) {
		db := open(t)
		seed(t, db, true)
		exec(t, db, "DROP TABLE wisp_dependencies")
		checkByIDsForm(t, db)
		checkCases(t, db, cases(), false)
	})
	// Pages at and around the ids-first ceiling, over more matches than it
	// in both tables: every row of a 199-, 200- and 201-row page, in order.
	t.Run("pages at the ceiling", func(t *testing.T) {
		db := open(t)
		seedLarge(t, db, largeRows)
		checkCases(t, db, largeCases(), true)
	})
}

// largeRows is the per-table row count of the ceiling scenario; with one row
// in ten carrying another label instead, 207 rows per table carry "bulk".
const largeRows = 230

func largeCases() []Case {
	bulk := []string{"bulk"}
	return []Case{
		{Name: "label created 199", Filter: types.IssueFilter{Labels: bulk, SortBy: "created", Limit: 199}, WantRows: 199},
		{Name: "label created 200", Filter: types.IssueFilter{Labels: bulk, SortBy: "created", Limit: 200}, WantRows: 200},
		{Name: "label created 201", Filter: types.IssueFilter{Labels: bulk, SortBy: "created", Limit: 201}, WantRows: 201},
		{Name: "label created asc 200", Filter: types.IssueFilter{Labels: bulk, SortBy: "created", SortDesc: true, Limit: 200}, WantRows: 200},
		{Name: "label default order 200", Filter: types.IssueFilter{Labels: bulk, Limit: 200}, WantRows: 200},
		{Name: "label title 200", Filter: types.IssueFilter{Labels: bulk, SortBy: "title", Limit: 200}, WantRows: 200},
		{Name: "labelsAny created 200", Filter: types.IssueFilter{LabelsAny: []string{"bulk", "other"}, SortBy: "created", Limit: 200}, WantRows: 200},
		{Name: "no label created 200", Filter: types.IssueFilter{SortBy: "created", Limit: 200}, WantRows: 200},
		// One table only: the merge cannot refill a row a leg lost.
		{Name: "label created skip wisps 199", Filter: types.IssueFilter{Labels: bulk, SortBy: "created", Limit: 199, SkipWisps: true}, WantRows: 199},
		{Name: "label created skip wisps 200", Filter: types.IssueFilter{Labels: bulk, SortBy: "created", Limit: 200, SkipWisps: true}, WantRows: 200},
		{Name: "label created skip wisps 201", Filter: types.IssueFilter{Labels: bulk, SortBy: "created", Limit: 201, SkipWisps: true}, WantRows: 201},
		{Name: "label created ephemeral 200", Filter: types.IssueFilter{Ephemeral: ptr(true), Labels: bulk, SortBy: "created", Limit: 200}, WantRows: 200},
	}
}

// Case is one search request. NoMatch marks a case the full fixture answers
// with no rows on purpose; WantRows, when set, is the exact number of rows
// the reference must deliver, so a page that came back short fails on the
// premise rather than passing as two equal short answers.
type Case struct {
	Name     string
	Query    string
	Filter   types.IssueFilter
	NoMatch  bool
	WantRows int
}

func cases() []Case {
	hot := []string{"hot"}
	return []Case{
		{Name: "label page created", Filter: types.IssueFilter{Labels: hot, SortBy: "created", Limit: 5}},
		{Name: "label page created asc", Filter: types.IssueFilter{Labels: hot, SortBy: "created", SortDesc: true, Limit: 4}},
		{Name: "label page default order", Filter: types.IssueFilter{Labels: hot, Limit: 3}},
		{Name: "no label page", Filter: types.IssueFilter{Limit: 6}},
		{Name: "no label page updated", Filter: types.IssueFilter{SortBy: "updated", Limit: 7}},
		{Name: "labels AND", Filter: types.IssueFilter{Labels: []string{"hot", "warm"}, Limit: 10}},
		{Name: "labelsAny", Filter: types.IssueFilter{LabelsAny: []string{"cold", "warm"}, SortBy: "priority", Limit: 6}},
		{Name: "labelsAny and label", Filter: types.IssueFilter{Labels: hot, LabelsAny: []string{"cold", "warm"}, SortBy: "created", Limit: 4}},
		{Name: "label NOT", Filter: types.IssueFilter{Labels: hot, ExcludeLabels: []string{"cold"}, SortBy: "created", Limit: 5}},
		{Name: "sort title", Filter: types.IssueFilter{Labels: hot, SortBy: "title", Limit: 4}},
		{Name: "sort title desc", Filter: types.IssueFilter{SortBy: "title", SortDesc: true, Limit: 5}},
		{Name: "sort closed", Filter: types.IssueFilter{Labels: hot, SortBy: "closed", Limit: 6}},
		{Name: "sort assignee desc", Filter: types.IssueFilter{SortBy: "assignee", SortDesc: true, Limit: 5}},
		{Name: "sort status", Filter: types.IssueFilter{LabelsAny: []string{"hot", "cold"}, SortBy: "status", Limit: 5}},
		{Name: "go-side sort id", Filter: types.IssueFilter{Labels: hot, SortBy: "id", Limit: 4}},
		{Name: "go-side sort id desc", Filter: types.IssueFilter{SortBy: "id", SortDesc: true, Limit: 3}},
		// The store reader's page shapes: an offset widens the bound
		// (workapi.WithRowsBeforeThePage) and the probe row adds one
		// (workapi.WithFetchOneExtra), so the bound can sit at, past or far
		// past the match count.
		{Name: "probe row at the end", Filter: types.IssueFilter{Labels: hot, SortBy: "created", Limit: 13}},
		{Name: "bound past the matches", Filter: types.IssueFilter{Labels: hot, SortBy: "created", Limit: 50}},
		{Name: "offset page", Filter: types.IssueFilter{Labels: hot, SortBy: "created", Limit: 2 + 3 + 1}},
		{Name: "bound at the ids-first ceiling", Filter: types.IssueFilter{SortBy: "created", Limit: 200}},
		{Name: "bound above the ids-first ceiling", Filter: types.IssueFilter{SortBy: "created", Limit: 201}},
		{Name: "unbounded label", Filter: types.IssueFilter{Labels: hot, SortBy: "created"}},
		{Name: "unbounded", Filter: types.IssueFilter{}},
		// MaxRows: with no Limit the breaker pushes MaxRows+1, which is a
		// bound like any other; the cap fires on the delivered rows.
		{Name: "max rows trips", Filter: types.IssueFilter{Labels: hot, SortBy: "created", MaxRows: 3, MaxRowsSource: "--max-rows"}},
		{Name: "max rows holds", Filter: types.IssueFilter{Labels: hot, SortBy: "created", MaxRows: 40}},
		{Name: "limit under max rows", Filter: types.IssueFilter{Labels: hot, SortBy: "created", Limit: 2, MaxRows: 5}},
		{Name: "limit over max rows", Filter: types.IssueFilter{Labels: hot, SortBy: "created", Limit: 9, MaxRows: 4}},
		{Name: "max rows above the ids-first ceiling", Filter: types.IssueFilter{Labels: hot, SortBy: "created", MaxRows: 5000}},
		{Name: "ephemeral", Filter: types.IssueFilter{Ephemeral: ptr(true), Labels: hot, SortBy: "created", Limit: 3}},
		{Name: "ephemeral unbounded", Filter: types.IssueFilter{Ephemeral: ptr(true), Labels: hot}},
		// Only an issues-table row carries this label, so the wisps leg finds
		// nothing and the read falls through to the issues table.
		{Name: "ephemeral falls through", Filter: types.IssueFilter{Ephemeral: ptr(true), Labels: []string{"eph-only"}, Limit: 3}},
		{Name: "skip wisps", Filter: types.IssueFilter{Labels: hot, SortBy: "created", Limit: 5, SkipWisps: true}},
		{Name: "skip labels", Filter: types.IssueFilter{Labels: hot, SortBy: "created", Limit: 5, SkipLabels: true}},
		{Name: "skip counts", Filter: types.IssueFilter{Labels: hot, SortBy: "created", Limit: 5, SkipCounts: true}},
		{Name: "lite", Filter: types.IssueFilter{Labels: hot, SortBy: "created", Limit: 5, Lite: true}},
		{Name: "status and label", Filter: types.IssueFilter{Labels: hot, Statuses: []types.Status{types.StatusOpen, types.StatusInProgress}, Limit: 5}},
		{Name: "text query", Query: "alpha", Filter: types.IssueFilter{Limit: 4}},
		// An empty entry in Labels or LabelsAny is a predicate in its own
		// right: `label = ''` matches only a stored empty label. The id scan's
		// label JOINs drop empty entries (sqlbuild.BuildLabelDrivenSearch), so
		// a filter that carries one must not be answered by them.
		{Name: "labels with an empty entry", Filter: types.IssueFilter{Labels: []string{"hot", ""}, SortBy: "created", Limit: 5}},
		{Name: "labels only empty", Filter: types.IssueFilter{Labels: []string{""}, Limit: 5}},
		{Name: "labelsAny with an empty entry", Filter: types.IssueFilter{LabelsAny: []string{"cold", ""}, SortBy: "created", Limit: 6}},
		{Name: "labelsAny only empty", Filter: types.IssueFilter{LabelsAny: []string{""}, Limit: 4}},
		{Name: "label and labelsAny with an empty entry", Filter: types.IssueFilter{Labels: hot, LabelsAny: []string{"", "warm"}, Limit: 5}},
		{Name: "no match", Filter: types.IssueFilter{Labels: []string{"nobody"}, Limit: 5}, NoMatch: true},
	}
}

func ptr[T any](v T) *T { return &v }

// checkCases runs every case through issueops.SearchIssuesWithCountsInTx and
// the oracle, each in its own read transaction, and compares the answers.
// requireRows fails a case whose reference answer is empty (unless it is
// marked NoMatch), so a broken fixture cannot pass by comparing nothing.
func checkCases(t *testing.T, db *sql.DB, cs []Case, requireRows bool) {
	t.Helper()
	for _, c := range cs {
		t.Run(c.Name, func(t *testing.T) {
			ctx := t.Context()
			got, gotErr := inTx(ctx, t, db, func(tx *sql.Tx) ([]*types.IssueWithCounts, error) {
				return issueops.SearchIssuesWithCountsInTx(ctx, tx, c.Query, c.Filter)
			})
			want, wantErr := inTx(ctx, t, db, func(tx *sql.Tx) ([]*types.IssueWithCounts, error) {
				return PredicateFormSearch(ctx, tx, c.Query, c.Filter)
			})
			if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
				t.Fatalf("error: got %v, want %v", gotErr, wantErr)
			}
			if requireRows && !c.NoMatch && wantErr == nil && len(want) == 0 {
				t.Fatalf("fixture premise: the reference matched nothing, so the comparison would be vacuous")
			}
			if c.WantRows > 0 && len(want) != c.WantRows {
				t.Fatalf("fixture premise: the reference delivered %d rows, want %d", len(want), c.WantRows)
			}
			compareRows(t, want, got)
		})
	}
}

// checkByIDsForm compares the by-IDs form over every id of a table family,
// one id per statement and all ids in one, with the predicate form over the
// whole table — under every hydration.
func checkByIDsForm(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()
	// Without wisp_dependencies the wisps family is never read, and the issues
	// family's reverse blockers come from dependencies alone.
	wispDeps := tableExists(ctx, t, db, "wisp_dependencies")
	families := []sqlbuild.FilterTables{sqlbuild.IssuesFilterTables}
	if wispDeps {
		families = append(families, sqlbuild.WispsFilterTables)
	}
	for _, tables := range families {
		for _, hyd := range []sqlbuild.CountsHydration{{}, {SkipLabels: true}, {SkipCounts: true}, {Lite: true}} {
			t.Run(fmt.Sprintf("by-IDs form %s %+v", tables.Main, hyd), func(t *testing.T) {
				q, _ := sqlbuild.SearchCountsSQL(tables, nil, "", "ORDER BY i.id", "", wispDeps, hyd)
				want := scan(ctx, t, db, q, nil, hyd)
				if len(want) == 0 {
					t.Fatalf("fixture has no %s rows", tables.Main)
				}
				ids := make([]string, len(want))
				for i, r := range want {
					ids[i] = r.Issue.ID
				}
				all, args := sqlbuild.SearchCountsSQL(tables, ids, "", "", "", wispDeps, hyd)
				compareRows(t, want, byID(scan(ctx, t, db, all, args, hyd), ids))
				var one []*types.IssueWithCounts
				for _, id := range ids {
					q1, a1 := sqlbuild.SearchCountsSQL(tables, []string{id}, "", "", "", wispDeps, hyd)
					one = append(one, scan(ctx, t, db, q1, a1, hyd)...)
				}
				compareRows(t, want, one)
			})
		}
	}
}

func byID(rows []*types.IssueWithCounts, order []string) []*types.IssueWithCounts {
	m := make(map[string]*types.IssueWithCounts, len(rows))
	for _, r := range rows {
		m[r.Issue.ID] = r
	}
	out := make([]*types.IssueWithCounts, 0, len(order))
	for _, id := range order {
		if r, ok := m[id]; ok {
			out = append(out, r)
		}
	}
	return out
}

// PredicateFormSearch is SearchIssuesWithCountsInTx as it stood before #7635:
// every leg is the predicate form, rendered by the same builders, and the
// control flow — the Ephemeral branch and its fall-through, SkipWisps, the
// empty or missing wisp plane, the wisp-preferred merge and the trim-then-cap
// finish — is restated from it. It is Run's oracle, and the baseline
// BenchmarkPerfSearchWithCountsLabelPage times the new path against.
func PredicateFormSearch(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	wispDepsExist, err := oracleTableExists(ctx, tx, "wisp_dependencies")
	if err != nil {
		return nil, err
	}
	leg := func(tables sqlbuild.FilterTables, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
		return oracleLeg(ctx, tx, query, filter, tables, includeWispReverseDeps)
	}
	if filter.Ephemeral != nil && *filter.Ephemeral {
		empty, err := oracleWispsEmptyOrMissing(ctx, tx)
		if err != nil {
			return nil, err
		}
		if !empty && wispDepsExist {
			wisps, err := leg(sqlbuild.WispsFilterTables, true)
			if err != nil && !oracleMissingOptionalWispTable(err) {
				return nil, err
			}
			if len(wisps) > 0 {
				return oracleFinish(wisps, filter)
			}
		}
		out, err := leg(sqlbuild.IssuesFilterTables, wispDepsExist)
		if err != nil {
			return nil, err
		}
		return oracleFinish(out, filter)
	}
	out, err := leg(sqlbuild.IssuesFilterTables, wispDepsExist)
	if err != nil {
		return nil, err
	}
	if filter.SkipWisps {
		return oracleFinish(out, filter)
	}
	empty, err := oracleWispsEmptyOrMissing(ctx, tx)
	if err != nil {
		return nil, err
	}
	if empty || !wispDepsExist {
		return oracleFinish(out, filter)
	}
	wisps, err := leg(sqlbuild.WispsFilterTables, true)
	if err != nil {
		if oracleMissingOptionalWispTable(err) {
			return oracleFinish(out, filter)
		}
		return nil, err
	}
	if len(wisps) == 0 {
		return oracleFinish(out, filter)
	}
	wispIDs := make(map[string]bool, len(wisps))
	for _, w := range wisps {
		wispIDs[w.Issue.ID] = true
	}
	var kept []*types.IssueWithCounts
	for _, r := range out {
		if !wispIDs[r.Issue.ID] {
			kept = append(kept, r)
		}
	}
	return oracleFinish(append(kept, wisps...), filter)
}

// oracleLeg is one table family in predicate form: the filter inside the
// derived driver, ORDER BY and the pushed bound outside the aggregate joins,
// and a Go-side sort applied before the bound.
func oracleLeg(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter, tables sqlbuild.FilterTables, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
	clauses, args, err := sqlbuild.BuildIssueFilterClauses(query, filter, tables)
	if err != nil {
		return nil, err
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = "WHERE " + strings.Join(clauses, " AND ")
	}
	goSideSort := sqlbuild.IsGoSideSort(filter.SortBy)
	eff := issueops.EffectiveSearchLimit(filter.Limit, filter.MaxRows)
	limitSQL := ""
	if eff > 0 && !goSideSort {
		limitSQL = fmt.Sprintf("LIMIT %d", eff)
	}
	hyd := sqlbuild.CountsHydration{SkipLabels: filter.SkipLabels, SkipCounts: filter.SkipCounts, Lite: filter.Lite}
	q, _ := sqlbuild.SearchCountsSQL(tables, nil, whereSQL, sqlbuild.OrderBy(filter.SortBy, filter.SortDesc, "i"), limitSQL, includeWispReverseDeps, hyd)
	out, err := scanRows(ctx, tx, q, args, hyd)
	if err != nil {
		return nil, err
	}
	if goSideSort {
		sort.SliceStable(out, func(i, j int) bool { return sqlbuild.LessID(out[i].Issue.ID, out[j].Issue.ID, filter.SortDesc) })
		if eff > 0 && len(out) > eff {
			out = out[:eff]
		}
	}
	return out, nil
}

func oracleFinish(items []*types.IssueWithCounts, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	sort.SliceStable(items, func(i, j int) bool {
		return sqlbuild.Less(items[i].Issue, items[j].Issue, filter.SortBy, filter.SortDesc)
	})
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	if err := issueops.EnforceMaxRowsCap(len(items), filter.MaxRows, filter.MaxRowsSource); err != nil {
		return nil, err
	}
	return items, nil
}

func oracleTableExists(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var probe int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM "+table+" LIMIT 1").Scan(&probe)
	switch {
	case err == nil, errors.Is(err, sql.ErrNoRows):
		return true, nil
	case dberrors.IsTableNotExist(err):
		return false, nil
	}
	return false, err
}

func oracleWispsEmptyOrMissing(ctx context.Context, tx *sql.Tx) (bool, error) {
	var probe int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM wisps LIMIT 1").Scan(&probe)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, sql.ErrNoRows), dberrors.IsTableNotExist(err):
		return true, nil
	}
	return false, err
}

func oracleMissingOptionalWispTable(err error) bool {
	name, ok := dberrors.MissingTableName(err)
	return ok && sqlbuild.OptionalWispTable(name)
}

// scanRows runs a counts statement and scans it the way production does:
// ScanReadyWorkRowWithCounts, first row per id wins.
func scanRows(ctx context.Context, tx *sql.Tx, q string, args []any, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*types.IssueWithCounts
	seen := map[string]bool{}
	for rows.Next() {
		r, err := issueops.ScanReadyWorkRowWithCounts(rows, hyd)
		if err != nil {
			return nil, err
		}
		if r == nil || r.Issue == nil || seen[r.Issue.ID] {
			continue
		}
		seen[r.Issue.ID] = true
		out = append(out, r)
	}
	return out, rows.Err()
}

func scan(ctx context.Context, t *testing.T, db *sql.DB, q string, args []any, hyd sqlbuild.CountsHydration) []*types.IssueWithCounts {
	t.Helper()
	out, err := inTx(ctx, t, db, func(tx *sql.Tx) ([]*types.IssueWithCounts, error) {
		return scanRows(ctx, tx, q, args, hyd)
	})
	if err != nil {
		t.Fatalf("query: %v\n%s", err, q)
	}
	return out
}

func inTx(ctx context.Context, t *testing.T, db *sql.DB, fn func(*sql.Tx) ([]*types.IssueWithCounts, error)) ([]*types.IssueWithCounts, error) {
	t.Helper()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

// compareRows fails unless got holds want's rows in want's order with every
// field equal, json:"-" fields included. Labels arrive sorted from the scan;
// dependencies are compared as a set, since JSON_ARRAYAGG promises no element
// order.
func compareRows(t *testing.T, want, got []*types.IssueWithCounts) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("rows: got %d %v, want %d %v", len(got), ids(got), len(want), ids(want))
	}
	for i := range want {
		w, g := normalize(want[i]), normalize(got[i])
		if !reflect.DeepEqual(w, g) {
			t.Fatalf("row %d (%s) differs:\n%s", i, want[i].Issue.ID, fieldDiff(reflect.ValueOf(g), reflect.ValueOf(w), ""))
		}
	}
}

func ids(rows []*types.IssueWithCounts) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Issue.ID
	}
	return out
}

// row is one result row flattened for comparison: the issue by value and the
// counts with the issue pointer cleared.
type row struct {
	Issue  types.Issue
	Counts types.IssueWithCounts
}

func normalize(r *types.IssueWithCounts) row {
	issue := *r.Issue
	if issue.Dependencies != nil {
		deps := append([]*types.Dependency{}, issue.Dependencies...)
		sort.Slice(deps, func(i, j int) bool {
			a, b := deps[i], deps[j]
			if a.DependsOnID != b.DependsOnID {
				return a.DependsOnID < b.DependsOnID
			}
			return a.Type < b.Type
		})
		issue.Dependencies = deps
	}
	counts := *r
	counts.Issue = nil
	return row{Issue: issue, Counts: counts}
}

// fieldDiff names the struct fields on which got and want differ.
func fieldDiff(got, want reflect.Value, prefix string) string {
	var out []string
	for i := 0; i < want.NumField(); i++ {
		f := want.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		g, w := got.Field(i), want.Field(i)
		if f.Type.Kind() == reflect.Struct && f.Type.PkgPath() != "time" {
			if d := fieldDiff(g, w, prefix+f.Name+"."); d != "" {
				out = append(out, d)
			}
			continue
		}
		if !reflect.DeepEqual(g.Interface(), w.Interface()) {
			out = append(out, fmt.Sprintf("  %s%s: got %s, want %s", prefix, f.Name, show(g), show(w)))
		}
	}
	return strings.Join(out, "\n")
}

func show(v reflect.Value) string {
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() == reflect.Slice {
		parts := make([]string, v.Len())
		for i := range parts {
			parts[i] = show(v.Index(i))
		}
		return "[" + strings.Join(parts, " ") + "]"
	}
	return fmt.Sprintf("%+v", v.Interface())
}

func tableExists(ctx context.Context, t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var probe int
	err := db.QueryRowContext(ctx, "SELECT 1 FROM "+table+" LIMIT 1").Scan(&probe)
	switch {
	case err == nil, errors.Is(err, sql.ErrNoRows):
		return true
	case dberrors.IsTableNotExist(err):
		return false
	}
	t.Fatalf("probe %s: %v", table, err)
	return false
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

var base = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// seed writes the fixture with plain INSERTs, so every column the ordering
// and the counts read is set exactly. Ids are eq-i01..eq-i11 (issues),
// eq-w01..eq-w10 (wisps) and eq-dup, which is in both tables.
func seed(t *testing.T, db *sql.DB, withWisps bool) {
	t.Helper()
	statuses := []string{"open", "in_progress", "closed", "open", "blocked"}
	titles := []string{"Alpha task", "beta task", "Gamma", "alpha two", "delta", "Epsilon", "zeta", "Eta", "theta", "Iota"}
	assignees := []any{nil, "ann", "bob", nil, "ann", "cy", nil, "bob", "dee", nil}
	insert := func(table, id, title, status string, priority int, created, updated time.Time, assignee any, ephemeral int) {
		var closed any
		if status == "closed" {
			closed = updated.Add(time.Minute)
		}
		exec(t, db, "INSERT INTO "+table+` (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type,
			created_at, updated_at, closed_at, created_by, assignee, ephemeral) VALUES (?, ?, '', '', '', '', ?, ?, 'task', ?, ?, ?, 'eq', ?, ?)`,
			id, title, status, priority, created, updated, closed, assignee, ephemeral)
	}
	for n := 1; n <= 10; n++ {
		i := n - 1
		insert("issues", fmt.Sprintf("eq-i%02d", n), titles[i], statuses[i%5], i%4,
			base.Add(time.Duration(n)*time.Minute), base.Add(time.Duration(10-n)*time.Hour), assignees[i], 0)
	}
	insert("issues", "eq-i11", "durable ephemeral", "open", 2, base.Add(11*time.Minute), base, nil, 1)
	insert("issues", "eq-dup", "dup issues copy", "open", 1, base.Add(30*time.Minute), base, nil, 0)

	labels := map[string][]string{
		"eq-i01": {"hot"}, "eq-i02": {"hot", "warm"}, "eq-i03": {"warm", "cold"}, "eq-i04": {"hot"},
		"eq-i05": {"cold"}, "eq-i06": {"hot", "warm", "cold"}, "eq-i08": {"hot"}, "eq-i10": {"hot"},
		"eq-i11": {"eph-only"}, "eq-dup": {"hot"},
	}
	// A stored empty label, on a row that also carries "hot" and on one that
	// does not, so a filter with an empty entry selects differently from the
	// same filter with the entry dropped.
	labels["eq-i04"] = append(labels["eq-i04"], "")
	labels["eq-i05"] = append(labels["eq-i05"], "")
	for id, ls := range labels {
		for _, l := range ls {
			exec(t, db, "INSERT INTO labels (issue_id, label) VALUES (?, ?)", id, l)
		}
	}
	dep := func(table, issue, col, target, typ string) {
		exec(t, db, "INSERT INTO "+table+" (id, issue_id, "+col+", type, created_at, created_by) VALUES (UUID(), ?, ?, ?, ?, 'eq')",
			issue, target, typ, base)
	}
	dep("dependencies", "eq-i02", "depends_on_issue_id", "eq-i01", "blocks")
	dep("dependencies", "eq-i03", "depends_on_issue_id", "eq-i01", "blocks")
	dep("dependencies", "eq-i05", "depends_on_external", "eq-i06", "blocks") // an external target that spells an id
	dep("dependencies", "eq-i07", "depends_on_external", "ext:other:1", "blocks")
	dep("dependencies", "eq-i02", "depends_on_issue_id", "eq-i10", "parent-child")
	dep("dependencies", "eq-i03", "depends_on_issue_id", "eq-i10", "parent-child")
	exec(t, db, "INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (UUID(), 'eq-i01', 'eq', 'one', ?), (UUID(), 'eq-i01', 'eq', 'two', ?)", base, base)
	if !withWisps {
		return
	}

	for n := 1; n <= 10; n++ {
		i := n - 1
		eph := 1
		if n == 10 {
			eph = 0 // a no-history row in the wisps table
		}
		insert("wisps", fmt.Sprintf("eq-w%02d", n), titles[(i+3)%10], statuses[(i+2)%5], (i+1)%4,
			base.Add(time.Duration(n)*time.Minute+30*time.Second), base.Add(time.Duration(n)*time.Hour), assignees[(i+5)%10], eph)
	}
	insert("wisps", "eq-dup", "dup wisps copy", "open", 1, base.Add(40*time.Minute), base, nil, 1)
	wispLabels := map[string][]string{
		"eq-w01": {"hot", ""}, "eq-w02": {"cold"}, "eq-w03": {"hot", "warm", "cold"}, "eq-w04": {"warm", ""},
		"eq-w05": {"hot"}, "eq-w07": {"hot"}, "eq-w09": {"hot"}, "eq-w10": {"hot"}, "eq-dup": {"hot"},
	}
	for id, ls := range wispLabels {
		for _, l := range ls {
			exec(t, db, "INSERT INTO wisp_labels (issue_id, label) VALUES (?, ?)", id, l)
		}
	}
	dep("wisp_dependencies", "eq-w02", "depends_on_wisp_id", "eq-w01", "blocks")
	dep("wisp_dependencies", "eq-w03", "depends_on_wisp_id", "eq-w01", "blocks")
	dep("wisp_dependencies", "eq-w04", "depends_on_issue_id", "eq-i01", "blocks")
	dep("wisp_dependencies", "eq-w05", "depends_on_external", "eq-w07", "blocks")
	dep("wisp_dependencies", "eq-w02", "depends_on_wisp_id", "eq-w10", "parent-child")
	dep("wisp_dependencies", "eq-w03", "depends_on_issue_id", "eq-i10", "parent-child")
	dep("dependencies", "eq-i04", "depends_on_wisp_id", "eq-w01", "blocks") // a durable issue blocked by a wisp
	exec(t, db, "INSERT INTO wisp_comments (id, issue_id, author, text, created_at) VALUES (UUID(), 'eq-w01', 'eq', 'w', ?), (UUID(), 'eq-w03', 'eq', 'x', ?), (UUID(), 'eq-w03', 'eq', 'y', ?), (UUID(), 'eq-dup', 'eq', 'z', ?)", base, base, base, base)
	exec(t, db, "INSERT INTO leases (issue_id, holder, granted_at, lease_expires_at, heartbeat_at) VALUES ('eq-w01', 'eq-holder', ?, ?, ?)",
		base, base.Add(time.Hour), base)
}

// plantTwoTargetEdges drops the one-target CHECKs, as a store with a drifted
// schema stands, and adds blocks edges with two targets each. COALESCE files
// each under its FIRST non-NULL target; the comments name the target a
// matcher without the IS NULL guards would also, wrongly, count.
func plantTwoTargetEdges(t *testing.T, db *sql.DB) {
	t.Helper()
	exec(t, db, "ALTER TABLE dependencies DROP CHECK ck_dep_one_target")
	exec(t, db, "ALTER TABLE wisp_dependencies DROP CHECK ck_wisp_dep_one_target")
	two := func(table, issue, colA, a, colB, b string) {
		exec(t, db, "INSERT INTO "+table+" (id, issue_id, "+colA+", "+colB+", type, created_at, created_by) VALUES (UUID(), ?, ?, ?, 'blocks', ?, 'eq')",
			issue, a, b, base)
	}
	two("dependencies", "eq-i08", "depends_on_issue_id", "eq-i09", "depends_on_wisp_id", "eq-w09")      // also eq-w09
	two("dependencies", "eq-i07", "depends_on_issue_id", "eq-dup", "depends_on_wisp_id", "eq-dup")      // eq-dup twice
	two("dependencies", "eq-i01", "depends_on_wisp_id", "eq-w05", "depends_on_external", "eq-w06")      // also eq-w06
	two("wisp_dependencies", "eq-w06", "depends_on_issue_id", "eq-i05", "depends_on_wisp_id", "eq-w08") // also eq-w08
	two("wisp_dependencies", "eq-w07", "depends_on_wisp_id", "eq-w04", "depends_on_external", "eq-w05") // also eq-w05
	two("wisp_dependencies", "eq-w09", "depends_on_issue_id", "eq-dup", "depends_on_wisp_id", "eq-dup") // eq-dup twice

	// Guard the premise: the rows are there, and the reference form files
	// them under their first target only.
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT
		(SELECT COUNT(*) FROM dependencies WHERE (depends_on_issue_id IS NOT NULL) + (depends_on_wisp_id IS NOT NULL) + (depends_on_external IS NOT NULL) > 1) +
		(SELECT COUNT(*) FROM wisp_dependencies WHERE (depends_on_issue_id IS NOT NULL) + (depends_on_wisp_id IS NOT NULL) + (depends_on_external IS NOT NULL) > 1)`).Scan(&n); err != nil {
		t.Fatalf("count two-target rows: %v", err)
	}
	if n != 6 {
		t.Fatalf("two-target rows planted = %d, want 6", n)
	}
	// Dependent counts as COALESCE files those rows; an unguarded matcher
	// gives eq-w09, eq-w06 and eq-w08 one each, eq-w05 two and eq-dup four.
	q, _ := sqlbuild.SearchCountsSQL(sqlbuild.WispsFilterTables, nil, "", "ORDER BY i.id", "", true, sqlbuild.CountsHydration{})
	want := map[string]int{"eq-w09": 0, "eq-w06": 0, "eq-w08": 0, "eq-w05": 1, "eq-dup": 2}
	for _, r := range scan(t.Context(), t, db, q, nil, sqlbuild.CountsHydration{}) {
		if w, ok := want[r.Issue.ID]; ok && r.DependentCount != w {
			t.Fatalf("reference dependent count of %s = %d, want %d", r.Issue.ID, r.DependentCount, w)
		}
	}
}

// seedLarge writes n issues (lg-i…) and n wisps (lg-w…). Rows share
// created_at in threes, so the id tie-break decides order inside a page, and
// one row in ten carries "other" instead of "bulk". Each row blocks on the
// previous one of its table, every fifth is a parent-child child of the
// table's first row, and every seventh has a comment.
func seedLarge(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	bulkInsert := func(table, cols, row string, rows [][]any) {
		const chunk = 100
		for start := 0; start < len(rows); start += chunk {
			end := min(start+chunk, len(rows))
			var args []any
			for _, r := range rows[start:end] {
				args = append(args, r...)
			}
			exec(t, db, "INSERT INTO "+table+" ("+cols+") VALUES "+strings.TrimSuffix(strings.Repeat(row+", ", end-start), ", "), args...)
		}
	}
	for _, tier := range []struct {
		main, labels, deps, comments, prefix string
		ephemeral                            int
	}{
		{"issues", "labels", "dependencies", "comments", "lg-i", 0},
		{"wisps", "wisp_labels", "wisp_dependencies", "wisp_comments", "lg-w", 1},
	} {
		id := func(k int) string { return fmt.Sprintf("%s%03d", tier.prefix, k) }
		offset := time.Duration(0)
		if tier.main == "wisps" {
			offset = 20 * time.Second
		}
		var rows, labelRows, depRows, commentRows [][]any
		for k := 0; k < n; k++ {
			created := base.Add(time.Duration(k/3)*time.Minute + offset)
			status, closed := "open", any(nil)
			if k%4 == 3 {
				status, closed = "closed", created.Add(time.Hour)
			}
			rows = append(rows, []any{id(k), fmt.Sprintf("Bulk %c%03d", 'a'+rune(k%26), k), status, k % 5, created, created, closed, tier.ephemeral})
			label := "bulk"
			if k%10 == 9 {
				label = "other"
			}
			labelRows = append(labelRows, []any{id(k), label})
			if k > 0 {
				depRows = append(depRows, []any{id(k), id(k - 1), "blocks"})
				if k%5 == 0 {
					depRows = append(depRows, []any{id(k), id(0), "parent-child"})
				}
			}
			if k%7 == 0 {
				commentRows = append(commentRows, []any{id(k)})
			}
		}
		target := "depends_on_issue_id"
		if tier.main == "wisps" {
			target = "depends_on_wisp_id"
		}
		bulkInsert(tier.main, "id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at, closed_at, created_by, ephemeral",
			"(?, ?, '', '', '', '', ?, ?, 'task', ?, ?, ?, 'eq', ?)", rows)
		bulkInsert(tier.labels, "issue_id, label", "(?, ?)", labelRows)
		bulkInsert(tier.deps, "id, issue_id, "+target+", type, created_at, created_by", "(UUID(), ?, ?, ?, '2026-03-01 09:00:00', 'eq')", depRows)
		bulkInsert(tier.comments, "id, issue_id, author, text, created_at", "(UUID(), ?, 'eq', 'c', '2026-03-01 09:00:00')", commentRows)
	}
}
