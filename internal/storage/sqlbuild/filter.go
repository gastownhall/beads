package sqlbuild

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// KeysetCreatedAtIDPredicate is the SARGABLE (created_at DESC, id ASC) keyset
// predicate emitted for IssueFilter.AfterCreatedAt/AfterID. Its three ?
// placeholders bind, in order: created_at (the sargable upper bound), created_at
// (strict, drops the same-second rows already returned), and id (the same-second
// tie-break).
//
// It is logically "(created_at, id) is strictly after (cursor)" under
// created_at DESC, id ASC — i.e. (created_at < ?) OR (created_at = ? AND id > ?)
// — but rewritten with a redundant `created_at <= ?` leading bound so the planner
// seeks idx_issues_created_at (an IndexedTableAccess range on Dolt, an index/
// BitmapOr scan on Postgres) instead of full-scanning and filtering. The two
// forms select the same rows: created_at <= C is true whenever the OR is, and
// prunes only created_at > C, which the OR already excludes. It is exported so
// the backend sargability guards EXPLAIN this exact string rather than a copy —
// a change here then breaks the guard.
const KeysetCreatedAtIDPredicate = "(created_at <= ? AND ((created_at < ?) OR (id > ?)))"

// BuildIssueFilterClauses builds WHERE clause fragments and args from a query
// string and IssueFilter. The tables parameter controls which table names are
// referenced in subqueries (issues vs wisps).
func BuildIssueFilterClauses(query string, filter types.IssueFilter, tables FilterTables) ([]string, []any, error) {
	var whereClauses []string
	var args []any

	if query != "" {
		lowerQuery := strings.ToLower(query)
		if LooksLikeIssueID(query) {
			whereClauses = append(whereClauses, "(id = ? OR id LIKE ? OR LOWER(title) LIKE ? OR LOWER(external_ref) LIKE ?)")
			args = append(args, lowerQuery, lowerQuery+"%", "%"+lowerQuery+"%", "%"+lowerQuery+"%")
		} else {
			whereClauses = append(whereClauses, "(LOWER(title) LIKE ? OR id LIKE ?)")
			pattern := "%" + lowerQuery + "%"
			args = append(args, pattern, pattern)
		}
	}

	if filter.TitleSearch != "" {
		whereClauses = append(whereClauses, "LOWER(title) LIKE ?")
		args = append(args, "%"+strings.ToLower(filter.TitleSearch)+"%")
	}
	if filter.TitleContains != "" {
		whereClauses = append(whereClauses, "LOWER(title) LIKE ?")
		args = append(args, "%"+strings.ToLower(filter.TitleContains)+"%")
	}
	if filter.DescriptionContains != "" {
		whereClauses = append(whereClauses, "LOWER(description) LIKE ?")
		args = append(args, "%"+strings.ToLower(filter.DescriptionContains)+"%")
	}
	if filter.NotesContains != "" {
		whereClauses = append(whereClauses, "LOWER(notes) LIKE ?")
		args = append(args, "%"+strings.ToLower(filter.NotesContains)+"%")
	}
	if filter.ExternalRefContains != "" {
		whereClauses = append(whereClauses, "LOWER(external_ref) LIKE ?")
		args = append(args, "%"+strings.ToLower(filter.ExternalRefContains)+"%")
	}
	if filter.ExternalRef != nil {
		whereClauses = append(whereClauses, "external_ref = ?")
		args = append(args, *filter.ExternalRef)
	}

	if filter.Status != nil {
		whereClauses = append(whereClauses, "status = ?")
		args = append(args, *filter.Status)
	}
	if len(filter.Statuses) > 0 {
		placeholders := make([]string, len(filter.Statuses))
		for i, s := range filter.Statuses {
			placeholders[i] = "?"
			args = append(args, string(s))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("status IN (%s)", strings.Join(placeholders, ",")))
	}
	if len(filter.ExcludeStatus) > 0 {
		placeholders := make([]string, len(filter.ExcludeStatus))
		for i, s := range filter.ExcludeStatus {
			placeholders[i] = "?"
			args = append(args, string(s))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("status NOT IN (%s)", strings.Join(placeholders, ",")))
	}

	if filter.IssueType != nil {
		whereClauses = append(whereClauses, "issue_type = ?")
		args = append(args, *filter.IssueType)
	}
	if len(filter.ExcludeTypes) > 0 {
		placeholders := make([]string, len(filter.ExcludeTypes))
		for i, t := range filter.ExcludeTypes {
			placeholders[i] = "?"
			args = append(args, string(t))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("issue_type NOT IN (%s)", strings.Join(placeholders, ",")))
	}

	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}
	if filter.PriorityMin != nil {
		whereClauses = append(whereClauses, "priority >= ?")
		args = append(args, *filter.PriorityMin)
	}
	if filter.PriorityMax != nil {
		whereClauses = append(whereClauses, "priority <= ?")
		args = append(args, *filter.PriorityMax)
	}

	if len(filter.IDs) > 0 {
		placeholders := make([]string, len(filter.IDs))
		for i, id := range filter.IDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ", ")))
	}
	if filter.IDPrefix != "" {
		whereClauses = append(whereClauses, "id LIKE ?")
		args = append(args, filter.IDPrefix+"%")
	}
	if filter.SpecIDPrefix != "" {
		whereClauses = append(whereClauses, "spec_id LIKE ?")
		args = append(args, filter.SpecIDPrefix+"%")
	}

	if filter.ParentID != nil {
		parentID := *filter.ParentID
		whereClauses = append(whereClauses, fmt.Sprintf("(id IN (SELECT issue_id FROM %s WHERE type = 'parent-child' AND %s = ?) OR (id LIKE CONCAT(?, '.%%') AND id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child')))", tables.Dependencies, DepTargetExpr, tables.Dependencies))
		args = append(args, parentID, parentID)
	}
	if filter.NoParent {
		whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child')", tables.Dependencies))
	}

	if filter.MolType != nil {
		whereClauses = append(whereClauses, "mol_type = ?")
		args = append(args, string(*filter.MolType))
	}
	if filter.WispType != nil {
		whereClauses = append(whereClauses, "wisp_type = ?")
		args = append(args, string(*filter.WispType))
	}

	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label = ?)", tables.Labels))
			args = append(args, label)
		}
	}
	if len(filter.LabelsAny) > 0 {
		placeholders := make([]string, len(filter.LabelsAny))
		for i, label := range filter.LabelsAny {
			placeholders[i] = "?"
			args = append(args, label)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label IN (%s))", tables.Labels, strings.Join(placeholders, ", ")))
	}
	if len(filter.ExcludeLabels) > 0 {
		placeholders := make([]string, len(filter.ExcludeLabels))
		for i, label := range filter.ExcludeLabels {
			placeholders[i] = "?"
			args = append(args, label)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (SELECT issue_id FROM %s WHERE label IN (%s))", tables.Labels, strings.Join(placeholders, ", ")))
	}
	if filter.NoLabels {
		whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (SELECT DISTINCT issue_id FROM %s)", tables.Labels))
	}

	if filter.Pinned != nil {
		if *filter.Pinned {
			whereClauses = append(whereClauses, "pinned = 1")
		} else {
			whereClauses = append(whereClauses, "(pinned = 0 OR pinned IS NULL)")
		}
	}
	if filter.SourceRepo != nil {
		whereClauses = append(whereClauses, "source_repo = ?")
		args = append(args, *filter.SourceRepo)
	}
	if filter.Ephemeral != nil {
		if *filter.Ephemeral {
			whereClauses = append(whereClauses, "ephemeral = 1")
		} else {
			whereClauses = append(whereClauses, "(ephemeral = 0 OR ephemeral IS NULL)")
		}
	}
	if filter.IsTemplate != nil {
		if *filter.IsTemplate {
			whereClauses = append(whereClauses, "is_template = 1")
		} else {
			whereClauses = append(whereClauses, "(is_template = 0 OR is_template IS NULL)")
		}
	}
	if filter.IsBlocked != nil {
		// is_blocked is NOT NULL DEFAULT 0 on both issues and wisps, so a plain
		// equality is exact (no IS NULL arm needed) and index-backed by
		// idx_issues_is_blocked(is_blocked, status). Bound as an int so the same
		// clause is portable across every backend (Dolt/MySQL/SQLite native, and
		// pgdialect rewrites ? → $n).
		blocked := 0
		if *filter.IsBlocked {
			blocked = 1
		}
		whereClauses = append(whereClauses, "is_blocked = ?")
		args = append(args, blocked)
	}

	if filter.EmptyDescription {
		whereClauses = append(whereClauses, "(description IS NULL OR description = '')")
	}
	if filter.NoAssignee {
		whereClauses = append(whereClauses, "(assignee IS NULL OR assignee = '')")
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
		if tc.v != nil {
			whereClauses = append(whereClauses, fmt.Sprintf("%s %s ?", tc.col, tc.op))
			args = append(args, tc.v.Format(time.RFC3339))
		}
	}

	if filter.AfterCreatedAt != nil {
		// Bind the cursor time as time.Time, not a formatted string: the issues/
		// wisps created_at columns are DATETIME (NUMERIC affinity), so an RFC3339
		// string parameter mis-compares on the SQLite backend, while a time.Time
		// value compares correctly on every backend — the same binding EventsSince
		// uses. Bound twice (the sargable upper bound and the strict bound), then
		// the id tie-break.
		ac := *filter.AfterCreatedAt
		whereClauses = append(whereClauses, KeysetCreatedAtIDPredicate)
		args = append(args, ac, ac, filter.AfterID)
	}

	if filter.Deferred {
		whereClauses = append(whereClauses, "(defer_until IS NOT NULL OR status = ?)")
		args = append(args, types.StatusDeferred)
	}
	if filter.Overdue {
		whereClauses = append(whereClauses, "due_at IS NOT NULL AND due_at < ? AND status != ?")
		args = append(args, time.Now().UTC().Format(time.RFC3339), types.StatusClosed)
	}

	var err error
	whereClauses, args, err = AppendMetadataClauses(whereClauses, args, filter.HasMetadataKey, filter.MetadataFields)
	if err != nil {
		return nil, nil, err
	}

	return whereClauses, args, nil
}

// AppendMetadataClauses appends JSON metadata predicates (has-key and exact
// field matches, keys in sorted order) to an existing clause/arg list.
func AppendMetadataClauses(where []string, args []any, hasKey string, fields map[string]string) ([]string, []any, error) {
	if hasKey != "" {
		if err := storage.ValidateMetadataKey(hasKey); err != nil {
			return nil, nil, err
		}
		where = append(where, "JSON_EXTRACT(metadata, ?) IS NOT NULL")
		args = append(args, storage.JSONMetadataPath(hasKey))
	}
	if len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := storage.ValidateMetadataKey(k); err != nil {
				return nil, nil, err
			}
			where = append(where, "JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?")
			args = append(args, storage.JSONMetadataPath(k), fields[k])
		}
	}
	return where, args, nil
}

// LooksLikeIssueID returns true if the query string looks like a beads issue ID.
func LooksLikeIssueID(query string) bool {
	idx := strings.Index(query, "-")
	if idx <= 0 || idx >= len(query)-1 {
		return false
	}
	if strings.Contains(query, " ") {
		return false
	}
	for _, c := range query {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
