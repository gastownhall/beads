package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

// MigrateUUIDPrimaryKeys converts AUTO_INCREMENT BIGINT primary keys to
// CHAR(36) UUID primary keys on the 6 tables that previously used them.
// This eliminates duplicate PK collisions during Dolt federation (multi-clone
// push/pull), where independent AUTO_INCREMENT counters produce conflicting IDs.
//
// Idempotent: checks column type before migrating.
func MigrateUUIDPrimaryKeys(ctx context.Context, db Runner) error {
	tables := []string{
		"events",
		"comments",
		"issue_snapshots",
		"compaction_snapshots",
		"wisp_events",
		"wisp_comments",
	}

	for _, table := range tables {
		if err := migrateTableToUUID(ctx, db, table); err != nil {
			return fmt.Errorf("migrate %s to UUID PK: %w", table, err)
		}
	}

	return nil
}

// migrateTableToUUID converts a single table's id column from BIGINT AUTO_INCREMENT
// to CHAR(36) with UUID default. Uses add-column/copy/drop/rename pattern since
// Dolt doesn't support ALTER COLUMN to change a PK's type in place.
func migrateTableToUUID(ctx context.Context, db Runner, table string) error {
	// Check if table exists
	exists, err := tableExists(ctx, db, table)
	if err != nil {
		return fmt.Errorf("check table existence: %w", err)
	}
	if !exists {
		return nil // Table doesn't exist yet; schema.go will create it with UUID PKs
	}

	// Check if already migrated (column type is already CHAR)
	var colType string
	err = queryRowContext(ctx, db,
		"SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE TABLE_NAME = ? AND COLUMN_NAME = 'id' AND TABLE_SCHEMA = DATABASE()",
		table,
	).Scan(&colType)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil // No id column — nothing to migrate
		}
		return fmt.Errorf("check column type: %w", err)
	}

	// If already char(36), migration was already applied
	if colType == "char(36)" {
		return nil
	}

	log.Printf("migration 010: converting %s.id from %s to CHAR(36) UUID", table, colType)

	// Step 1: Add new UUID column
	//nolint:gosec // G201: table is from hardcoded list
	if _, err := execContext(ctx, db, fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN `uuid_id` CHAR(36) NOT NULL DEFAULT (UUID())", table)); err != nil {
		return fmt.Errorf("add uuid_id column: %w", err)
	}

	// Step 2: Backfill existing rows with UUIDs
	//nolint:gosec // G201: table is from hardcoded list
	if _, err := execContext(ctx, db, fmt.Sprintf("UPDATE `%s` SET `uuid_id` = UUID() WHERE `uuid_id` = '' OR `uuid_id` IS NULL", table)); err != nil {
		return fmt.Errorf("backfill uuid_id: %w", err)
	}

	// Step 3: Drop old primary key and id column.
	// Dolt requires removing AUTO_INCREMENT before dropping a PK,
	// so we MODIFY the column to plain BIGINT first.
	//nolint:gosec // G201: table is from hardcoded list
	if _, err := execContext(ctx, db, fmt.Sprintf("ALTER TABLE `%s` MODIFY `id` BIGINT NOT NULL", table)); err != nil {
		return fmt.Errorf("remove auto_increment: %w", err)
	}
	//nolint:gosec // G201: table is from hardcoded list
	if _, err := execContext(ctx, db, fmt.Sprintf("ALTER TABLE `%s` DROP PRIMARY KEY", table)); err != nil {
		return fmt.Errorf("drop primary key: %w", err)
	}
	//nolint:gosec // G201: table is from hardcoded list
	if _, err := execContext(ctx, db, fmt.Sprintf("ALTER TABLE `%s` DROP COLUMN `id`", table)); err != nil {
		return fmt.Errorf("drop old id column: %w", err)
	}

	// Step 4: Rename uuid_id to id and make it the primary key
	//nolint:gosec // G201: table is from hardcoded list
	if _, err := execContext(ctx, db, fmt.Sprintf("ALTER TABLE `%s` RENAME COLUMN `uuid_id` TO `id`", table)); err != nil {
		return fmt.Errorf("rename uuid_id to id: %w", err)
	}
	//nolint:gosec // G201: table is from hardcoded list
	if _, err := execContext(ctx, db, fmt.Sprintf("ALTER TABLE `%s` ADD PRIMARY KEY (`id`)", table)); err != nil {
		return fmt.Errorf("add primary key: %w", err)
	}

	log.Printf("migration 010: %s.id migrated to CHAR(36) UUID successfully", table)
	return nil
}
