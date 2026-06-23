package sqlbuild

import (
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// SortDef maps a user-facing sort key to its column and default direction.
type SortDef struct {
	Column     string
	DefaultDir string
}

// SortDefs is the canonical sort-key table for issue list/search ordering.
var SortDefs = map[string]SortDef{
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

// UnionSortColumnsSQL projects every sortable column under a stable sort_*
// alias so a UNION ALL outer query can ORDER BY any sort key.
const UnionSortColumnsSQL = `priority AS sort_priority,
	created_at AS sort_created,
	updated_at AS sort_updated,
	closed_at AS sort_closed,
	status AS sort_status,
	issue_type AS sort_type,
	assignee AS sort_assignee,
	LOWER(title) AS sort_title`

// IsGoSideSort reports sort keys that are applied in Go after the query
// instead of in SQL.
func IsGoSideSort(sortBy string) bool {
	return sortBy == "id"
}

func flipDir(dir string) string {
	if dir == "ASC" {
		return "DESC"
	}
	return "ASC"
}

// OrderByForColumns renders the ORDER BY clause for a sort key, mapping sort
// keys to column expressions via col. Used directly by UNION consumers whose
// columns are aliased; per-table callers should use OrderBy.
func OrderByForColumns(sortBy string, sortDesc bool, col func(sortKey string) string) string {
	if IsGoSideSort(sortBy) {
		return ""
	}
	def, ok := SortDefs[sortBy]
	if !ok {
		def = SortDefs[""]
		sortBy = ""
	}
	dir := def.DefaultDir
	if sortDesc {
		dir = flipDir(dir)
	}
	if sortBy == "" || sortBy == "priority" {
		return fmt.Sprintf("ORDER BY %s %s, %s DESC, %s ASC", col("priority"), dir, col("created"), col("id"))
	}
	return fmt.Sprintf("ORDER BY %s %s, %s ASC", col(sortBy), dir, col("id"))
}

// OrderBy renders the ORDER BY clause against real table columns, optionally
// qualified ("i" -> "i.priority"). Ties always break by id ASC; the default
// priority sort additionally breaks by created_at DESC.
func OrderBy(sortBy string, sortDesc bool, table string) string {
	qual := ""
	if table != "" {
		qual = table + "."
	}
	return OrderByForColumns(sortBy, sortDesc, func(k string) string {
		switch k {
		case "id":
			return qual + "id"
		case "title":
			return "LOWER(" + qual + "title)"
		}
		return qual + SortDefs[k].Column
	})
}

// Less is the Go-side mirror of OrderBy for merge sorts over rows fetched
// from separate queries (issues + wisps). It must order exactly the way the
// SQL does, including MySQL NULL-first semantics for nullable columns;
// otherwise a post-merge limit cut keeps a different row set than SQL
// selected.
func Less(a, b *types.Issue, sortBy string, sortDesc bool) bool {
	if sortBy == "id" {
		return a.ID < b.ID
	}
	def, ok := SortDefs[sortBy]
	if !ok {
		def = SortDefs[""]
		sortBy = ""
	}
	descending := def.DefaultDir == "DESC"
	if sortDesc {
		descending = !descending
	}
	if c := sortKeyCompare(a, b, sortBy); c != 0 {
		if descending {
			return c > 0
		}
		return c < 0
	}
	if (sortBy == "" || sortBy == "priority") && !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID < b.ID
}

// sortKeyCompare three-way compares the primary sort column in ascending
// order, with MySQL NULL-first semantics for nullable columns.
func sortKeyCompare(a, b *types.Issue, sortBy string) int {
	switch sortBy {
	case "created":
		return compareTimesAsc(a.CreatedAt, b.CreatedAt)
	case "updated":
		return compareTimesAsc(a.UpdatedAt, b.UpdatedAt)
	case "closed":
		switch {
		case a.ClosedAt == nil && b.ClosedAt == nil:
			return 0
		case a.ClosedAt == nil:
			return -1
		case b.ClosedAt == nil:
			return 1
		}
		return compareTimesAsc(*a.ClosedAt, *b.ClosedAt)
	case "status":
		return strings.Compare(string(a.Status), string(b.Status))
	case "type":
		return strings.Compare(string(a.IssueType), string(b.IssueType))
	case "assignee":
		return strings.Compare(a.Assignee, b.Assignee)
	case "title":
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	}
	return a.Priority - b.Priority
}

func compareTimesAsc(a, b time.Time) int {
	switch {
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	}
	return 0
}
