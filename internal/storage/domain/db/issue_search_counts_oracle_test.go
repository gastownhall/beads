package db

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// TestSearchCountsSinglePlaneMatchesThePredicateFormOracle (AC-3, GH#4370) is
// the proxied backend's oracle matrix. Over a filter × order × window ×
// hydration matrix, the counted search's single-plane routes (SkipWisps and
// Ephemeral-only) must answer exactly what the pre-change route answered: the
// PREDICATE form of the counts query under the old WHERE, ORDER BY and
// searchWindowForFilter clause, then finishSearchCountsPage's Go-side sort and
// window (cap, offset, page trim, has-more). Membership, order, counts,
// parent, labels, dependencies, HasMore and the MaxRows verdict are compared.
//
// The matrix reaches both sides of the routes' ceiling
// (countsWindowGoesIDsFirst): the one-row, offset and capped windows go
// ids-first, while the unlimited and 201-row windows, the Go-side id sort and
// the label filters with an empty entry keep the predicate form. The durable
// fixture holds more than sqlbuild.QueryBatchSize matching rows, and one row
// carries a stored empty label, so an empty filter entry that the id scan's
// label JOINs dropped would change the answer.
func (s *testSuite) TestSearchCountsSinglePlaneMatchesThePredicateFormOracle() {
	const prefix = "bd-sco-"
	const durable = 210
	r := s.issueRepo()
	dep := s.depRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())

	for i := 0; i < durable; i++ {
		iss := newTestIssue(fmt.Sprintf("%s%03d", prefix, i), fmt.Sprintf("oracle row %d", (i*37)%durable))
		iss.Priority = i % 4
		if i%5 == 0 {
			iss.Status = types.StatusInProgress
		}
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
	}
	for i := 0; i < 4; i++ {
		w := newTestIssue(fmt.Sprintf("%sw%d", prefix, i), fmt.Sprintf("oracle wisp %d", i))
		w.Ephemeral = true
		w.Priority = i % 2
		s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))
	}
	for i := 0; i < durable; i += 7 {
		s.Require().NoError(labelRepo.Insert(s.Ctx(), fmt.Sprintf("%s%03d", prefix, i), "alpha", "tester", domain.LabelOpts{}))
	}
	s.Require().NoError(labelRepo.Insert(s.Ctx(), prefix+"w1", "alpha", "tester", domain.LabelOpts{UseWispsTable: true}))
	// A stored empty label, which the predicate form's `label = ?` bound to
	// "" matches and the id scan's label JOINs would drop.
	_, err := s.Runner().ExecContext(s.Ctx(), "INSERT INTO labels (issue_id, label) VALUES (?, '')", prefix+"015")
	s.Require().NoError(err)
	for _, d := range [][3]string{
		{"001", "000", string(types.DepBlocks)},
		{"002", "000", string(types.DepBlocks)},
		{"003", "000", string(types.DepParentChild)},
		{"205", "204", string(types.DepBlocks)},
		{"w1", "w0", string(types.DepBlocks)},
		{"w2", "000", string(types.DepBlocks)},
		{"007", "w0", string(types.DepBlocks)},
	} {
		opts := domain.DepInsertOpts{UseWispsTable: strings.HasPrefix(d[0], "w")}
		s.Require().NoError(dep.Insert(s.Ctx(), newDep(prefix+d[0], prefix+d[1], types.DependencyType(d[2])), "tester", opts))
	}
	for _, id := range []string{"000", "000", "201"} {
		_, err := s.Runner().ExecContext(s.Ctx(),
			"INSERT INTO comments (id, issue_id, author, text) VALUES (UUID(), ?, ?, ?)", prefix+id, "tester", "note")
		s.Require().NoError(err)
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
		{"labels-empty-entry", "", func(f *types.IssueFilter) { f.Labels = []string{"alpha", ""} }},
		{"labels-any-empty-entry", "", func(f *types.IssueFilter) { f.LabelsAny = []string{"alpha", ""} }},
	}
	orders := []variant{
		{"default", "", func(*types.IssueFilter) {}},
		{"created", "", func(f *types.IssueFilter) { f.SortBy = "created" }},
		{"title", "", func(f *types.IssueFilter) { f.SortBy = "title" }},
		{"priority-desc", "", func(f *types.IssueFilter) { f.SortBy, f.SortDesc = "priority", true }},
		{"id-desc", "", func(f *types.IssueFilter) { f.SortBy, f.SortDesc = "id", true }},
	}
	windows := []variant{
		{"unlimited", "", func(*types.IssueFilter) {}},
		{"limit1", "", func(f *types.IssueFilter) { f.Limit = 1 }},
		{"limit201", "", func(f *types.IssueFilter) { f.Limit = 201 }},
		{"offset", "", func(f *types.IssueFilter) { f.Limit, f.Offset = 2, 3 }},
		{"offset-capped", "", func(f *types.IssueFilter) { f.Limit, f.Offset, f.MaxRows, f.MaxRowsSource = 2, 3, 100, "test" }},
		{"capped", "", func(f *types.IssueFilter) { f.MaxRows, f.MaxRowsSource = 50, "test" }},
	}
	hydrations := []variant{
		{"full", "", func(*types.IssueFilter) {}},
		{"skip-labels", "", func(f *types.IssueFilter) { f.SkipLabels = true }},
		{"skip-counts", "", func(f *types.IssueFilter) { f.SkipCounts = true }},
		{"lite", "", func(f *types.IssueFilter) { f.Lite = true }},
	}
	yes := true
	routes := []variant{
		{"skip-wisps", "", func(f *types.IssueFilter) { f.SkipWisps = true }},
		{"ephemeral", "", func(f *types.IssueFilter) { f.Ephemeral = &yes }},
	}

	impl := &issueSQLRepositoryImpl{runner: s.Runner()}
	idsFirst, predicate, pastBatch := 0, 0, false
	n := 0
	for _, rv := range routes {
		for _, fv := range filters {
			for _, ov := range orders {
				for _, wv := range windows {
					hv := hydrations[n%len(hydrations)]
					n++
					filter := types.IssueFilter{IDPrefix: prefix}
					for _, v := range []variant{rv, fv, ov, wv, hv} {
						v.apply(&filter)
					}
					name := fmt.Sprintf("%s/%s/%s/%s/%s", rv.name, fv.name, ov.name, wv.name, hv.name)

					tables, wispRev := issuesFilterTables, true
					if filter.Ephemeral != nil {
						tables = wispsFilterTables
					}
					want, wantErr := oracleSearchCountsPage(s.Ctx(), impl, fv.query, filter, tables, wispRev)
					got, gotErr := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), fv.query, filter)
					var tooMany *issueops.ErrTooManyRows
					if (wantErr != nil) != (gotErr != nil) || (wantErr != nil && !errors.As(gotErr, &tooMany)) {
						s.FailNowf("error mismatch", "%s: error = %v, oracle error = %v", name, gotErr, wantErr)
					}
					if wantErr != nil {
						continue
					}
					if countsWindowGoesIDsFirst(filter, searchWindowForFilter(filter)) {
						idsFirst++
					} else {
						predicate++
					}
					if len(want.Items) > sqlbuild.QueryBatchSize {
						pastBatch = true
					}
					s.Require().Equal(flattenCountsPage(want), flattenCountsPage(got), name)
					s.Require().Equal(want.HasMore, got.HasMore, "%s: HasMore", name)
				}
			}
		}
	}
	s.Require().Positive(idsFirst, "no case went ids-first")
	s.Require().Positive(predicate, "no case kept the predicate form")
	s.Require().True(pastBatch, "no case returned more than %d rows; the matrix never reached past one by-IDs batch", sqlbuild.QueryBatchSize)
}

