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

// LessID orders two ids for the Go-side "id" sort key: byte order — the order
// SQL would apply — flipped by desc. This is THE membership comparator for a
// Go-side id sort; every seam (Less, the union page's sortGoSide, the
// per-table sortRowsGoSide, the store-backed goSideSortAndTrim) calls it
// rather than restating the comparison, because the sortDesc bug it fixes
// came from exactly such a restated copy drifting from its siblings. The
// natural-numeric order a human reads (bd-9 before bd-10) is display-layer
// (workapi.CompareIssuesBy), applied to the delivered page — never here.
func LessID(a, b string, desc bool) bool {
	if desc {
		return a > b
	}
	return a < b
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
	// A nullable sort column (closed_at, assignee) treats NULL as lowest: first on ASC
	// and last on DESC. Lead with an explicit (col IS NULL) key so the contract does not
	// depend on a driver's default NULL ordering. NOT NULL columns keep the plain clause.
	if sortBy == "closed" || sortBy == "assignee" {
		return fmt.Sprintf("ORDER BY (%s IS NULL) %s, %s %s, %s ASC", col(sortBy), flipDir(dir), col(sortBy), dir, col("id"))
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

// sortFields holds exactly the columns Less/LessSummary compare. Sharing one
// comparator core (lessFields/sortKeyCompareFields) between types.Issue and
// types.IssueSummary means the correctness-critical NULL-first and tie-break
// rules are written once instead of drifting across two hand-copied switch
// statements.
type sortFields struct {
	id        string
	priority  int
	createdAt time.Time
	updatedAt time.Time
	closedAt  *time.Time
	status    string
	issueType string
	assignee  string
	title     string
}

func issueSortFields(i *types.Issue) sortFields {
	return sortFields{
		id:        i.ID,
		priority:  i.Priority,
		createdAt: i.CreatedAt,
		updatedAt: i.UpdatedAt,
		closedAt:  i.ClosedAt,
		status:    string(i.Status),
		issueType: string(i.IssueType),
		assignee:  i.Assignee,
		title:     i.Title,
	}
}

func summarySortFields(s *types.IssueSummary) sortFields {
	return sortFields{
		id:        s.ID,
		priority:  s.Priority,
		createdAt: s.CreatedAt,
		updatedAt: s.UpdatedAt,
		closedAt:  s.ClosedAt,
		status:    string(s.Status),
		issueType: string(s.IssueType),
		assignee:  s.Assignee,
		title:     s.Title,
	}
}

// Less is the Go-side mirror of OrderBy for merge sorts over rows fetched
// from separate queries (issues + wisps). It must order exactly the way the
// SQL does, including NULL-first ascending semantics for nullable columns;
// otherwise a post-merge limit cut keeps a different row set than SQL
// selected.
func Less(a, b *types.Issue, sortBy string, sortDesc bool) bool {
	return lessFields(issueSortFields(a), issueSortFields(b), sortBy, sortDesc)
}

// LessSummary is Less's sibling for types.IssueSummary, used by the
// summaryProjection merge sort (internal/storage/issueops.searchInTx). It
// shares lessFields with Less so the two can never drift on the NULL/tie-break
// rules that make a post-merge limit cut safe.
func LessSummary(a, b *types.IssueSummary, sortBy string, sortDesc bool) bool {
	return lessFields(summarySortFields(a), summarySortFields(b), sortBy, sortDesc)
}

func lessFields(a, b sortFields, sortBy string, sortDesc bool) bool {
	if sortBy == "id" {
		// This key used to ignore sortDesc, so a reversed id merge kept the
		// byte-FIRST rows (the sibling bug idSrcPage.sortGoSide's doc named).
		// Living in the shared body means LessSummary honors sortDesc too,
		// rather than needing its own copy of the same fix.
		return LessID(a.id, b.id, sortDesc)
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
	if c := sortKeyCompareFields(a, b, sortBy); c != 0 {
		if descending {
			return c > 0
		}
		return c < 0
	}
	if (sortBy == "" || sortBy == "priority") && !a.createdAt.Equal(b.createdAt) {
		return a.createdAt.After(b.createdAt)
	}
	return a.id < b.id
}

// sortKeyCompareFields three-way compares the primary sort column in
// ascending order, with MySQL NULL-first semantics for nullable columns.
func sortKeyCompareFields(a, b sortFields, sortBy string) int {
	switch sortBy {
	case "created":
		return compareTimesAsc(a.createdAt, b.createdAt)
	case "updated":
		return compareTimesAsc(a.updatedAt, b.updatedAt)
	case "closed":
		switch {
		case a.closedAt == nil && b.closedAt == nil:
			return 0
		case a.closedAt == nil:
			return -1
		case b.closedAt == nil:
			return 1
		}
		return compareTimesAsc(*a.closedAt, *b.closedAt)
	case "status":
		return strings.Compare(a.status, b.status)
	case "type":
		return strings.Compare(a.issueType, b.issueType)
	case "assignee":
		return strings.Compare(a.assignee, b.assignee)
	case "title":
		return strings.Compare(strings.ToLower(a.title), strings.ToLower(b.title))
	}
	return a.priority - b.priority
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
