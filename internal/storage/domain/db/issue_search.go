package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
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

func (r *issueSQLRepositoryImpl) searchAcrossIssuesAndWisps(ctx context.Context, query string, filter types.IssueFilter) (domain.SearchPage, error) {
	if filter.Ephemeral != nil && *filter.Ephemeral {
		page, err := r.searchTable(ctx, query, filter, wispsFilterTables)
		if err != nil && !dberrors.IsTableNotExist(err) {
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
	iSub, iArgs, err := r.buildUnionSubquery(query, filter, issuesFilterTables, "i")
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("search union (issues): %w", err)
	}
	wSub, wArgs, err := r.buildUnionSubquery(query, filter, wispsFilterTables, "w")
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("search union (wisps): %w", err)
	}

	outerOrderBy := unionOrderBySQL(filter.SortBy, filter.SortDesc)
	outerLimit := limitOffsetSQL(filter.Limit, filter.Offset)

	//nolint:gosec // G201: subqueries built from hardcoded table names and ? placeholders.
	unionSQL := fmt.Sprintf("SELECT id, src FROM (%s UNION ALL %s) merged %s %s",
		iSub, wSub, outerOrderBy, outerLimit)

	args := make([]any, 0, len(iArgs)+len(wArgs))
	args = append(args, iArgs...)
	args = append(args, wArgs...)

	rows, err := r.runner.QueryContext(ctx, unionSQL, args...)
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("search union: %w", err)
	}
	page, err := scanIDSrcPage(rows, true)
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("search union: %w", err)
	}
	hasMore := page.trimToLimit(filter.Limit)

	issuesByID, err := r.fetchIssuesByIDs(ctx, page.issueIDs, issuesFilterTables, filter)
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("search union (hydrate issues): %w", err)
	}
	wispsByID, err := r.fetchIssuesByIDs(ctx, page.wispIDs, wispsFilterTables, filter)
	if err != nil && !dberrors.IsTableNotExist(err) {
		return domain.SearchPage{}, fmt.Errorf("search union (hydrate wisps): %w", err)
	}

	out := reassembleBySrc(page.ordered, issuesByID, wispsByID)
	return domain.SearchPage{Items: out, HasMore: hasMore}, nil
}

func (r *issueSQLRepositoryImpl) buildUnionSubquery(query string, filter types.IssueFilter, tables filterTables, srcTag string) (string, []any, error) {
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
	//nolint:gosec // G201: srcTag is a hardcoded 'i' or 'w'; fromSQL/whereSQL composed from fixed table names and ? placeholders.
	sub := fmt.Sprintf("%s id, '%s' AS src, %s FROM %s %s",
		selectKw, srcTag, unionSortColumnsSQL, plan.FromSQL, whereSQL)
	return sub, args, nil
}

func (r *issueSQLRepositoryImpl) fetchIssuesByIDs(ctx context.Context, ids []string, tables filterTables, filter types.IssueFilter) (map[string]*types.Issue, error) {
	if len(ids) == 0 {
		return map[string]*types.Issue{}, nil
	}

	placeholders, args := buildInPlaceholders(ids)

	//nolint:gosec // G201: tables.Main is "issues" or "wisps"; placeholders are ?.
	fetchSQL := fmt.Sprintf(`SELECT %s FROM %s WHERE id IN (%s)`,
		issueSelectColumns, tables.Main, placeholders)
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
	limitSQL := limitOffsetSQL(filter.Limit, filter.Offset)

	//nolint:gosec // G201: SQL fragments from fixed table names and parameterized filters.
	querySQL := fmt.Sprintf(`%s%s FROM %s %s %s %s`,
		selectKw, issueSelectColumns, plan.FromSQL, whereSQL, orderBy, limitSQL)

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

	items, hasMore := applyN1Overflow(issues, filter.Limit)

	if err := r.hydrateIssues(ctx, items, tables, filter.IncludeDependencies, filter.SkipLabels); err != nil {
		return domain.SearchPage{}, fmt.Errorf("search %s: hydrate: %w", tables.Main, err)
	}

	return domain.SearchPage{Items: items, HasMore: hasMore}, nil
}

func (r *issueSQLRepositoryImpl) scanFilterIDs(ctx context.Context, selectKw, fromSQL, whereSQL string, args []any, filter types.IssueFilter, tables filterTables) ([]string, bool, error) {
	orderBy := orderBySQL(filter.SortBy, filter.SortDesc, tables.Main)
	limitSQL := limitOffsetSQL(filter.Limit, filter.Offset)
	//nolint:gosec // G201: SQL fragments from fixed table names and parameterized filters.
	idQuery := fmt.Sprintf(`%s%s.id FROM %s %s %s %s`,
		selectKw, tables.Main, fromSQL, whereSQL, orderBy, limitSQL)

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

	ids, hasMore := applyN1Overflow(ids, filter.Limit)
	return ids, hasMore, nil
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

func scanIDSrcPage(rows *sql.Rows, strictCrossTable bool) (idSrcPage, error) {
	defer func() { _ = rows.Close() }()

	var page idSrcPage
	seen := make(map[string]string)
	for rows.Next() {
		var id, src string
		if err := rows.Scan(&id, &src); err != nil {
			return idSrcPage{}, fmt.Errorf("scan: %w", err)
		}
		if prev, dup := seen[id]; dup {
			if strictCrossTable && prev != src {
				return idSrcPage{}, fmt.Errorf("id %q exists in both issues and wisps", id)
			}
			continue
		}
		seen[id] = src
		page.ordered = append(page.ordered, idSrcRef{id: id, src: src})
		switch src {
		case "i":
			page.issueIDs = append(page.issueIDs, id)
		case "w":
			page.wispIDs = append(page.wispIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return idSrcPage{}, fmt.Errorf("rows: %w", err)
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

func (p *idSrcPage) trimToLimit(limit int) bool {
	if limit <= 0 || len(p.ordered) <= limit {
		return false
	}
	p.ordered = p.ordered[:limit]
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
	return true
}

type idSrcRef struct{ id, src string }

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

func limitOffsetSQL(limit, offset int) string {
	if limit <= 0 {
		if offset > 0 {
			return fmt.Sprintf("LIMIT 18446744073709551615 OFFSET %d", offset)
		}
		return ""
	}
	if offset > 0 {
		return fmt.Sprintf("LIMIT %d OFFSET %d", limit+1, offset)
	}
	return fmt.Sprintf("LIMIT %d", limit+1)
}

func applyN1Overflow[T any](items []T, limit int) ([]T, bool) {
	if limit <= 0 || len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}