// oracleSearchCountsPage replays the pre-GH#4370 single-plane route:
// runFilterSearchQuery's predicate-form counts query, then
// finishSearchCountsPage.
func oracleSearchCountsPage(ctx context.Context, r *issueSQLRepositoryImpl, query string, filter types.IssueFilter, tables filterTables, wispRev bool) (domain.SearchCountsPage, error) {
	clauses, args, err := buildIssueFilterClauses(query, filter, tables)
	if err != nil {
		return domain.SearchCountsPage{}, err
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = "WHERE " + strings.Join(clauses, " AND ")
	}
	window := searchWindowForFilter(filter)
	hyd := hydrationFor(filter)
	q, _ := sqlbuild.SearchCountsSQL(tables, nil, whereSQL, orderBySQL(filter.SortBy, filter.SortDesc, "i"), window.sql, wispRev, hyd)
	items, err := r.scanCountsQuery(ctx, tables, q, args, hyd)
	if err != nil {
		return domain.SearchCountsPage{}, err
	}
	sortRowsGoSide(items, func(iwc *types.IssueWithCounts) string { return iwc.Issue.ID }, filter.SortBy, filter.SortDesc)
	trimmed, hasMore, err := finishWindow(items, window)
	if err != nil {
		return domain.SearchCountsPage{}, err
	}
	return domain.SearchCountsPage{Items: trimmed, HasMore: hasMore}, nil
}

// flattenCountsPage renders each row's id and every hydrated value, with
// labels and dependencies sorted (their JSON aggregates carry no ORDER BY).
func flattenCountsPage(page domain.SearchCountsPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, iwc := range page.Items {
		parent := ""
		if iwc.Parent != nil {
			parent = *iwc.Parent
		}
		labels := append([]string(nil), iwc.Issue.Labels...)
		sort.Strings(labels)
		deps := make([]string, 0, len(iwc.Issue.Dependencies))
		for _, d := range iwc.Issue.Dependencies {
			deps = append(deps, string(d.Type)+":"+d.DependsOnID)
		}
		sort.Strings(deps)
		out = append(out, fmt.Sprintf("%s dep=%d rdep=%d cmt=%d parent=%s labels=%s deps=%s",
			iwc.Issue.ID, iwc.DependencyCount, iwc.DependentCount, iwc.CommentCount, parent,
			strings.Join(labels, ","), strings.Join(deps, "|")))
	}
	return out
}
