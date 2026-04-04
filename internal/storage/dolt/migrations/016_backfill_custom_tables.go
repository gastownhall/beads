package migrations

import (
	"database/sql"
	"fmt"
)

// BackfillCustomTables populates custom_types and custom_statuses tables
// from config values when the tables exist but are empty.
//
// Migration 015 created these tables and populated them from config, but
// only when the tables did not already exist. Databases where the tables
// were created by schema initialization (before 015 ran) have empty tables
// because 015 skipped population on seeing the table already present.
//
// The empty tables shadow the config fallback in ResolveCustomTypesInTx
// and ResolveCustomStatusesDetailedInTx, causing "invalid issue type"
// errors for custom types like "agent".
//
// Fixes: GH#2984, GH#1632
func BackfillCustomTables(db *sql.DB) error {
	if err := backfillCustomTypes(db); err != nil {
		return fmt.Errorf("custom_types backfill: %w", err)
	}
	if err := backfillCustomStatuses(db); err != nil {
		return fmt.Errorf("custom_statuses backfill: %w", err)
	}
	return nil
}

func backfillCustomTypes(db *sql.DB) error {
	exists, err := tableExists(db, "custom_types")
	if err != nil || !exists {
		return err
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_types").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil // Already populated
	}

	// Read from config table
	var value string
	err = db.QueryRow("SELECT `value` FROM config WHERE `key` = 'types.custom'").Scan(&value)
	if err != nil || value == "" {
		return nil // No config to backfill from
	}

	for _, name := range parseTypesValue(value) {
		_, err = db.Exec("INSERT IGNORE INTO custom_types (name) VALUES (?)", name)
		if err != nil {
			return fmt.Errorf("inserting type %q: %w", name, err)
		}
	}

	return nil
}

func backfillCustomStatuses(db *sql.DB) error {
	exists, err := tableExists(db, "custom_statuses")
	if err != nil || !exists {
		return err
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM custom_statuses").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil // Already populated
	}

	// Read from config table
	var value string
	err = db.QueryRow("SELECT `value` FROM config WHERE `key` = 'status.custom'").Scan(&value)
	if err != nil || value == "" {
		return nil // No config to backfill from
	}

	// Status config can be JSON array of strings or comma-separated.
	// Categories are not stored in config — use "unspecified" default.
	for _, name := range parseTypesValue(value) {
		_, err = db.Exec("INSERT IGNORE INTO custom_statuses (name, category) VALUES (?, 'unspecified')", name)
		if err != nil {
			return fmt.Errorf("inserting status %q: %w", name, err)
		}
	}

	return nil
}
