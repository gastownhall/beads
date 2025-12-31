package migrations

import (
	"database/sql"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// TestMigrateDropEdgeColumns_FromOldSchema verifies that migration 022 works
// when upgrading from a database that still has the deprecated edge columns.
// This is a regression test for GitHub issue #809 where the migration failed
// with "SQL logic error: near '%': syntax error" because db.Exec was being
// called with %s format specifiers instead of using fmt.Sprintf first.
func TestMigrateDropEdgeColumns_FromOldSchema(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create old schema WITH the deprecated edge columns (simulating v0.30.3)
	// This is a simplified version of the old schema with just the columns
	// needed to trigger the migration path.
	_, err = db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			content_hash TEXT,
			title TEXT NOT NULL CHECK(length(title) <= 500),
			description TEXT NOT NULL DEFAULT '',
			design TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'open',
			priority INTEGER NOT NULL DEFAULT 2 CHECK(priority >= 0 AND priority <= 4),
			issue_type TEXT NOT NULL DEFAULT 'task',
			assignee TEXT,
			estimated_minutes INTEGER,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			closed_at DATETIME,
			external_ref TEXT,
			source_repo TEXT DEFAULT '',
			compaction_level INTEGER DEFAULT 0,
			compacted_at DATETIME,
			compacted_at_commit TEXT,
			original_size INTEGER,
			deleted_at DATETIME,
			deleted_by TEXT DEFAULT '',
			delete_reason TEXT DEFAULT '',
			original_type TEXT DEFAULT '',
			sender TEXT DEFAULT '',
			ephemeral INTEGER DEFAULT 0,
			close_reason TEXT DEFAULT '',
			-- These are the deprecated edge columns that trigger migration 022
			replies_to TEXT,
			relates_to TEXT,
			duplicate_of TEXT,
			superseded_by TEXT,
			CHECK ((status = 'closed') = (closed_at IS NOT NULL))
		)
	`)
	if err != nil {
		t.Fatalf("failed to create old issues table: %v", err)
	}

	// Create the dependencies table (required by migration 022)
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS dependencies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'blocks',
			metadata TEXT DEFAULT '{}',
			thread_id TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(issue_id, depends_on_id, type)
		)
	`)
	if err != nil {
		t.Fatalf("failed to create dependencies table: %v", err)
	}

	// Insert test data to ensure the migration copies correctly
	_, err = db.Exec(`
		INSERT INTO issues (id, title, status, replies_to, relates_to)
		VALUES ('test-001', 'Test Issue', 'open', 'old-ref-1', 'old-ref-2')
	`)
	if err != nil {
		t.Fatalf("failed to insert test issue: %v", err)
	}

	// Run the migration - this should NOT fail with SQL syntax error
	err = MigrateDropEdgeColumns(db)
	if err != nil {
		t.Fatalf("MigrateDropEdgeColumns failed: %v", err)
	}

	// Verify the issue still exists and data was preserved
	var id, title, status string
	err = db.QueryRow(`SELECT id, title, status FROM issues WHERE id = 'test-001'`).Scan(&id, &title, &status)
	if err != nil {
		t.Fatalf("failed to query migrated issue: %v", err)
	}
	if title != "Test Issue" {
		t.Errorf("title mismatch: got %q, want %q", title, "Test Issue")
	}
	if status != "open" {
		t.Errorf("status mismatch: got %q, want %q", status, "open")
	}

	// Verify the edge columns no longer exist
	var colCount int
	err = db.QueryRow(`
		SELECT COUNT(*)
		FROM pragma_table_info('issues')
		WHERE name IN ('replies_to', 'relates_to', 'duplicate_of', 'superseded_by')
	`).Scan(&colCount)
	if err != nil {
		t.Fatalf("failed to check columns: %v", err)
	}
	if colCount != 0 {
		t.Errorf("expected edge columns to be removed, but %d still exist", colCount)
	}
}

// TestMigrateDropEdgeColumns_WithPinnedAndTemplate verifies that the migration
// correctly preserves pinned and is_template columns when they exist.
func TestMigrateDropEdgeColumns_WithPinnedAndTemplate(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create schema with edge columns AND pinned/is_template columns
	_, err = db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			content_hash TEXT,
			title TEXT NOT NULL CHECK(length(title) <= 500),
			description TEXT NOT NULL DEFAULT '',
			design TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'open',
			priority INTEGER NOT NULL DEFAULT 2,
			issue_type TEXT NOT NULL DEFAULT 'task',
			assignee TEXT,
			estimated_minutes INTEGER,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			closed_at DATETIME,
			external_ref TEXT,
			source_repo TEXT DEFAULT '',
			compaction_level INTEGER DEFAULT 0,
			compacted_at DATETIME,
			compacted_at_commit TEXT,
			original_size INTEGER,
			deleted_at DATETIME,
			deleted_by TEXT DEFAULT '',
			delete_reason TEXT DEFAULT '',
			original_type TEXT DEFAULT '',
			sender TEXT DEFAULT '',
			ephemeral INTEGER DEFAULT 0,
			close_reason TEXT DEFAULT '',
			pinned INTEGER DEFAULT 0,
			is_template INTEGER DEFAULT 0,
			-- Deprecated edge columns
			replies_to TEXT,
			relates_to TEXT,
			duplicate_of TEXT,
			superseded_by TEXT,
			CHECK ((status = 'closed') = (closed_at IS NOT NULL))
		)
	`)
	if err != nil {
		t.Fatalf("failed to create issues table: %v", err)
	}

	// Create dependencies table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS dependencies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'blocks',
			metadata TEXT DEFAULT '{}',
			thread_id TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(issue_id, depends_on_id, type)
		)
	`)
	if err != nil {
		t.Fatalf("failed to create dependencies table: %v", err)
	}

	// Insert test data with pinned and is_template set
	_, err = db.Exec(`
		INSERT INTO issues (id, title, status, pinned, is_template)
		VALUES ('test-001', 'Pinned Template', 'open', 1, 1)
	`)
	if err != nil {
		t.Fatalf("failed to insert test issue: %v", err)
	}

	// Run migration
	err = MigrateDropEdgeColumns(db)
	if err != nil {
		t.Fatalf("MigrateDropEdgeColumns failed: %v", err)
	}

	// Verify pinned and is_template were preserved
	var pinned, isTemplate int
	err = db.QueryRow(`SELECT pinned, is_template FROM issues WHERE id = 'test-001'`).Scan(&pinned, &isTemplate)
	if err != nil {
		t.Fatalf("failed to query migrated issue: %v", err)
	}
	if pinned != 1 {
		t.Errorf("pinned not preserved: got %d, want 1", pinned)
	}
	if isTemplate != 1 {
		t.Errorf("is_template not preserved: got %d, want 1", isTemplate)
	}
}
