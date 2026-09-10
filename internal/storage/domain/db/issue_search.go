package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

const queryBatchSize = 200

// filterTables aliases the shared table-name config so both stacks build
// against the same definitions (bd-6dnrw.46).
type filterTables = sqlbuild.FilterTables

var (
	issuesFilterTables = sqlbuild.IssuesFilterTables
	wispsFilterTables  = sqlbuild.WispsFilterTables
)

// missingOptionalWispTable reports whether err is the absence of a wisp-plane
// table a database may legitimately not have. A wisp search touches more than
// that: its FROM clause carries sqlbuild.LeaseJoin and its hydration reads
// wisp_labels, so a blanket table-not-exist check reports a broken database as
// an empty wisp plane and silently drops live rows from the result.
func missingOptionalWispTable(err error) bool {
	name, ok := dberrors.MissingTableName(err)
	return ok && sqlbuild.OptionalWispTable(name)
}

func (r *issueSQLRepositoryImpl) searchAcrossIssuesAndWisps(ctx context.Context, query string, filter types.IssueFilter) (domain.SearchPage, error) {
	if filter.Ephemeral != nil && *filter.Ephemeral {
		page, err := r.searchTable(ctx, query, filter, wispsFilterTables)
		if err != nil && !missingOptionalWispTable(err) {
			return domain.SearchPage{}, fmt.Errorf("search wisps (ephemeral filter): %w", err)
		}
		if len(page.Items) > 0 {
			return page, nil
		}
	}

	if filter.SkipWisps {
		page, err := r.searchTable(ctx, query, filter, issuesFilterTables)
		if err != nil {
			return domain.SearchPage{}, fmt.Errorf("search issues: %w", err)
		}
		return page, nil
	}

	empty, probeErr := r.wispsTableEmptyOrMissing(ctx)
	if probeErr != nil {
		return domain.SearchPage{}, fmt.Errorf("search wisps (merge): probe: %w", probeErr)
	}
	if empty {
		page, err := r.searchTable(ctx, query, filter, issuesFilterTables)
		if err != nil {
			return domain.SearchPage{}, fmt.Errorf("search issues: %w", err)
		}
		return page, nil
	}

	return r.searchUnion(ctx, query, filter)
}

func (r *issueSQLRepositoryImpl) searchUnion(ctx context.Context, query string, filter types.IssueFilter) (domain.SearchPage, error) {
	outerOrderBy := unionOrderBySQL(filter.SortBy, filter.SortDesc)
	window := searchWindowForFilter(filter)
	legWindow := legWindowSQL(outerOrderBy, window)

	iSub, iArgs, err := r.buildUnionSubquery(query, filter, issuesFilterTables, "i", legWindow)
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("search union (issues): %w", err)
	}
	wSub, wArgs, err := r.buildUnionSubquery(query, filter, wispsFilterTables, "w", legWindow)
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("search union (wisps): %w", err)
	}

	// EACH LEG IS PARENTHESIZED, and it is not decoration. A leg that carries
	// its own ORDER BY and LIMIT (legWindowSQL) is a syntax error inside a bare
	// UNION ALL — the engine reads the clause as belonging to the union — so the
	// parentheses are what let the window be pushed down at all.
	//nolint:gosec // G201: subqueries built from hardcoded table names and ? placeholders.
	unionSQL := fmt.Sprintf("SELECT id, src FROM ((%s) UNION ALL (%s)) merged %s %s",
		iSub, wSub, outerOrderBy, window.sql)

	args := make([]any, 0, len(iArgs)+len(wArgs))
	args = append(args, iArgs...)
	args = append(args, wArgs...)

	rows, err := r.runner.QueryContext(ctx, unionSQL, args...)
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("search union: %w", err)
	}
	page, err := scanIDSrcPage(rows)
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("search union: %w", err)
	}
	page.sortGoSide(filter.SortBy, filter.SortDesc)
	hasMore, err := page.finishWindow(window)
	if err != nil {
		return domain.SearchPage{}, err
	}

	issuesByID, err := r.fetchIssuesByIDs(ctx, page.issueIDs, issuesFilterTables, filter)
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("search union (hydrate issues): %w", err)
	}
	wispsByID, err := r.fetchIssuesByIDs(ctx, page.wispIDs, wispsFilterTables, filter)
	if err != nil && !missingOptionalWispTable(err) {
		return domain.SearchPage{}, fmt.Errorf("search union (hydrate wisps): %w", err)
	}

	out := reassembleBySrc(page.ordered, issuesByID, wispsByID)
	return domain.SearchPage{Items: out, HasMore: hasMore}, nil
}

