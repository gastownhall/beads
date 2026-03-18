package migrations

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// DetectOrphanedChildren finds child issues whose parent no longer exists.
// A child issue has a dotted ID (e.g., "bd-abc.1") where the parent is the
// part before the last dot ("bd-abc"). An orphan is a child whose parent ID
// is not present in the issues table.
//
// This migration is non-destructive: it only logs orphans for the user to
// review. Users can then decide to delete orphans or convert them to
// top-level issues using 'bd doctor --fix'.
func DetectOrphanedChildren(ctx context.Context, db Runner) error {
	// Find child issues (IDs containing a dot) whose parent doesn't exist.
	// SUBSTRING_INDEX(id, '.', -1) gives the last segment after the final dot.
	// Removing that (plus the dot) gives us the parent ID.
	// We use a LEFT JOIN to find children with no matching parent.
	query := `
		SELECT child.id, child.title, child.status
		FROM issues child
		LEFT JOIN issues parent
			ON parent.id = SUBSTRING(child.id, 1, LENGTH(child.id) - LENGTH(SUBSTRING_INDEX(child.id, '.', -1)) - 1)
		WHERE child.id LIKE '%.%'
			AND parent.id IS NULL
		ORDER BY child.id`

	rows, err := queryContext(ctx, db, query)
	if err != nil {
		// If the query fails (e.g., older Dolt version), log and continue.
		// This is a diagnostic migration, not a schema change.
		log.Printf("orphan detection: query failed (non-fatal): %v", err)
		return nil
	}
	defer rows.Close()

	var orphans []string
	for rows.Next() {
		var id, title, status string
		if err := rows.Scan(&id, &title, &status); err != nil {
			continue
		}
		orphans = append(orphans, fmt.Sprintf("  %s [%s] %s", id, status, title))
	}
	if err := rows.Err(); err != nil {
		log.Printf("orphan detection: row iteration error (non-fatal): %v", err)
		return nil
	}

	if len(orphans) == 0 {
		return nil
	}

	log.Printf("orphan detection: found %d orphaned child issue(s) whose parent no longer exists:\n%s",
		len(orphans), strings.Join(orphans, "\n"))
	log.Printf("orphan detection: run 'bd doctor --deep' to review, or 'bd doctor --fix' to repair")

	return nil
}
