package sqlite

import (
	"database/sql"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

func TestProbeSchema_AllTablesPresent(t *testing.T) {
	// Create in-memory database with full schema
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Initialize schema and run migrations
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed to initialize schema: %v", err)
	}
	if err := RunMigrations(db); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Run schema probe
	result := probeSchema(db)

	// Should be compatible
	if !result.Compatible {
		t.Errorf("expected schema to be compatible, got: %s", result.ErrorMessage)
	}
	if len(result.MissingTables) > 0 {
		t.Errorf("unexpected missing tables: %v", result.MissingTables)
	}
	if len(result.MissingColumns) > 0 {
		t.Errorf("unexpected missing columns: %v", result.MissingColumns)
	}
}

func TestProbeSchema_MissingTable(t *testing.T) {
	// Create in-memory database without child_counters table
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create minimal schema (just issues table)
	_, err = db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
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
			content_hash TEXT,
			external_ref TEXT,
			compaction_level INTEGER DEFAULT 0,
			compacted_at DATETIME,
			compacted_at_commit TEXT,
			original_size INTEGER
		)
	`)
	if err != nil {
		t.Fatalf("failed to create issues table: %v", err)
	}

	// Run schema probe
	result := probeSchema(db)

	// Should not be compatible
	if result.Compatible {
		t.Error("expected schema to be incompatible (missing tables)")
	}
	if len(result.MissingTables) == 0 {
		t.Error("expected missing tables to be reported")
	}
}

func TestProbeSchema_MissingColumn(t *testing.T) {
	// Create in-memory database with issues table missing content_hash
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create issues table WITHOUT content_hash column
	_, err = db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
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
			compaction_level INTEGER DEFAULT 0,
			compacted_at DATETIME,
			compacted_at_commit TEXT,
			original_size INTEGER
		);
		CREATE TABLE dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'blocks',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_by TEXT NOT NULL,
			PRIMARY KEY (issue_id, depends_on_id)
		);
		CREATE TABLE labels (issue_id TEXT NOT NULL, label TEXT NOT NULL, PRIMARY KEY (issue_id, label));
		CREATE TABLE comments (id INTEGER PRIMARY KEY AUTOINCREMENT, issue_id TEXT NOT NULL, author TEXT NOT NULL, text TEXT NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
		CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, issue_id TEXT NOT NULL, event_type TEXT NOT NULL, actor TEXT NOT NULL, old_value TEXT, new_value TEXT, comment TEXT, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
		CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE child_counters (parent_id TEXT PRIMARY KEY, last_child INTEGER NOT NULL DEFAULT 0);
		CREATE TABLE issue_snapshots (id INTEGER PRIMARY KEY AUTOINCREMENT, issue_id TEXT NOT NULL, snapshot_time DATETIME NOT NULL, compaction_level INTEGER NOT NULL, original_size INTEGER NOT NULL, compressed_size INTEGER NOT NULL, original_content TEXT NOT NULL, archived_events TEXT);
		CREATE TABLE compaction_snapshots (id INTEGER PRIMARY KEY AUTOINCREMENT, issue_id TEXT NOT NULL, compaction_level INTEGER NOT NULL, snapshot_json BLOB NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
		CREATE TABLE repo_mtimes (repo_path TEXT PRIMARY KEY, jsonl_path TEXT NOT NULL, mtime_ns INTEGER NOT NULL, last_checked DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
	`)
	if err != nil {
		t.Fatalf("failed to create tables: %v", err)
	}

	// Run schema probe
	result := probeSchema(db)

	// Should not be compatible
	if result.Compatible {
		t.Error("expected schema to be incompatible (missing content_hash column)")
	}
	if len(result.MissingColumns) == 0 {
		t.Error("expected missing columns to be reported")
	}
	if _, ok := result.MissingColumns["issues"]; !ok {
		t.Error("expected missing columns in issues table")
	}
}

func TestVerifySchemaCompatibility(t *testing.T) {
	// Create in-memory database with full schema
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Initialize schema and run migrations
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed to initialize schema: %v", err)
	}
	if err := RunMigrations(db); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Verify schema compatibility
	err = verifySchemaCompatibility(db)
	if err != nil {
		t.Errorf("expected schema to be compatible, got error: %v", err)
	}
}

func TestVerifySchemaCompatibility_Incompatible(t *testing.T) {
	// Create in-memory database with minimal schema
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create minimal schema
	_, err = db.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT NOT NULL)`)
	if err != nil {
		t.Fatalf("failed to create issues table: %v", err)
	}

	// Verify schema compatibility
	err = verifySchemaCompatibility(db)
	if err == nil {
		t.Error("expected schema incompatibility error, got nil")
	}
	if err != nil && err != errSchemaIncompatible {
		// Check that error wraps errSchemaIncompatible
		t.Logf("got error: %v", err)
	}
}