// buildUnionSubquery renders one leg of the wisp-inclusive UNION ALL.
//
// legWindow is the ORDER BY and LIMIT the leg carries, from legWindowSQL. It is
// the leg's whole share of the caller's window, and pushing it down is sound
// for the reason a top-N is: under a TOTAL order, a row in the union's top-N is
// in its own leg's top-N too, because anything ahead of it in its leg is ahead
// of it globally. Every ORDER BY sqlbuild renders ends in `id ASC`, so the
// order is total and there are no ties for the bound to break arbitrarily.
//
// It is empty when the sort has no SQL ORDER BY, and that is not a missed case:
// a LIMIT on an UNORDERED leg is the bug this exists to remove, one level
// further down and harder to see.
func (r *issueSQLRepositoryImpl) buildUnionSubquery(query string, filter types.IssueFilter, tables filterTables, srcTag, legWindow string) (string, []any, error) {
	plan := buildLabelDrivenSearch(filter, tables)
	whereClauses, args, err := buildIssueFilterClauses(query, plan.Filter, tables)
	if err != nil {
		return "", nil, err
	}
	whereClauses, args = plan.MergeInto(whereClauses, args)
	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}
	selectKw := "SELECT"
	if plan.Distinct {
		selectKw = "SELECT DISTINCT"
	}
	//nolint:gosec // G201: srcTag is a hardcoded 'i' or 'w'; fromSQL/whereSQL/legWindow composed from fixed table names, sort aliases and ? placeholders.
	sub := fmt.Sprintf("%s id, '%s' AS src, %s FROM %s %s %s",
		selectKw, srcTag, unionSortColumnsSQL, plan.FromSQL, whereSQL, legWindow)
	return sub, args, nil
}

// legWindowSQL is the window each UNION leg carries: the SAME ordering the
// outer query applies, plus the deepest row index that outer query can reach.
//
// THE ORDER CLAUSE IS REUSED VERBATIM rather than rebuilt, and that is what
// makes the bound safe. unionOrderBySQL names `id` and the `sort_*` aliases,
// both of which every leg projects, so one string is valid in both positions
// and the leg cannot come to rest in a different order than the one the outer
// query is about to cut against.
//
// legLimit is the outer bound PLUS any offset the engine carries, because those
// are rows the outer query reads and discards — a leg cut to the page size
// alone would starve an offset page of exactly the rows it was going to skip
// past. An offset this package keeps for itself (the capped case) is already
// inside the bound.
func legWindowSQL(outerOrderBy string, w searchWindow) string {
	if outerOrderBy == "" || w.legLimit <= 0 {
		return ""
	}
	return fmt.Sprintf("%s LIMIT %d", outerOrderBy, w.legLimit)
}

func (r *issueSQLRepositoryImpl) fetchIssuesByIDs(ctx context.Context, ids []string, tables filterTables, filter types.IssueFilter) (map[string]*types.Issue, error) {
	if len(ids) == 0 {
		return map[string]*types.Issue{}, nil
	}

	placeholders, args := buildInPlaceholders(ids)

	//nolint:gosec // G201: tables.Main is "issues" or "wisps"; placeholders are ?.
	fetchSQL := fmt.Sprintf(`SELECT %s FROM %s %s WHERE id IN (%s)`,
		issueSelectColumns, tables.Main, sqlbuild.LeaseJoin(tables.Main), placeholders)
	rows, err := r.runner.QueryContext(ctx, fetchSQL, args...)
	if err != nil {
		return nil, err
	}

	out := make(map[string]*types.Issue, len(ids))
	ordered := make([]*types.Issue, 0, len(ids))
	for rows.Next() {
		issue, scanErr := scanIssue(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan: %w", scanErr)
		}
		out[issue.ID] = issue
		ordered = append(ordered, issue)
	}

	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	if err := r.hydrateIssues(ctx, ordered, tables, filter.IncludeDependencies, filter.SkipLabels); err != nil {
		return nil, fmt.Errorf("hydrate: %w", err)
	}
	return out, nil
}

