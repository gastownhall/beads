package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

const queryBatchSize = 200

type filterTables struct {
	Main         string
	Labels       string
	Dependencies string
	Comments     string
}

var (
	issuesFilterTables = filterTables{Main: "issues", Labels: "labels", Dependencies: "dependencies", Comments: "comments"}
	wispsFilterTables  = filterTables{Main: "wisps", Labels: "wisp_labels", Dependencies: "wisp_dependencies", Comments: "wisp_comments"}
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
	whereClauses, args, err := buildIssueFilterClauses(query, plan.filter, tables)
	if err != nil {
		return "", nil, err
	}
	whereClauses, args = plan.mergeInto(whereClauses, args)
	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}
	selectKw := "SELECT"
	if plan.distinct {
		selectKw = "SELECT DISTINCT"
	}
	//nolint:gosec // G201: srcTag is a hardcoded 'i' or 'w'; fromSQL/whereSQL composed from fixed table names and ? placeholders.
	sub := fmt.Sprintf("%s id, '%s' AS src, %s FROM %s %s",
		selectKw, srcTag, unionSortColumnsSQL, plan.fromSQL, whereSQL)
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
	whereClauses, args, err := buildIssueFilterClauses(query, plan.filter, tables)
	if err != nil {
		return domain.SearchPage{}, err
	}
	whereClauses, args = plan.mergeInto(whereClauses, args)

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	selectKw := "SELECT "
	if plan.distinct {
		selectKw = "SELECT DISTINCT "
	}

	if filter.Limit > 0 && !filter.NoIDShrink {
		ids, hasMore, err := r.scanFilterIDs(ctx, selectKw, plan.fromSQL, whereSQL, args, filter, tables)
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
		selectKw, issueSelectColumns, plan.fromSQL, whereSQL, orderBy, limitSQL)

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

type labelSearchPlan struct {
	fromSQL  string
	where    []string
	args     []any
	distinct bool
	filter   types.IssueFilter
}

func (p labelSearchPlan) mergeInto(where []string, args []any) ([]string, []any) {
	if len(p.where) == 0 {
		return where, args
	}
	return append(p.where, where...), append(p.args, args...)
}

func buildLabelDrivenSearch(filter types.IssueFilter, tables filterTables) labelSearchPlan {
	labels := compactNonEmptyStrings(filter.Labels)
	labelsAny := compactNonEmptyStrings(filter.LabelsAny)
	if len(labels) == 0 && len(labelsAny) == 0 {
		return labelSearchPlan{fromSQL: tables.Main, filter: filter}
	}

	filterForClauses := filter
	filterForClauses.Labels = nil
	filterForClauses.LabelsAny = nil

	var joins, where []string
	var args []any

	for i, label := range labels {
		alias := fmt.Sprintf("label_filter_%d", i)
		joins = append(joins, fmt.Sprintf("JOIN %s %s ON %s.issue_id = %s.id", tables.Labels, alias, alias, tables.Main))
		where = append(where, fmt.Sprintf("%s.label = ?", alias))
		args = append(args, label)
	}

	if len(labelsAny) > 0 {
		alias := "label_filter_any"
		joins = append(joins, fmt.Sprintf("JOIN %s %s ON %s.issue_id = %s.id", tables.Labels, alias, alias, tables.Main))
		ph, anyArgs := buildInPlaceholders(labelsAny)
		where = append(where, fmt.Sprintf("%s.label IN (%s)", alias, ph))
		args = append(args, anyArgs...)
	}

	return labelSearchPlan{
		fromSQL:  tables.Main + " " + strings.Join(joins, " "),
		where:    where,
		args:     args,
		distinct: true,
		filter:   filterForClauses,
	}
}

func compactNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func buildIssueFilterClauses(query string, filter types.IssueFilter, tables filterTables) ([]string, []any, error) {
	var c clauseBuf

	if query != "" {
		lowerQuery := strings.ToLower(query)
		if looksLikeIssueID(query) {
			c.and("(id = ? OR id LIKE ? OR LOWER(title) LIKE ? OR LOWER(external_ref) LIKE ?)",
				lowerQuery, lowerQuery+"%", "%"+lowerQuery+"%", "%"+lowerQuery+"%")
		} else {
			pattern := "%" + lowerQuery + "%"
			c.and("(LOWER(title) LIKE ? OR id LIKE ?)", pattern, pattern)
		}
	}

	likeLowerContains(&c, "title", filter.TitleSearch)
	likeLowerContains(&c, "title", filter.TitleContains)
	likeLowerContains(&c, "description", filter.DescriptionContains)
	likeLowerContains(&c, "notes", filter.NotesContains)
	likeLowerContains(&c, "external_ref", filter.ExternalRefContains)

	eqStrPtr(&c, "status", filter.Status)
	inList(&c, "status", filter.Statuses)
	notInList(&c, "status", filter.ExcludeStatus)

	eqStrPtr(&c, "issue_type", filter.IssueType)
	notInList(&c, "issue_type", filter.ExcludeTypes)

	eqStrPtr(&c, "assignee", filter.Assignee)

	eqIntPtr(&c, "priority", filter.Priority)
	if filter.PriorityMin != nil {
		c.and("priority >= ?", *filter.PriorityMin)
	}
	if filter.PriorityMax != nil {
		c.and("priority <= ?", *filter.PriorityMax)
	}

	inList(&c, "id", filter.IDs)
	if filter.IDPrefix != "" {
		c.and("id LIKE ?", filter.IDPrefix+"%")
	}
	if filter.SpecIDPrefix != "" {
		c.and("spec_id LIKE ?", filter.SpecIDPrefix+"%")
	}

	if filter.ParentID != nil {
		parentID := *filter.ParentID
		c.and(fmt.Sprintf("(id IN (SELECT issue_id FROM %s WHERE type = 'parent-child' AND %s = ?) OR (id LIKE CONCAT(?, '.%%') AND id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child')))",
			tables.Dependencies, depTargetExpr, tables.Dependencies),
			parentID, parentID)
	}
	if filter.NoParent {
		c.and(fmt.Sprintf("id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child')", tables.Dependencies))
	}

	eqStrPtr(&c, "mol_type", filter.MolType)
	eqStrPtr(&c, "wisp_type", filter.WispType)

	for _, label := range filter.Labels {
		c.and(fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label = ?)", tables.Labels), label)
	}
	if len(filter.LabelsAny) > 0 {
		ph, a := buildInPlaceholders(filter.LabelsAny)
		c.and(fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label IN (%s))", tables.Labels, ph), a...)
	}
	if len(filter.ExcludeLabels) > 0 {
		ph, a := buildInPlaceholders(filter.ExcludeLabels)
		c.and(fmt.Sprintf("id NOT IN (SELECT issue_id FROM %s WHERE label IN (%s))", tables.Labels, ph), a...)
	}
	if filter.NoLabels {
		c.and(fmt.Sprintf("id NOT IN (SELECT DISTINCT issue_id FROM %s)", tables.Labels))
	}

	boolFlag(&c, "pinned", filter.Pinned)
	eqStrPtr(&c, "source_repo", filter.SourceRepo)
	boolFlag(&c, "ephemeral", filter.Ephemeral)
	boolFlag(&c, "is_template", filter.IsTemplate)

	if filter.EmptyDescription {
		nullOrEmpty(&c, "description")
	}
	if filter.NoAssignee {
		nullOrEmpty(&c, "assignee")
	}

	for _, tc := range []struct {
		col, op string
		v       *time.Time
	}{
		{"created_at", ">", filter.CreatedAfter},
		{"created_at", "<", filter.CreatedBefore},
		{"updated_at", ">", filter.UpdatedAfter},
		{"updated_at", "<", filter.UpdatedBefore},
		{"closed_at", ">", filter.ClosedAfter},
		{"closed_at", "<", filter.ClosedBefore},
		{"started_at", ">", filter.StartedAfter},
		{"started_at", "<", filter.StartedBefore},
		{"defer_until", ">", filter.DeferAfter},
		{"defer_until", "<", filter.DeferBefore},
		{"due_at", ">", filter.DueAfter},
		{"due_at", "<", filter.DueBefore},
	} {
		timeOp(&c, tc.col, tc.op, tc.v)
	}

	if filter.Deferred {
		c.and("(defer_until IS NOT NULL OR status = ?)", types.StatusDeferred)
	}
	if filter.Overdue {
		c.and("due_at IS NOT NULL AND due_at < ? AND status != ?",
			time.Now().UTC().Format(time.RFC3339), types.StatusClosed)
	}

	if err := c.metadata(filter.HasMetadataKey, filter.MetadataFields); err != nil {
		return nil, nil, err
	}
	return c.where, c.args, nil
}

func looksLikeIssueID(query string) bool {
	idx := strings.Index(query, "-")
	if idx <= 0 || idx >= len(query)-1 {
		return false
	}
	return strings.IndexFunc(query, func(c rune) bool {
		switch {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c == '-', c == '.':
			return false
		default:
			return true
		}
	}) == -1
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

type sortDef struct {
	column     string
	defaultDir string
}

var sortDefs = map[string]sortDef{
	"":         {"priority", "ASC"},
	"priority": {"priority", "ASC"},
	"created":  {"created_at", "DESC"},
	"updated":  {"updated_at", "DESC"},
	"closed":   {"closed_at", "DESC"},
	"status":   {"status", "ASC"},
	"type":     {"issue_type", "ASC"},
	"assignee": {"assignee", "ASC"},
	"title":    {"title", "ASC"},
}

const unionSortColumnsSQL = `priority AS sort_priority,
	created_at AS sort_created,
	updated_at AS sort_updated,
	closed_at AS sort_closed,
	status AS sort_status,
	issue_type AS sort_type,
	assignee AS sort_assignee,
	LOWER(title) AS sort_title`

func isGoSideSort(sortBy string) bool {
	return sortBy == "id"
}

func flipDir(dir string) string {
	if dir == "ASC" {
		return "DESC"
	}
	return "ASC"
}

func orderBySQLForColumns(sortBy string, sortDesc bool, col func(sortKey string) string) string {
	if isGoSideSort(sortBy) {
		return ""
	}
	def, ok := sortDefs[sortBy]
	if !ok {
		def = sortDefs[""]
		sortBy = ""
	}
	dir := def.defaultDir
	if sortDesc {
		dir = flipDir(dir)
	}
	if sortBy == "" || sortBy == "priority" {
		return fmt.Sprintf("ORDER BY %s %s, %s DESC, %s ASC", col("priority"), dir, col("created"), col("id"))
	}
	return fmt.Sprintf("ORDER BY %s %s, %s ASC", col(sortBy), dir, col("id"))
}

func unionOrderBySQL(sortBy string, sortDesc bool) string {
	return orderBySQLForColumns(sortBy, sortDesc, func(k string) string {
		if k == "id" {
			return "id"
		}
		return "sort_" + k
	})
}

func orderBySQL(sortBy string, sortDesc bool, prefix string) string {
	qual := ""
	if prefix != "" {
		qual = prefix + "."
	}
	return orderBySQLForColumns(sortBy, sortDesc, func(k string) string {
		switch k {
		case "id":
			return qual + "id"
		case "title":
			return "LOWER(" + qual + "title)"
		}
		return qual + sortDefs[k].column
	})
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
