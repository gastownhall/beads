package migrations

import (
	"database/sql"
	"fmt"
	"strings"
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

	// Populate from existing config value
	var value string
	err = db.QueryRow("SELECT `value` FROM config WHERE `key` = 'status.custom'").Scan(&value)
	if err != nil || value == "" {
		return nil
	}

	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		category := "unspecified"
		if idx := strings.IndexByte(part, ':'); idx >= 0 {
			name = part[:idx]
			category = part[idx+1:]
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		_, err = db.Exec("INSERT IGNORE INTO custom_statuses (name, category) VALUES (?, ?)", name, category)
		if err != nil {
			return fmt.Errorf("inserting status %q: %w", name, err)
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
	// Simple JSON array detection
	if strings.HasPrefix(value, "[") {
		// Strip brackets and quotes
		inner := strings.TrimPrefix(value, "[")
		inner = strings.TrimSuffix(inner, "]")
		parts := strings.Split(inner, ",")
		var result []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			p = strings.Trim(p, `"`)
			if p != "" {
				result = append(result, p)
			}
		}
		return result
	}
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
