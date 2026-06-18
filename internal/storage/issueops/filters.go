package issueops

import (
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// FilterTables configures table names for BuildIssueFilterClauses,
// allowing the same filter logic to target both issues and wisps tables.
// The implementation lives in internal/storage/sqlbuild, shared with the
// domain/db stack (bd-6dnrw.46).
type FilterTables = sqlbuild.FilterTables

var (
	IssuesFilterTables = sqlbuild.IssuesFilterTables
	WispsFilterTables  = sqlbuild.WispsFilterTables
)

// BuildIssueFilterClauses builds WHERE clause fragments and args from a query
// string and IssueFilter. The tables parameter controls which table names are
// referenced in subqueries (issues vs wisps).
func BuildIssueFilterClauses(query string, filter types.IssueFilter, tables FilterTables) ([]string, []interface{}, error) {
	return sqlbuild.BuildIssueFilterClauses(query, filter, tables)
}

// LooksLikeIssueID returns true if the query string looks like a beads issue ID.
func LooksLikeIssueID(query string) bool {
	return sqlbuild.LooksLikeIssueID(query)
}
