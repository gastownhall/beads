package issueops

import (
	"strings"

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

// globToLikePattern converts a shell-style glob (* and ?) to a SQL LIKE
// pattern. Literal % and _ in the input — and the '|' escape char itself —
// are escaped so they don't act as LIKE wildcards. The resulting SQL must
// use ESCAPE '|'.
func globToLikePattern(pattern string) string {
	var b strings.Builder
	b.Grow(len(pattern))
	for _, c := range pattern {
		switch c {
		case '%', '_', '|':
			b.WriteByte('|')
			b.WriteRune(c)
		case '*':
			b.WriteByte('%')
		case '?':
			b.WriteByte('_')
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

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
