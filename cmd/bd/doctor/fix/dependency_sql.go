package fix

import (
	"database/sql"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// Keep in sync with issueops.DepTargetExpr.
const fixDependencyTargetExpr = "COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)"

func fixDependencyUnionSQL() string {
	return `
		SELECT 'dependencies' AS dep_table, issue_id, ` + fixDependencyTargetExpr + ` AS depends_on_id, type
		FROM dependencies
		UNION ALL
		SELECT 'wisp_dependencies' AS dep_table, issue_id, ` + fixDependencyTargetExpr + ` AS depends_on_id, type
		FROM wisp_dependencies`
}

// HeldPrefixes returns the id prefixes this database holds rows for: its
// issue_prefix and every allowed_prefixes entry, each without a trailing
// hyphen. A missing config row contributes nothing.
func HeldPrefixes(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT `key`, value FROM config WHERE `key` IN ('issue_prefix', 'allowed_prefixes')")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var held []string
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		for _, p := range strings.Split(value, ",") {
			if p = strings.TrimSuffix(strings.TrimSpace(p), "-"); p != "" {
				held = append(held, p)
			}
		}
	}
	return held, rows.Err()
}

// IsUnverifiableDepTarget reports whether a dependency target names something
// this database cannot hold a row for, so that a missing row is expected and
// the edge is not an orphan. Two shapes qualify: an "external:" ref, and a
// target whose prefix (types.ExtractPrefix, the first-hyphen rule) differs
// from its source's and is not one of the held prefixes (HeldPrefixes), which
// names another repository (#4647).
//
// Where the edge is stored does not decide it: migrations move same-prefix
// targets that were already missing into depends_on_external, and a target
// under an allowed_prefixes entry is stored there too. A target with no
// hyphen names no other repository. A missing row for any of these is still
// an orphan. The orphan check, its --fix and the --deep integrity check all
// classify by this one rule.
func IsUnverifiableDepTarget(sourceID, targetID string, held []string) bool {
	if strings.HasPrefix(targetID, "external:") {
		return true
	}
	prefix := types.ExtractPrefix(targetID)
	if prefix == "" || prefix == types.ExtractPrefix(sourceID) {
		return false
	}
	for _, p := range held {
		if strings.HasPrefix(targetID, p+"-") {
			return false
		}
	}
	return true
}
