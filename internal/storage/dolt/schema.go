package dolt

import (
	"strings"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// BuildReadyIssuesView delegates to the shared schema package.
func BuildReadyIssuesView() string {
	return schema.BuildReadyIssuesView()
}

// BuildBlockedIssuesView delegates to the shared schema package.
func BuildBlockedIssuesView() string {
	return schema.BuildBlockedIssuesView()
}

// escapeSQL escapes a string for safe inclusion in SQL string literals.
// Retained for test helpers.
func escapeSQL(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
