package dolt

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gastownhall/beads/internal/storage"
	"github.com/gastownhall/beads/internal/types"
)

// filterTables configures table names for buildIssueFilterClauses,
// allowing the same filter logic to target both issues and wisps tables.
type filterTables struct {
	main         string // "issues" or "wisps"
	labels       string // "labels" or "wisp_labels"
	dependencies string // "dependencies" or "wisp_dependencies"
}

var (
	issuesFilterTables = filterTables{main: "issues", labels: "labels", dependencies: "dependencies"}
	wispsFilterTables  = filterTables{main: "wisps", labels: "wisp_labels", dependencies: "wisp_dependencies"}
)

// buildIssueFilterClauses builds WHERE clause fragments and args from a query
// string and IssueFilter. The tables parameter controls which table names are
// referenced in subqueries (issues vs wisps).
func buildIssueFilterClauses(query string, filter types.IssueFilter, tables filterTables) ([]string, []interface{}, error) {
	var whereClauses []string
	var args []interface{}

	// Free-text search — optimized to avoid full-table scans (hq-319).
	//
	// The old approach used (title LIKE %q% OR description LIKE %q% OR id LIKE %q%)
	// which forces Dolt to scan every row in the prolly tree for all three columns.
	// With 12K+ issues and concurrent agents, this caused CPU spikes of 200-400%.
	//
	// Optimization: detect ID-like queries and use exact/prefix match on the indexed
	// id column. For text queries, search title and id only (description is large and
	// rarely matches short queries — use --desc-contains for explicit description search).
	if query != "" {
		lowerQuery := strings.ToLower(query)
		if looksLikeIssueID(query) {
			// ID-like query: use exact match + prefix match on indexed id column,
			// plus title LIKE and external_ref LIKE as fallbacks (e.g. Linear IDs
			// like "BE-1521" stored in external_ref).
			// IDs are always lowercase, so no LOWER() needed for id columns.
			whereClauses = append(whereClauses, "(id = ? OR id LIKE ? OR LOWER(title) LIKE ? OR LOWER(external_ref) LIKE ?)")
			args = append(args, lowerQuery, lowerQuery+"%", "%"+lowerQuery+"%", "%"+lowerQuery+"%")
		} else {
			// Text query: search title and id (skip description — it's large and
			// LIKE %% on TEXT columns causes the worst prolly-tree scan behavior).
			// Users can use --desc-contains for explicit description search.
			// LOWER() ensures case-insensitive matching (Dolt uses binary collation by default).
			whereClauses = append(whereClauses, "(LOWER(title) LIKE ? OR id LIKE ?)")
			pattern := "%" + lowerQuery + "%"
			args = append(args, pattern, pattern)
		}
	}

	if filter.TitleSearch != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		args = append(args, "%"+filter.TitleSearch+"%")
	}
	if filter.TitleContains != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		args = append(args, "%"+filter.TitleContains+"%")
	}
	if filter.DescriptionContains != "" {
		whereClauses = append(whereClauses, "description LIKE ?")
		args = append(args, "%"+filter.DescriptionContains+"%")
	}
	if filter.NotesContains != "" {
		whereClauses = append(whereClauses, "notes LIKE ?")
		args = append(args, "%"+filter.NotesContains+"%")
	}
	if filter.ExternalRefContains != "" {
		whereClauses = append(whereClauses, "external_ref LIKE ?")
		args = append(args, "%"+filter.ExternalRefContains+"%")
	}

	// Status filters
	if filter.Status != nil {
		whereClauses = append(whereClauses, "status = ?")
		args = append(args, *filter.Status)
	}
	if len(filter.ExcludeStatus) > 0 {
		placeholders := make([]string, len(filter.ExcludeStatus))
		for i, s := range filter.ExcludeStatus {
			placeholders[i] = "?"
			args = append(args, string(s))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("status NOT IN (%s)", strings.Join(placeholders, ",")))
	}

	// Use subquery for type filter to prevent Dolt mergeJoinIter panic.
	// When issue_type equality is combined with other indexed predicates (status, priority)
	// in the same WHERE clause, Dolt's query optimizer may select a merge join plan
	// between index scans that panics in mergeJoinIter. Isolating the type predicate
	// in a subquery forces sequential evaluation and avoids the problematic plan.
	if filter.IssueType != nil {
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT id FROM %s WHERE issue_type = ?)", tables.main))
		args = append(args, *filter.IssueType)
	}
	// Use subquery for type exclusion to prevent Dolt mergeJoinIter panic (same as above).
	if len(filter.ExcludeTypes) > 0 {
		placeholders := make([]string, len(filter.ExcludeTypes))
		for i, t := range filter.ExcludeTypes {
			placeholders[i] = "?"
			args = append(args, string(t))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT id FROM %s WHERE issue_type NOT IN (%s))", tables.main, strings.Join(placeholders, ",")))
	}

	// Assignee
	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}

	// Priority filters
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

	// ID filters
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

	// Parent/child dependency filters
	if filter.ParentID != nil {
		parentID := *filter.ParentID
		whereClauses = append(whereClauses, fmt.Sprintf("(id IN (SELECT issue_id FROM %s WHERE type = 'parent-child' AND depends_on_id = ?) OR (id LIKE CONCAT(?, '.%%') AND id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child')))", tables.dependencies, tables.dependencies))
		args = append(args, parentID, parentID)
	}
	if filter.NoParent {
		whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child')", tables.dependencies))
	}

	// Type classification filters
	if filter.MolType != nil {
		whereClauses = append(whereClauses, "mol_type = ?")
		args = append(args, string(*filter.MolType))
	}
	if filter.WispType != nil {
		whereClauses = append(whereClauses, "wisp_type = ?")
		args = append(args, string(*filter.WispType))
	}

	// Label filtering (AND — all labels must be present)
	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label = ?)", tables.labels))
			args = append(args, label)
		}
	}
	// Label filtering (OR — any label matches)
	if len(filter.LabelsAny) > 0 {
		placeholders := make([]string, len(filter.LabelsAny))
		for i, label := range filter.LabelsAny {
			placeholders[i] = "?"
			args = append(args, label)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label IN (%s))", tables.labels, strings.Join(placeholders, ", ")))
	}
	if filter.NoLabels {
		whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (SELECT DISTINCT issue_id FROM %s)", tables.labels))
	}

	// Boolean/flag filters
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

	// Empty/null checks
	if filter.EmptyDescription {
		whereClauses = append(whereClauses, "(description IS NULL OR description = '')")
	}
	if filter.NoAssignee {
		whereClauses = append(whereClauses, "(assignee IS NULL OR assignee = '')")
	}

	// Date range filters
	if filter.CreatedAfter != nil {
		whereClauses = append(whereClauses, "created_at > ?")
		args = append(args, filter.CreatedAfter.Format(time.RFC3339))
	}
	if filter.CreatedBefore != nil {
		whereClauses = append(whereClauses, "created_at < ?")
		args = append(args, filter.CreatedBefore.Format(time.RFC3339))
	}
	if filter.UpdatedAfter != nil {
		whereClauses = append(whereClauses, "updated_at > ?")
		args = append(args, filter.UpdatedAfter.Format(time.RFC3339))
	}
	if filter.UpdatedBefore != nil {
		whereClauses = append(whereClauses, "updated_at < ?")
		args = append(args, filter.UpdatedBefore.Format(time.RFC3339))
	}
	if filter.ClosedAfter != nil {
		whereClauses = append(whereClauses, "closed_at > ?")
		args = append(args, filter.ClosedAfter.Format(time.RFC3339))
	}
	if filter.ClosedBefore != nil {
		whereClauses = append(whereClauses, "closed_at < ?")
		args = append(args, filter.ClosedBefore.Format(time.RFC3339))
	}
	if filter.DeferAfter != nil {
		whereClauses = append(whereClauses, "defer_until > ?")
		args = append(args, filter.DeferAfter.Format(time.RFC3339))
	}
	if filter.DeferBefore != nil {
		whereClauses = append(whereClauses, "defer_until < ?")
		args = append(args, filter.DeferBefore.Format(time.RFC3339))
	}
	if filter.DueAfter != nil {
		whereClauses = append(whereClauses, "due_at > ?")
		args = append(args, filter.DueAfter.Format(time.RFC3339))
	}
	if filter.DueBefore != nil {
		whereClauses = append(whereClauses, "due_at < ?")
		args = append(args, filter.DueBefore.Format(time.RFC3339))
	}

	// Time-based scheduling filters
	if filter.Deferred {
		whereClauses = append(whereClauses, "defer_until IS NOT NULL")
	}
	if filter.Overdue {
		whereClauses = append(whereClauses, "due_at IS NOT NULL AND due_at < ? AND status != ?")
		args = append(args, time.Now().UTC().Format(time.RFC3339), types.StatusClosed)
	}

	// Metadata filters
	if filter.HasMetadataKey != "" {
		if err := storage.ValidateMetadataKey(filter.HasMetadataKey); err != nil {
			return nil, nil, err
		}
		whereClauses = append(whereClauses, "JSON_EXTRACT(metadata, ?) IS NOT NULL")
		args = append(args, "$."+filter.HasMetadataKey)
	}
	if len(filter.MetadataFields) > 0 {
		metaKeys := make([]string, 0, len(filter.MetadataFields))
		for k := range filter.MetadataFields {
			metaKeys = append(metaKeys, k)
		}
		sort.Strings(metaKeys)
		for _, k := range metaKeys {
			if err := storage.ValidateMetadataKey(k); err != nil {
				return nil, nil, err
			}
			whereClauses = append(whereClauses, "JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?")
			args = append(args, "$."+k, filter.MetadataFields[k])
		}
	}

	return whereClauses, args, nil
}

// looksLikeIssueID returns true if the query string looks like a beads issue ID
// (e.g., "bd-123", "hq-319", "bd-wisp-abc"). Issue IDs have the pattern:
// prefix-suffix where prefix is 1+ alphanumeric/hyphen segments and suffix is
// alphanumeric (hash or numeric). This is used to optimize search by routing
// ID-like queries to exact/prefix match instead of LIKE %%.
func looksLikeIssueID(query string) bool {
	// Must contain at least one hyphen
	idx := strings.Index(query, "-")
	if idx <= 0 || idx >= len(query)-1 {
		return false
	}
	// Must not contain spaces (IDs never have spaces)
	if strings.Contains(query, " ") {
		return false
	}
	// All characters must be alphanumeric, hyphen, or dot (for child IDs like "bd-123.1")
	for _, c := range query {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
