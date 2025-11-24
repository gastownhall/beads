package migrations

import (
	"database/sql"
	"fmt"
	"log"
)

// MigrateOrphanDetection detects orphaned child issues and logs them for user action
// Orphaned children are issues with hierarchical IDs (e.g., "parent.child") where the
// parent issue no longer exists in the database.
//
// This migration does NOT automatically delete or convert orphans - it only logs them
// so the user can decide whether to:
// - Delete the orphans if they're no longer needed
// - Convert them to top-level issues by renaming them
// - Restore the missing parent issues
func MigrateOrphanDetection(db *sql.DB) error {
	// Query for orphaned children using the pattern from the issue description:
	// SELECT id FROM issues WHERE id LIKE '%.%'
	// AND substr(id, 1, instr(id || '.', '.') - 1) NOT IN (SELECT id FROM issues)
	rows, err := db.Query(`
		SELECT id
		FROM issues
		WHERE id LIKE '%.%'
		  AND substr(id, 1, instr(id || '.', '.') - 1) NOT IN (SELECT id FROM issues)
		ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("failed to query for orphaned children: %w", err)
	}
	defer rows.Close()

	var orphans []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("failed to scan orphan ID: %w", err)
		}
		orphans = append(orphans, id)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating orphan results: %w", err)
	}

	// Log results for user review
	if len(orphans) > 0 {
		log.Printf("⚠️  Orphan Detection: Found %d orphaned child issue(s):", len(orphans))
		for _, id := range orphans {
			log.Printf("  - %s", id)
		}
		log.Println("\nThese issues have hierarchical IDs but their parent issues no longer exist.")
		log.Println("You can:")
		log.Println("  1. Delete them if no longer needed: bd delete <issue-id>")
		log.Println("  2. Convert to top-level issues by exporting and reimporting with new IDs")
		log.Println("  3. Restore the missing parent issues")
	}

	// Migration is idempotent - always succeeds since it's just detection/logging
	return nil
}