func (r *issueSQLRepositoryImpl) searchTable(ctx context.Context, query string, filter types.IssueFilter, tables filterTables) (domain.SearchPage, error) {
	plan := buildLabelDrivenSearch(filter, tables)
	whereClauses, args, err := buildIssueFilterClauses(query, plan.Filter, tables)
	if err != nil {
		return domain.SearchPage{}, err
	}
	whereClauses, args = plan.MergeInto(whereClauses, args)

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	selectKw := "SELECT "
	if plan.Distinct {
		selectKw = "SELECT DISTINCT "
	}

	if filter.Limit > 0 && !filter.NoIDShrink {
		ids, hasMore, err := r.scanFilterIDs(ctx, selectKw, plan.FromSQL, whereSQL, args, filter, tables)
		if err != nil {
			return domain.SearchPage{}, err
		}
		if len(ids) == 0 {
			return domain.SearchPage{}, nil
		}
		byID, err := r.fetchIssuesByIDs(ctx, ids, tables, filter)
		if err != nil {
			return domain.SearchPage{}, fmt.Errorf("search %s (hydrate): %w", tables.Main, err)
		}
		return domain.SearchPage{Items: orderByIDs(ids, byID), HasMore: hasMore}, nil
	}

	orderBy := orderBySQL(filter.SortBy, filter.SortDesc, "")
	window := searchWindowForFilter(filter)

	//nolint:gosec // G201: SQL fragments from fixed table names and parameterized filters.
	querySQL := fmt.Sprintf(`%s%s FROM %s %s %s %s %s`,
		selectKw, issueSelectColumns, plan.FromSQL, sqlbuild.LeaseJoin(tables.Main), whereSQL, orderBy, window.sql)

	rows, err := r.runner.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("search %s: %w", tables.Main, err)
	}

	var issues []*types.Issue
	seen := make(map[string]bool)
	for rows.Next() {
		issue, scanErr := scanIssue(rows)
		if scanErr != nil {
			_ = rows.Close()
			return domain.SearchPage{}, fmt.Errorf("search %s: scan: %w", tables.Main, scanErr)
		}
		if seen[issue.ID] {
			continue
		}
		seen[issue.ID] = true
		issues = append(issues, issue)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return domain.SearchPage{}, fmt.Errorf("search %s: rows: %w", tables.Main, err)
	}

	sortRowsGoSide(issues, func(i *types.Issue) string { return i.ID }, filter.SortBy, filter.SortDesc)
	items, hasMore, err := finishWindow(issues, window)
	if err != nil {
		return domain.SearchPage{}, err
	}

	if err := r.hydrateIssues(ctx, items, tables, filter.IncludeDependencies, filter.SkipLabels); err != nil {
		return domain.SearchPage{}, fmt.Errorf("search %s: hydrate: %w", tables.Main, err)
	}

	return domain.SearchPage{Items: items, HasMore: hasMore}, nil
}

