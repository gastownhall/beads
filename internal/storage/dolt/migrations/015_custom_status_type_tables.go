package migrations

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// MigrateCustomStatusTypeTables creates normalized custom_statuses and
// custom_types tables, then populates them from existing config values.
// This migration is idempotent — safe to run multiple times.
func MigrateCustomStatusTypeTables(db *sql.DB) error {
	if err := migrateCustomStatusesTable(db); err != nil {
		return fmt.Errorf("custom_statuses table: %w", err)
	}
	if err := migrateCustomTypesTable(db); err != nil {
		return fmt.Errorf("custom_types table: %w", err)
	}
	return nil
}

func migrateCustomStatusesTable(db *sql.DB) error {
	exists, err := tableExists(db, "custom_statuses")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = db.Exec(`CREATE TABLE custom_statuses (
		name VARCHAR(64) PRIMARY KEY,
		category VARCHAR(32) NOT NULL DEFAULT 'unspecified'
	)`)
	if err != nil {
		return fmt.Errorf("creating table: %w", err)
	}

	// Populate from existing config value using validated parser.
	// Invalid entries are logged and skipped — the migration is intentionally lenient
	// so existing databases aren't blocked. The first `bd config set status.custom`
	// will normalize the data through the stricter runtime validation.
	var value string
	err = db.QueryRow("SELECT `value` FROM config WHERE `key` = 'status.custom'").Scan(&value)
	if err != nil || value == "" {
		return nil
	}

	parsed, parseErr := types.ParseCustomStatusConfig(value)
	if parseErr != nil {
		// Config has invalid entries — log and skip rather than blocking the migration.
		log.Printf("migration: skipping invalid status.custom entries: %v", parseErr)
		return nil
	}
	for _, s := range parsed {
		_, err = db.Exec("INSERT IGNORE INTO custom_statuses (name, category) VALUES (?, ?)", s.Name, string(s.Category))
		if err != nil {
			return fmt.Errorf("inserting status %q: %w", s.Name, err)
		}
	}

	return nil
}

func migrateCustomTypesTable(db *sql.DB) error {
	exists, err := tableExists(db, "custom_types")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = db.Exec(`CREATE TABLE custom_types (
		name VARCHAR(64) PRIMARY KEY
	)`)
	if err != nil {
		return fmt.Errorf("creating table: %w", err)
	}

	// Populate from existing config value
	var value string
	err = db.QueryRow("SELECT `value` FROM config WHERE `key` = 'types.custom'").Scan(&value)
	if err != nil || value == "" {
		return nil
	}

	// Try JSON array first, fall back to comma-separated
	names := parseTypesValue(value)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		_, err = db.Exec("INSERT IGNORE INTO custom_types (name) VALUES (?)", name)
		if err != nil {
			return fmt.Errorf("inserting type %q: %w", name, err)
		}
	}

	return nil
}

// parseTypesValue tries JSON array then comma-separated.
func parseTypesValue(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	// Try JSON array first (e.g. '["gate","convoy"]')
	var jsonTypes []string
	if err := json.Unmarshal([]byte(value), &jsonTypes); err == nil {
		return jsonTypes
	}
	// Fall back to comma-separated
	parts := strings.Split(value, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
