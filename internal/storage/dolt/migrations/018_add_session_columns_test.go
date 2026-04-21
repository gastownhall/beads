package migrations

import (
	"testing"
)

// TestMigrateAddSessionColumns verifies the compat migration adds
// created_by_session and claimed_by_session to the issues table when
// missing, and is idempotent on re-run.
func TestMigrateAddSessionColumns(t *testing.T) {
	db := openTestDoltBranch(t)

	cols := []string{"created_by_session", "claimed_by_session"}

	// Drop columns if they exist from the test's base schema so we can
	// observe the migration adding them back.
	for _, col := range cols {
		if exists, err := columnExists(db, "issues", col); err != nil {
			t.Fatalf("failed to check %s column: %v", col, err)
		} else if exists {
			if _, err := db.Exec("ALTER TABLE `issues` DROP COLUMN " + col); err != nil {
				t.Fatalf("failed to drop %s for test setup: %v", col, err)
			}
		}
	}

	// Verify preconditions
	for _, col := range cols {
		exists, err := columnExists(db, "issues", col)
		if err != nil {
			t.Fatalf("failed to check %s column: %v", col, err)
		}
		if exists {
			t.Fatalf("%s should not exist yet", col)
		}
	}

	if err := MigrateAddSessionColumns(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// Verify post-conditions
	for _, col := range cols {
		exists, err := columnExists(db, "issues", col)
		if err != nil {
			t.Fatalf("failed to check %s column: %v", col, err)
		}
		if !exists {
			t.Fatalf("%s should exist on issues after migration", col)
		}
	}

	// Idempotent: re-running must succeed even when columns already exist.
	if err := MigrateAddSessionColumns(db); err != nil {
		t.Fatalf("re-running migration should be idempotent: %v", err)
	}
}
