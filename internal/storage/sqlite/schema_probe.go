// Package sqlite - schema compatibility probing
package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
)

// errSchemaIncompatible is returned when the database schema is incompatible with the current version
var errSchemaIncompatible = fmt.Errorf("database schema is incompatible")

// expectedSchema defines all expected tables and their required columns
// This is used to verify migrations completed successfully
var expectedSchema = map[string][]string{
	"issues": {
		"id", "title", "description", "design", "acceptance_criteria", "notes",
		"status", "priority", "issue_type", "assignee", "estimated_minutes",
		"created_at", "created_by", "owner", "updated_at", "closed_at", "closed_by_session",
		"content_hash", "external_ref", "spec_id",
		"compaction_level", "compacted_at", "compacted_at_commit", "original_size",
		"source_repo", "close_reason",
		"deleted_at", "deleted_by", "delete_reason", "original_type",
		"sender", "ephemeral", "wisp_type", "pinned",
		"is_template", "crystallizes", "mol_type", "work_type", "quality_score",
		"source_system", "metadata",
		"event_kind", "actor", "target", "payload",
	},
	"dependencies":         {"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id"},
	"labels":               {"issue_id", "label"},
	"comments":             {"id", "issue_id", "author", "text", "created_at"},
	"events":               {"id", "issue_id", "event_type", "actor", "old_value", "new_value", "comment", "created_at"},
	"config":               {"key", "value"},
	"metadata":             {"key", "value"},
	"child_counters":       {"parent_id", "last_child"},
	"issue_snapshots":      {"id", "issue_id", "snapshot_time", "compaction_level", "original_size", "compressed_size", "original_content", "archived_events"},
	"compaction_snapshots": {"id", "issue_id", "compaction_level", "snapshot_json", "created_at"},
	"repo_mtimes":          {"repo_path", "jsonl_path", "mtime_ns", "last_checked"},
}

// schemaProbeResult contains the results of a schema compatibility check
type schemaProbeResult struct {
	Compatible     bool
	MissingTables  []string
	MissingColumns map[string][]string // table -> missing columns
	ErrorMessage   string
}

// probeSchema verifies all expected tables and columns exist
// Returns schemaProbeResult with details about any missing schema elements
func probeSchema(db *sql.DB) schemaProbeResult {
	result := schemaProbeResult{
		Compatible:     true,
		MissingTables:  []string{},
		MissingColumns: make(map[string][]string),
	}

	for table, expectedCols := range expectedSchema {
		// Try to query the table with all expected columns
		query := fmt.Sprintf("SELECT %s FROM %s LIMIT 0", strings.Join(expectedCols, ", "), table) // #nosec G201 -- table/column names sourced from hardcoded schema
		_, err := db.Exec(query)

		if err != nil {
			errMsg := err.Error()

			// Check if table doesn't exist
			if strings.Contains(errMsg, "no such table") {
				result.Compatible = false
				result.MissingTables = append(result.MissingTables, table)
				continue
			}

			// Check if column doesn't exist
			if strings.Contains(errMsg, "no such column") {
				result.Compatible = false
				// Try to find which columns are missing
				missingCols := findMissingColumns(db, table, expectedCols)
				if len(missingCols) > 0 {
					result.MissingColumns[table] = missingCols
				}
			}
		}
	}

	// Build error message if incompatible
	if !result.Compatible {
		var parts []string
		if len(result.MissingTables) > 0 {
			parts = append(parts, fmt.Sprintf("missing tables: %s", strings.Join(result.MissingTables, ", ")))
		}
		if len(result.MissingColumns) > 0 {
			for table, cols := range result.MissingColumns {
				parts = append(parts, fmt.Sprintf("missing columns in %s: %s", table, strings.Join(cols, ", ")))
			}
		}
		result.ErrorMessage = strings.Join(parts, "; ")
	}

	return result
}

// findMissingColumns determines which columns are missing from a table
func findMissingColumns(db *sql.DB, table string, expectedCols []string) []string {
	missing := []string{}

	for _, col := range expectedCols {
		query := fmt.Sprintf("SELECT %s FROM %s LIMIT 0", col, table) // #nosec G201 -- table/column names sourced from hardcoded schema
		_, err := db.Exec(query)
		if err != nil && strings.Contains(err.Error(), "no such column") {
			missing = append(missing, col)
		}
	}

	return missing
}

// verifySchemaCompatibility runs schema probe and returns detailed error on failure
func verifySchemaCompatibility(db *sql.DB) error {
	result := probeSchema(db)

	if !result.Compatible {
		return fmt.Errorf("%w: %s", errSchemaIncompatible, result.ErrorMessage)
	}

	return nil
}