func (r *issueSQLRepositoryImpl) scanFilterIDs(ctx context.Context, selectKw, fromSQL, whereSQL string, args []any, filter types.IssueFilter, tables filterTables) ([]string, bool, error) {
	orderBy := orderBySQL(filter.SortBy, filter.SortDesc, tables.Main)
	window := searchWindowForFilter(filter)
	//nolint:gosec // G201: SQL fragments from fixed table names and parameterized filters.
	idQuery := fmt.Sprintf(`%s%s.id FROM %s %s %s %s`,
		selectKw, tables.Main, fromSQL, whereSQL, orderBy, window.sql)

	rows, err := r.runner.QueryContext(ctx, idQuery, args...)
	if err != nil {
		return nil, false, fmt.Errorf("search %s (id scan): %w", tables.Main, err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, fmt.Errorf("search %s (id scan): scan: %w", tables.Main, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("search %s (id scan): rows: %w", tables.Main, err)
	}

	sortRowsGoSide(ids, func(id string) string { return id }, filter.SortBy, filter.SortDesc)
	return finishWindow(ids, window)
}

func (r *issueSQLRepositoryImpl) hydrateIssues(ctx context.Context, issues []*types.Issue, tables filterTables, includeDeps bool, skipLabels bool) error {
	if len(issues) == 0 {
		return nil
	}

	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}

	if !skipLabels {
		labelMap, err := r.getLabelsFromTable(ctx, tables.Labels, ids)
		if err != nil {
			return fmt.Errorf("hydrate labels: %w", err)
		}
		for _, issue := range issues {
			if labels, ok := labelMap[issue.ID]; ok {
				issue.Labels = labels
			}
		}
	}

	if includeDeps {
		depMap, err := r.getDependencyRecordsFromTable(ctx, tables.Dependencies, ids)
		if err != nil {
			return fmt.Errorf("hydrate dependencies: %w", err)
		}
		for _, issue := range issues {
			if deps, ok := depMap[issue.ID]; ok {
				issue.Dependencies = deps
			}
		}
	}

	return nil
}

//nolint:gosec // G201: labelTable is "labels" or "wisp_labels" (hardcoded by callers).
func (r *issueSQLRepositoryImpl) getLabelsFromTable(ctx context.Context, labelTable string, ids []string) (map[string][]string, error) {
	result := make(map[string][]string)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildInPlaceholders(ids[start:end])
		rows, err := r.runner.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id, label FROM %s WHERE issue_id IN (%s) ORDER BY issue_id, label`,
			labelTable, placeholders), args...)
		if err != nil {
			return nil, fmt.Errorf("get labels from %s: %w", labelTable, err)
		}
		for rows.Next() {
			var issueID, label string
			if err := rows.Scan(&issueID, &label); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get labels: scan: %w", err)
			}
			result[issueID] = append(result[issueID], label)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get labels: rows: %w", err)
		}
	}
	return result, nil
}

//nolint:gosec // G201: depTable is "dependencies" or "wisp_dependencies" (hardcoded by callers).
func (r *issueSQLRepositoryImpl) getDependencyRecordsFromTable(ctx context.Context, depTable string, ids []string) (map[string][]*types.Dependency, error) {
	result := make(map[string][]*types.Dependency)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		placeholders, args := buildInPlaceholders(ids[start:end])
		rows, err := r.runner.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id, %s AS depends_on_id, type, created_at, created_by, metadata, thread_id
			 FROM %s WHERE issue_id IN (%s) ORDER BY issue_id`,
			depTargetExpr, depTable, placeholders), args...)
		if err != nil {
			return nil, fmt.Errorf("get dep records from %s: %w", depTable, err)
		}
		for rows.Next() {
			dep, scanErr := scanDepRow(rows)
			if scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get dep records: scan: %w", scanErr)
			}
			result[dep.IssueID] = append(result[dep.IssueID], dep)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get dep records: rows: %w", err)
		}
	}
	return result, nil
}

func scanDepRow(rows *sql.Rows) (*types.Dependency, error) {
	var dep types.Dependency
	var createdAt sql.NullTime
	var createdBy, metadata, threadID sql.NullString
	if err := rows.Scan(&dep.IssueID, &dep.DependsOnID, &dep.Type, &createdAt, &createdBy, &metadata, &threadID); err != nil {
		return nil, err
	}
	if createdAt.Valid {
		dep.CreatedAt = createdAt.Time
	}
	if createdBy.Valid {
		dep.CreatedBy = createdBy.String
	}
	if metadata.Valid {
		dep.Metadata = metadata.String
	}
	if threadID.Valid {
		dep.ThreadID = threadID.String
	}
	return &dep, nil
}

