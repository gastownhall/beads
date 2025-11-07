package sqlite

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite/migrations"
	"github.com/steveyegge/beads/internal/types"
)

func TestMigrateChildCountersTable(t *testing.T) {
	t.Run("creates child_counters table if not exists", func(t *testing.T) {
		// Create a temp database without child_counters table
		dbFile, err := os.CreateTemp("", "beads_test_*.db")
		if err != nil {
			t.Fatalf("failed to create temp db: %v", err)
		}
		defer os.Remove(dbFile.Name())
		dbFile.Close()

		db, err := sql.Open("sqlite3", dbFile.Name())
		if err != nil {
			t.Fatalf("failed to open db: %v", err)
		}
		defer db.Close()

		// Create minimal schema without child_counters
		_, err = db.Exec(`
			CREATE TABLE issues (
				id TEXT PRIMARY KEY,
				title TEXT NOT NULL,
				status TEXT NOT NULL DEFAULT 'open'
			);
		`)
		if err != nil {
			t.Fatalf("failed to create issues table: %v", err)
		}

		// Verify child_counters doesn't exist
		var count int
		err = db.QueryRow(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type='table' AND name='child_counters'
		`).Scan(&count)
		if err != nil {
			t.Fatalf("failed to check for child_counters table: %v", err)
		}
		if count != 0 {
			t.Fatalf("child_counters table should not exist yet, got count %d", count)
		}

		// Run migration
		err = migrations.MigrateChildCountersTable(db)
		if err != nil {
			t.Fatalf("migration failed: %v", err)
		}

		// Verify child_counters exists
		err = db.QueryRow(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type='table' AND name='child_counters'
		`).Scan(&count)
		if err != nil {
			t.Fatalf("failed to check for child_counters table: %v", err)
		}
		if count == 0 {
			t.Fatalf("child_counters table not created, got count %d", count)
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		// Create storage with full schema (including child_counters)
		s := newTestStore(t, "")
		defer s.Close()

		db := s.db

		// Run migration twice
		err := migrations.MigrateChildCountersTable(db)
		if err != nil {
			t.Fatalf("first migration failed: %v", err)
		}

		err = migrations.MigrateChildCountersTable(db)
		if err != nil {
			t.Fatalf("second migration failed (not idempotent): %v", err)
		}

		// Verify table exists
		var count int
		err = db.QueryRow(`
			SELECT COUNT(*) FROM sqlite_master
			WHERE type='table' AND name='child_counters'
		`).Scan(&count)
		if err != nil {
			t.Fatalf("failed to check for child_counters table: %v", err)
		}
		if count == 0 {
			t.Fatalf("child_counters table not found, got count %d", count)
		}
	})

	t.Run("has ON DELETE CASCADE constraint", func(t *testing.T) {
		// Create storage with full schema
		s := newTestStore(t, "")
		defer s.Close()

		ctx := context.Background()

		// Create a parent issue
		parent := &types.Issue{
			ID:        "bd-parent",
			Title:     "Parent",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeEpic,
		}
		err := s.CreateIssue(ctx, parent, "test")
		if err != nil {
			t.Fatalf("failed to create parent issue: %v", err)
		}

		// Generate child ID (this populates child_counters)
		childID, err := s.GetNextChildID(ctx, "bd-parent")
		if err != nil {
			t.Fatalf("failed to get next child ID: %v", err)
		}
		if childID != "bd-parent.1" {
			t.Fatalf("expected bd-parent.1, got %s", childID)
		}

		// Verify child_counters has entry for parent
		var lastChild int
		err = s.db.QueryRow(`
			SELECT last_child FROM child_counters WHERE parent_id = ?
		`, "bd-parent").Scan(&lastChild)
		if err != nil {
			t.Fatalf("failed to query child_counters: %v", err)
		}
		if lastChild != 1 {
			t.Fatalf("expected last_child=1, got %d", lastChild)
		}

		// Delete the parent issue
		err = s.DeleteIssue(ctx, "bd-parent")
		if err != nil {
			t.Fatalf("failed to delete parent issue: %v", err)
		}

		// Verify child_counters entry was CASCADE deleted
		var count int
		err = s.db.QueryRow(`
			SELECT COUNT(*) FROM child_counters WHERE parent_id = ?
		`, "bd-parent").Scan(&count)
		if err != nil {
			t.Fatalf("failed to query child_counters: %v", err)
		}
		if count != 0 {
			t.Fatalf("expected child_counters entry to be deleted, found %d entries", count)
		}
	})
}
