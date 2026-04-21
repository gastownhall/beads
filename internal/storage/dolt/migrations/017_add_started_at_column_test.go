package migrations

import (
	"testing"
)

// TestMigrateAddStartedAtColumn verifies the compat migration adds
// started_at to the issues table when missing, and is idempotent on re-run.
// Regression test for GH#3363.
func TestMigrateAddStartedAtColumn(t *testing.T) {
	db := openTestDoltBranch(t)

	// Drop the column if it exists from the test's base schema so we can
	// observe the migration adding it back.
	if exists, err := columnExists(db, "issues", "started_at"); err != nil {
		t.Fatalf("failed to check column: %v", err)
	} else if exists {
		if _, err := db.Exec("ALTER TABLE `issues` DROP COLUMN started_at"); err != nil {
			t.Fatalf("failed to drop started_at for test setup: %v", err)
		}
	}

	// Verify precondition
	exists, err := columnExists(db, "issues", "started_at")
	if err != nil {
		t.Fatalf("failed to check column: %v", err)
	}
	if exists {
		t.Fatal("started_at should not exist yet")
	}

	if err := MigrateAddStartedAtColumn(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	exists, err = columnExists(db, "issues", "started_at")
	if err != nil {
		t.Fatalf("failed to check column: %v", err)
	}
	if !exists {
		t.Fatal("started_at should exist on issues after migration")
	}

	// Idempotent: re-running must succeed even when column already exists.
	if err := MigrateAddStartedAtColumn(db); err != nil {
		t.Fatalf("re-running migration should be idempotent: %v", err)
	}
}