func (r *issueSQLRepositoryImpl) wispsTableEmptyOrMissing(ctx context.Context) (bool, error) {
	var probe int
	err := r.runner.QueryRowContext(ctx, "SELECT 1 FROM wisps LIMIT 1").Scan(&probe)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, sql.ErrNoRows):
		return true, nil
	case dberrors.IsTableNotExist(err):
		return true, nil
	default:
		return false, err
	}
}

func buildLabelDrivenSearch(filter types.IssueFilter, tables filterTables) sqlbuild.LabelSearchPlan {
	return sqlbuild.BuildLabelDrivenSearch(filter, tables)
}

func buildIssueFilterClauses(query string, filter types.IssueFilter, tables filterTables) ([]string, []any, error) {
	return sqlbuild.BuildIssueFilterClauses(query, filter, tables)
}

type idSrcPage struct {
	ordered  []idSrcRef
	issueIDs []string
	wispIDs  []string
}

// scanIDSrcPage reads the (id, src) projection of a UNION ALL over the issues
// and wisps legs, in the order the outer ORDER BY produced it, and collapses
// the duplicates. There are two kinds and they are not the same event.
//
// A REPEAT WITHIN ONE LEG is ordinary: a dependency or label subquery can match
// the same row twice. The first occurrence wins, exactly as the per-table scans
// resolve it.
//
// A CROSS-TABLE DUPLICATE is corruption — one id resident in both planes. No
// local write path can produce it: issueops.EnsureIssueIDAvailableInTx probes
// BOTH tables before every insert, and promotion moves a row inside one
// transaction. Replication can, by merging a durable row into a clone that
// still holds the wisp.
//
// THE WISP COPY IS CANONICAL AND THE READ STILL ANSWERS. This used to be a hard
// error for the whole query, which made a store with one corrupt id unable to
// answer any question about the other rows. Three things say that was the wrong
// verdict. The per-table seam has always resolved it the other way and says why
// — "hard-erroring breaks every lookup city-wide" (issueops.searchInTx, and the
// three sibling merges beside it, be-iabdi). `bd doctor` detects the state with
// a query of its own rather than by watching reads fail, so nothing is blinded
// by answering; and its fix removes the stale ISSUES copy, calling the wisps row
// canonical. And its own check text names the hard failure as the damage:
// "stale issues-table copies break every lookup for the affected IDs".
//
// So the durable row is dropped and the wisp row is kept at its own position,
// which is the page the per-table seam produces for the same data. A page can
// come back one row short of its limit when this fires; that is a corrupt row
// being withheld, not a paging bug, and the repair is a doctor run.
//
// THE DROP REACHES ONLY THE SCANNED WINDOW, which is narrower than "wherever it
// sits" — the phrase this used to use. Both copies have to be inside the rows
// this query returned for the shadow to be seen at all, and under a bounded
// search they need not be: legs carry the outer bound now (legWindowSQL), so a
// durable copy inside the window whose wisp twin ranks past it survives the
// scan and is reported. That is a corrupt store answering approximately, not a
// second bug; the detector is `bd doctor`, not this loop.
func scanIDSrcPage(rows *sql.Rows) (idSrcPage, error) {
	defer func() { _ = rows.Close() }()

	var scanned []idSrcRef
	seen := make(map[idSrcRef]bool)
	shadowed := make(map[string]bool)
	for rows.Next() {
		var id, src string
		if err := rows.Scan(&id, &src); err != nil {
			return idSrcPage{}, fmt.Errorf("scan: %w", err)
		}
		ref := idSrcRef{id: id, src: src}
		if src == "w" {
			shadowed[id] = true
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		scanned = append(scanned, ref)
	}
	if err := rows.Err(); err != nil {
		return idSrcPage{}, fmt.Errorf("rows: %w", err)
	}

	// The wisps leg is known in full only after the scan — the outer ORDER BY
	// can place either copy first — so the drop is a second pass rather than a
	// decision taken row by row.
	var page idSrcPage
	for _, ref := range scanned {
		if ref.src == "i" && shadowed[ref.id] {
			continue
		}
		page.ordered = append(page.ordered, ref)
		switch ref.src {
		case "i":
			page.issueIDs = append(page.issueIDs, ref.id)
		case "w":
			page.wispIDs = append(page.wispIDs, ref.id)
		}
	}
	return page, nil
}

func orderByIDs[T any](ids []string, byID map[string]T) []T {
	out := make([]T, 0, len(ids))
	for _, id := range ids {
		if v, ok := byID[id]; ok {
			out = append(out, v)
		}
	}
	return out
}

func reassembleBySrc[T comparable](ordered []idSrcRef, issues, wisps map[string]T) []T {
	var zero T
	out := make([]T, 0, len(ordered))
	for _, p := range ordered {
		var v T
		switch p.src {
		case "i":
			v = issues[p.id]
		case "w":
			v = wisps[p.id]
		}
		if v != zero {
			out = append(out, v)
		}
	}
	return out
}

// sortGoSide establishes the order for a sort key SQL renders no ORDER BY for,
// BEFORE finishWindow's trim decides which rows the page keeps.
//
// It is the union seam's copy of issueops.sortMergedResults, and it exists for
// the identical reason that one gives: "a concatenation of two independently-
// ordered legs" trimmed without a re-sort "keeps an arbitrary prefix of the
// concatenation" rather than the top-n. Under a Go-side sort the legs are not
// merely independently ordered, they are UNORDERED — searchWindowFor pushes no
// bound in that case precisely so the whole matching set arrives here — and
// this is where the requested order first exists.
//
// The only such key is "id", and the id is on the ref already, so no row has to
// be hydrated to be placed. The comparison is the byte order SQL would apply
// and the byte order sqlbuild.Less applies for this key; the natural-numeric
// order a human reads (bd-9 before bd-10) is applied above, on the delivered
// page, by workapi.CompareIssuesBy. This decides MEMBERSHIP; that decides
// DISPLAY.
//
// It honors sortDesc, as sqlbuild.Less and the per-table seam's
// sortRowsGoSide now also do for this key (both were once live sibling bugs
// this comment named; bd-jao3t/bd-69c1a closed them).
func (p *idSrcPage) sortGoSide(sortBy string, sortDesc bool) {
	if !sqlbuild.IsGoSideSort(sortBy) || len(p.ordered) <= 1 {
		return
	}
	sort.SliceStable(p.ordered, func(i, j int) bool {
		return sqlbuild.LessID(p.ordered[i].id, p.ordered[j].id, sortDesc)
	})
	p.issueIDs = p.issueIDs[:0]
	p.wispIDs = p.wispIDs[:0]
	for _, r := range p.ordered {
		switch r.src {
		case "i":
			p.issueIDs = append(p.issueIDs, r.id)
		case "w":
			p.wispIDs = append(p.wispIDs, r.id)
		}
	}
}

// finishWindow is finishWindow's shape for a merged id page: the same cap,
// skip and trim, with the per-table id lists rebuilt once from whatever
// survives.
func (p *idSrcPage) finishWindow(w searchWindow) (bool, error) {
	if err := issueops.EnforceMaxRowsCap(len(p.ordered), w.rowCap, w.capSource); err != nil {
		return false, err
	}
	ordered, hasMore := applyN1Overflow(dropFirst(p.ordered, w.skip), w.limit)
	if len(ordered) == len(p.ordered) {
		return hasMore, nil
	}
	p.ordered = ordered
	p.issueIDs = p.issueIDs[:0]
	p.wispIDs = p.wispIDs[:0]
	for _, r := range p.ordered {
		switch r.src {
		case "i":
			p.issueIDs = append(p.issueIDs, r.id)
		case "w":
			p.wispIDs = append(p.wispIDs, r.id)
		}
	}
	return hasMore, nil
}

type idSrcRef struct{ id, src string }

// sortRowsGoSide is the per-table seam's copy of idSrcPage.sortGoSide: it
// establishes the order for a sort key SQL renders no ORDER BY for, BEFORE
// finishWindow's trim decides which rows the page keeps. searchWindowFor
// pushes no page bound under a Go-side sort precisely so the whole matching
// set arrives here — this is where the requested order first exists; without
// it the trim keeps an arbitrary engine-ordered subset (order-right,
// membership-wrong once sorted downstream). The comparison is byte order,
// matching sqlbuild.Less and idSrcPage.sortGoSide; the natural-numeric
// display order is applied later, on the delivered page (see sortGoSide's
// doc for the membership/display split). No-op for SQL-rendered sort keys,
// whose ORDER BY already ran.
func sortRowsGoSide[T any](rows []T, id func(T) string, sortBy string, sortDesc bool) {
	if !sqlbuild.IsGoSideSort(sortBy) || len(rows) <= 1 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return sqlbuild.LessID(id(rows[i]), id(rows[j]), sortDesc)
	})
}

