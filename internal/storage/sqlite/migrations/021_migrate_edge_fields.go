package migrations

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// MigrateEdgeFields migrates existing issue fields to dependency edges.
// This is Phase 3 of the Edge Schema Consolidation (Decision 004).
//
// Migrates:
// - replies_to -> replies-to dependency with thread_id
// - relates_to -> relates-to dependencies
// - duplicate_of -> duplicates dependency
// - superseded_by -> supersedes dependency
//
// This migration is idempotent: it uses INSERT OR IGNORE to skip
// edges that already exist (from Phase 2 dual-write).
func MigrateEdgeFields(db *sql.DB) error {
	now := time.Now()

	hasColumn := func(name string) (bool, error) {
		var exists bool
		err := db.QueryRow(`
			SELECT COUNT(*) > 0
			FROM pragma_table_info('issues')
			WHERE name = ?
		`, name).Scan(&exists)
		return exists, err
	}

	hasRepliesTo, err := hasColumn("replies_to")
	if err != nil {
		return fmt.Errorf("failed to check replies_to column: %w", err)
	}
	hasRelatesTo, err := hasColumn("relates_to")
	if err != nil {
		return fmt.Errorf("failed to check relates_to column: %w", err)
	}
	hasDuplicateOf, err := hasColumn("duplicate_of")
	if err != nil {
		return fmt.Errorf("failed to check duplicate_of column: %w", err)
	}
	hasSupersededBy, err := hasColumn("superseded_by")
	if err != nil {
		return fmt.Errorf("failed to check superseded_by column: %w", err)
	}

	if !hasRepliesTo && !hasRelatesTo && !hasDuplicateOf && !hasSupersededBy {
		return nil
	}

	// Migrate replies_to fields to replies-to edges
	// For thread_id, use the parent's ID as the thread root for first-level replies
	// (more sophisticated thread detection would require recursive queries)
	if hasRepliesTo {
		rows, err := db.Query(`
			SELECT id, replies_to
			FROM issues
			WHERE replies_to != '' AND replies_to IS NOT NULL
		`)
		if err != nil {
			return fmt.Errorf("failed to query replies_to fields: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var issueID, repliesTo string
			if err := rows.Scan(&issueID, &repliesTo); err != nil {
				return fmt.Errorf("failed to scan replies_to row: %w", err)
			}

			// Use repliesTo as thread_id (the root of the thread)
			// This is a simplification - existing threads will have the parent as thread root
			_, err := db.Exec(`
				INSERT OR IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
				VALUES (?, ?, 'replies-to', ?, 'migration', '{}', ?)
			`, issueID, repliesTo, now, repliesTo)
			if err != nil {
				return fmt.Errorf("failed to create replies-to edge for %s: %w", issueID, err)
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("error iterating replies_to rows: %w", err)
		}
	}

	// Migrate relates_to fields to relates-to edges
	// relates_to is stored as JSON array string
	if hasRelatesTo {
		rows, err := db.Query(`
			SELECT id, relates_to
			FROM issues
			WHERE relates_to != '' AND relates_to != '[]' AND relates_to IS NOT NULL
		`)
		if err != nil {
			return fmt.Errorf("failed to query relates_to fields: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var issueID, relatesTo string
			if err := rows.Scan(&issueID, &relatesTo); err != nil {
				return fmt.Errorf("failed to scan relates_to row: %w", err)
			}

			// Parse JSON array
			var relatedIDs []string
			if err := json.Unmarshal([]byte(relatesTo), &relatedIDs); err != nil {
				// Skip malformed JSON
				continue
			}

			for _, relatedID := range relatedIDs {
				if relatedID == "" {
					continue
				}
				_, err := db.Exec(`
					INSERT OR IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
					VALUES (?, ?, 'relates-to', ?, 'migration', '{}', '')
				`, issueID, relatedID, now)
				if err != nil {
					return fmt.Errorf("failed to create relates-to edge for %s -> %s: %w", issueID, relatedID, err)
				}
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("error iterating relates_to rows: %w", err)
		}
	}

	// Migrate duplicate_of fields to duplicates edges
	if hasDuplicateOf {
		rows, err := db.Query(`
			SELECT id, duplicate_of
			FROM issues
			WHERE duplicate_of != '' AND duplicate_of IS NOT NULL
		`)
		if err != nil {
			return fmt.Errorf("failed to query duplicate_of fields: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var issueID, duplicateOf string
			if err := rows.Scan(&issueID, &duplicateOf); err != nil {
				return fmt.Errorf("failed to scan duplicate_of row: %w", err)
			}

			_, err := db.Exec(`
				INSERT OR IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
				VALUES (?, ?, 'duplicates', ?, 'migration', '{}', '')
			`, issueID, duplicateOf, now)
			if err != nil {
				return fmt.Errorf("failed to create duplicates edge for %s: %w", issueID, err)
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("error iterating duplicate_of rows: %w", err)
		}
	}

	// Migrate superseded_by fields to supersedes edges
	if hasSupersededBy {
		rows, err := db.Query(`
			SELECT id, superseded_by
			FROM issues
			WHERE superseded_by != '' AND superseded_by IS NOT NULL
		`)
		if err != nil {
			return fmt.Errorf("failed to query superseded_by fields: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var issueID, supersededBy string
			if err := rows.Scan(&issueID, &supersededBy); err != nil {
				return fmt.Errorf("failed to scan superseded_by row: %w", err)
			}

			_, err := db.Exec(`
				INSERT OR IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
				VALUES (?, ?, 'supersedes', ?, 'migration', '{}', '')
			`, issueID, supersededBy, now)
			if err != nil {
				return fmt.Errorf("failed to create supersedes edge for %s: %w", issueID, err)
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("error iterating superseded_by rows: %w", err)
		}
	}

	return nil
}
