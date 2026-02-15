package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const legacyIssuesSchemaWithoutSpecID = `
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
    created_by TEXT DEFAULT '',
    owner TEXT DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    closed_at DATETIME,
    closed_by_session TEXT DEFAULT '',
    external_ref TEXT,
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
    wisp_type TEXT DEFAULT '',
    pinned INTEGER DEFAULT 0,
    is_template INTEGER DEFAULT 0,
    crystallizes INTEGER DEFAULT 0,
    mol_type TEXT DEFAULT '',
    work_type TEXT DEFAULT 'mutex',
    quality_score REAL,
    source_system TEXT DEFAULT '',
    metadata TEXT NOT NULL DEFAULT '{}',
    event_kind TEXT DEFAULT '',
    actor TEXT DEFAULT '',
    target TEXT DEFAULT '',
    payload TEXT DEFAULT '',
    CHECK (
        (status = 'closed' AND closed_at IS NOT NULL) OR
        (status NOT IN ('closed') AND closed_at IS NULL)
    )
);
`

func TestDoctorFix_UpgradesLegacySchemaWithoutSpecID(t *testing.T) {
	requireTestGuardDisabled(t)
	if testing.Short() {
		t.Skip("skipping slow doctor fix integration test in short mode")
	}

	bdExe := buildBDForTest(t)
	ws := mkTmpDirInTmp(t, "bd-doctor-migrate-*")

	tmpHome := filepath.Join(ws, "home")
	if err := os.MkdirAll(tmpHome, 0o755); err != nil {
		t.Fatalf("create temp home: %v", err)
	}
	t.Setenv("HOME", tmpHome)

	beadsDir := filepath.Join(ws, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads dir: %v", err)
	}

	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"database":"beads.db","jsonl_export":"issues.jsonl"}`), 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_pragma=foreign_keys(ON)&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := db.Exec(legacyIssuesSchemaWithoutSpecID); err != nil {
		_ = db.Close()
		t.Fatalf("create legacy issues table: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		_ = db.Close()
		t.Fatalf("create metadata table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO metadata (key, value) VALUES ('bd_version', '0.49.3')`); err != nil {
		_ = db.Close()
		t.Fatalf("insert legacy version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	out, err := runBDSideDB(t, bdExe, ws, dbPath, "doctor", "--fix", "--yes")
	if err != nil {
		t.Fatalf("bd doctor --fix failed: %v\n%s", err, out)
	}

	verifyDB, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open upgraded db: %v", err)
	}
	defer verifyDB.Close()

	var version string
	if err := verifyDB.QueryRow(`SELECT value FROM metadata WHERE key = 'bd_version'`).Scan(&version); err != nil {
		t.Fatalf("read upgraded version: %v", err)
	}
	if version != Version {
		t.Fatalf("expected upgraded version %s, got %s", Version, version)
	}

	var specIDCount int
	if err := verifyDB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('issues') WHERE name = 'spec_id'`).Scan(&specIDCount); err != nil {
		t.Fatalf("check spec_id column: %v", err)
	}
	if specIDCount != 1 {
		t.Fatalf("expected spec_id column to exist, count=%d", specIDCount)
	}

	var specIDIndexCount int
	if err := verifyDB.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('issues') WHERE name = 'idx_issues_spec_id'`).Scan(&specIDIndexCount); err != nil {
		t.Fatalf("check spec_id index: %v", err)
	}
	if specIDIndexCount != 1 {
		t.Fatalf("expected idx_issues_spec_id to exist, count=%d", specIDIndexCount)
	}
}