const unionSortColumnsSQL = sqlbuild.UnionSortColumnsSQL

func unionOrderBySQL(sortBy string, sortDesc bool) string {
	return sqlbuild.OrderByForColumns(sortBy, sortDesc, func(k string) string {
		if k == "id" {
			return "id"
		}
		return "sort_" + k
	})
}

func orderBySQL(sortBy string, sortDesc bool, prefix string) string {
	return sqlbuild.OrderBy(sortBy, sortDesc, prefix)
}

// searchWindow is the row window one search runs under: the clause the engine
// carries, and what this package must still do to the rows that come back. It
// is one value rather than two decisions at each call site because the halves
// only add up when they are decided together.
//
// WHO CARRIES THE OFFSET is the whole of it, and A CAP DECIDES IT. MaxRows
// counts the rows the query TOUCHED — offset + limit — because a row the engine
// skipped is still a row it matched, so an offset walks a caller toward the
// breaker and must never walk one past it. When a cap is set the skip therefore
// stays HERE: the bound covers the rows this package is about to drop, and
// EnforceMaxRowsCap counts them. That is the same window the store-backed seam
// runs under, which reaches it by widening the filter before it sizes its probe
// row (internal/workapi.WithRowsBeforeThePage, then WithFetchOneExtra), so one
// request fires on both.
//
// With no cap the engine carries it — LIMIT n OFFSET k — except when there is
// no LIMIT to hang one on. MySQL has no OFFSET without a LIMIT, and the
// sentinel this used to render for that shape
// ("LIMIT 18446744073709551615 OFFSET k") makes the Dolt engine size a result
// buffer from limit+offset and answer with a recovered "makeslice: cap out of
// range" out of topRowsIter instead of rows. An unbounded query reads every
// matching row anyway, so skipping here is the same answer at the same cost.
//
// THE CALLERS THAT COMBINE AN OFFSET WITH A CAP are the ones that consume the
// FILTER as a value and run their own query: proxied `bd list --watch`, with
// and without --ready, and the proxied hierarchical --parent walk. They used to
// get the engine's OFFSET and a cap counted on the survivors, which answered a
// page for the very request `bd list --offset --max-rows` refuses one row of
// result set earlier. The role that pages — issueops.Reader — hands this seam
// no offset at all; it applies its own in the shared page epilogue for the
// reason internal/workapi.FinishPageAt gives. The unit-of-work Querier does set
// one here, and a query request carries no cap, so that is the shape still
// pushing the skip down.
type searchWindow struct {
	// sql is the LIMIT/OFFSET clause, empty for an unbounded scan.
	sql string
	// skip is the offset the clause did not carry.
	skip int
	// limit is the page the caller receives; 0 is unlimited.
	limit int
	// rowCap and capSource are the defensive cap and its attribution. rowCap is
	// SearchProbeLimit's cap, not the filter's: at touched == maxRows the probe
	// row moves it by one.
	rowCap    int
	capSource string
	// legLimit is the deepest row index the outer query can reach, and so the
	// bound each UNION leg may carry. 0 means the legs stay unbounded, either
	// because the search is unbounded or because its order is not one SQL can
	// express — see legWindowSQL.
	legLimit int
}

