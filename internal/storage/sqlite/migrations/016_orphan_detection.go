package migrations

import (
	"database/sql"
	"fmt"
	"log"
)

// MigrateOrphanDetection detects orphaned child issues and logs them for user action
// Orphaned children are issues with hierarchical IDs (e.g., "parent.1") where the
// parent issue no longer exists in the database.
//
// Hierarchical IDs have the format {parentID}.{N} where N is a numeric child suffix.
// This correctly handles prefixes that contain dots (e.g., "my.project-abc123" is NOT
// hierarchical, but "my.project-abc123.1" IS hierarchical). See GH#508.
//
// This migration does NOT automatically delete or convert orphans - it only logs them
// so the user can decide whether to:
// - Delete the orphans if they're no longer needed
// - Convert them to top-level issues by renaming them
// - Restore the missing parent issues
func MigrateOrphanDetection(db *sql.DB) error {
	// Query for orphaned children:
	// - Must end with .N where N is 1-4 digits (covers child numbers 0-9999)
	// - Parent (everything before the last .N) must not exist in issues table
	// - Uses GLOB patterns to ensure suffix is purely numeric
	// - rtrim removes trailing digits, then trailing dot, to get parent ID
	//
	// GH#508: The old query used instr() to find the first dot, which incorrectly
	// flagged IDs with dots in the prefix (e.g., "my.project-abc") as orphans.
	// The fix uses GLOB patterns to only match IDs ending with .{digits}.
	rows, err := db.Query(`
		SELECT id
		FROM issues
		WHERE
		  -- Must end with .N where N is 1-4 digits (child number suffix)
		  (id GLOB '*.[0-9]' OR id GLOB '*.[0-9][0-9]' OR id GLOB '*.[0-9][0-9][0-9]' OR id GLOB '*.[0-9][0-9][0-9][0-9]')
		  -- Parent (remove trailing digits then dot) must not exist
		  AND rtrim(rtrim(id, '0123456789'), '.') NOT IN (SELECT id FROM issues)
		  -- Skip closed issues - no point warning about dead orphans
		  AND status != 'closed'
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