// searchWindowFor sizes one search's window. goSideSort reports a sort key SQL
// renders no ORDER BY for (sqlbuild.IsGoSideSort — "id", which needs the
// natural-numeric comparison that puts bd-9 before bd-10).
//
// A PAGE BOUND IS ONLY EVER PUSHED UNDER AN ORDER THE QUERY CAN EXPRESS, and
// that is the whole of what goSideSort changes here. A LIMIT with no ORDER BY
// does not return the first n rows; it returns n rows. Measured on a two-plane
// store with ids interleaved across the planes, `--sort id --limit 5` answered
// with five DURABLE rows and no wisp at all — a page whose order was right, and
// whose membership silently excluded an entire plane.
//
// So a Go-side sort takes the same window internal/workapi.SQLLimit gives the
// reader role for the same reason: none. The query returns the complete
// matching set, the comparator that can express the order runs over it, and
// FinishPageAt's cut is the only thing bounding the page — "this is where the
// requested order first exists", as its doc says. The CAP is not waived with
// it: maxRows still sizes a bound, so a Go-side sort over a set too large to
// order in memory reports ErrTooManyRows instead of scanning the workspace.
//
// The cost is stated rather than absorbed: a caller combining `--sort id` with
// a small limit and NO cap now reads every matching row where it used to read
// limit+1. It read the wrong ones, and the reader role has always paid this
// price on the same query.
func searchWindowFor(limit, offset, maxRows int, maxRowsSource string, goSideSort bool) searchWindow {
	// The window the cap is sized against is the one the query TOUCHES, so a
	// skip this package keeps has to be inside the bound. An unbounded limit
	// already carries every matching row and needs no widening — the same two
	// lines WithRowsBeforeThePage runs on the filter for the other seam.
	pageLimit := limit
	if goSideSort {
		pageLimit = 0
	}
	skipHere := maxRows > 0 && offset > 0
	touched := pageLimit
	if skipHere && pageLimit > 0 {
		touched += offset
	}
	bound, rowCap := issueops.SearchProbeLimit(touched, maxRows)
	w := searchWindow{limit: limit, rowCap: rowCap, capSource: maxRowsSource}
	switch {
	case bound <= 0:
		w.skip = offset
	case skipHere:
		w.skip, w.sql = offset, fmt.Sprintf("LIMIT %d", bound)
		w.legLimit = bound
	case offset > 0:
		w.sql = fmt.Sprintf("LIMIT %d OFFSET %d", bound, offset)
		w.legLimit = bound + offset
	default:
		w.sql = fmt.Sprintf("LIMIT %d", bound)
		w.legLimit = bound
	}
	if goSideSort {
		// The bound that survives above is the CAP's, not the page's, and a cap
		// is a defensive ceiling on an unordered scan rather than a top-n. It
		// stays on the outer query, where trimming it is the caller's error;
		// it must not become a leg's idea of which rows matter.
		w.legLimit = 0
	}
	return w
}

func searchWindowForFilter(filter types.IssueFilter) searchWindow {
	return searchWindowFor(filter.Limit, filter.Offset, filter.MaxRows, filter.MaxRowsSource, sqlbuild.IsGoSideSort(filter.SortBy))
}

// readyWindowForFilter sizes the ready query's window. types.WorkFilter carries
// no sort key — ready has one policy order, which SQL renders — so no Go-side
// sort is reachable here.
func readyWindowForFilter(filter types.WorkFilter) searchWindow {
	return searchWindowFor(filter.Limit, filter.Offset, filter.MaxRows, filter.MaxRowsSource, false)
}

// finishWindow is the Go half of a window: the defensive cap against what the
// query matched, then the offset the engine was not given, then the page trim
// that produces the has-more verdict. The cap runs first because it counts rows
// the query MATCHED, skipped ones included — the same count the store-backed
// seam checks, which skips nothing because its body does.
func finishWindow[T any](rows []T, w searchWindow) ([]T, bool, error) {
	if err := issueops.EnforceMaxRowsCap(len(rows), w.rowCap, w.capSource); err != nil {
		return nil, false, err
	}
	items, hasMore := applyN1Overflow(dropFirst(rows, w.skip), w.limit)
	return items, hasMore, nil
}

func dropFirst[T any](rows []T, n int) []T {
	if n <= 0 {
		return rows
	}
	if n >= len(rows) {
		return rows[:0]
	}
	return rows[n:]
}

func applyN1Overflow[T any](items []T, limit int) ([]T, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}
