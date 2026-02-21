//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/ephemeral"
	"github.com/steveyegge/beads/internal/types"
)

func TestMigrateWisps(t *testing.T) {
	// Set up a temporary Dolt store (embedded mode)
	tmpDir := t.TempDir()

	ctx := context.Background()
	doltCfg := &dolt.Config{
		Path:           filepath.Join(tmpDir, "dolt"),
		CommitterName:  "test",
		CommitterEmail: "test@test.com",
		Database:       "testdb",
	}
	doltStore, err := dolt.New(ctx, doltCfg)
	if err != nil {
		t.Fatalf("failed to create dolt store: %v", err)
	}
	defer doltStore.Close()

	doltDB := doltStore.UnderlyingDB()

	// Ensure wisps table exists by running migration
	if err := ensureWispsTable(ctx, doltDB); err != nil {
		// Table doesn't exist yet — run the migration manually
		_, err = doltDB.ExecContext(ctx, wispsTableSchemaForTest)
		if err != nil {
			t.Fatalf("failed to create wisps table: %v", err)
		}
	}

	// Create wisp_dependencies table
	if err := ensureWispDependenciesTable(ctx, doltDB); err != nil {
		t.Fatalf("failed to create wisp_dependencies table: %v", err)
	}

	// Set up SQLite ephemeral store with test data
	sqlitePath := filepath.Join(tmpDir, "ephemeral.sqlite3")
	es, err := ephemeral.New(sqlitePath, "bd")
	if err != nil {
		t.Fatalf("failed to create ephemeral store: %v", err)
	}
	defer es.Close()

	// Insert test issues
	issues := []*types.Issue{
		{ID: "bd-wisp-aaa1", Title: "Test wisp 1", Description: "Desc 1", Status: types.StatusOpen, Ephemeral: true},
		{ID: "bd-wisp-aaa2", Title: "Test wisp 2", Description: "Desc 2", Status: types.StatusClosed, Ephemeral: true},
		{ID: "bd-wisp-aaa3", Title: "Test wisp 3", Description: "Desc 3", Status: types.StatusOpen, Ephemeral: true},
	}
	for _, issue := range issues {
		if err := es.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create test issue %s: %v", issue.ID, err)
		}
	}

	// Insert test dependencies
	deps := []*types.Dependency{
		{IssueID: "bd-wisp-aaa1", DependsOnID: "bd-wisp-aaa2", Type: "blocks", CreatedBy: "test"},
		{IssueID: "bd-wisp-aaa3", DependsOnID: "bd-wisp-aaa1", Type: "parent-child", CreatedBy: "test"},
	}
	for _, dep := range deps {
		if err := es.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("failed to create test dep: %v", err)
		}
	}

	// Verify source counts
	srcCount, err := es.Count(ctx)
	if err != nil {
		t.Fatalf("failed to count source issues: %v", err)
	}
	if srcCount != 3 {
		t.Fatalf("expected 3 source issues, got %d", srcCount)
	}

	sqliteDB := es.DB()

	// Run migration
	migratedIssues, skippedIssues, err := migrateIssuesToWisps(ctx, sqliteDB, doltDB)
	if err != nil {
		t.Fatalf("migrateIssuesToWisps failed: %v", err)
	}
	if migratedIssues != 3 {
		t.Errorf("expected 3 migrated issues, got %d", migratedIssues)
	}
	if skippedIssues != 0 {
		t.Errorf("expected 0 skipped issues, got %d", skippedIssues)
	}

	migratedDeps, skippedDeps, err := migrateDepsToWispDeps(ctx, sqliteDB, doltDB)
	if err != nil {
		t.Fatalf("migrateDepsToWispDeps failed: %v", err)
	}
	if migratedDeps != 2 {
		t.Errorf("expected 2 migrated deps, got %d", migratedDeps)
	}
	if skippedDeps != 0 {
		t.Errorf("expected 0 skipped deps, got %d", skippedDeps)
	}

	// Verify destination counts
	var dstIssues, dstDeps int
	if err := doltDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps").Scan(&dstIssues); err != nil {
		t.Fatalf("failed to count wisps: %v", err)
	}
	if err := doltDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisp_dependencies").Scan(&dstDeps); err != nil {
		t.Fatalf("failed to count wisp_dependencies: %v", err)
	}
	if dstIssues != 3 {
		t.Errorf("expected 3 wisps rows, got %d", dstIssues)
	}
	if dstDeps != 2 {
		t.Errorf("expected 2 wisp_dependencies rows, got %d", dstDeps)
	}

	// Test idempotency: running again should skip duplicates
	migratedIssues2, skippedIssues2, err := migrateIssuesToWisps(ctx, sqliteDB, doltDB)
	if err != nil {
		t.Fatalf("second migrateIssuesToWisps failed: %v", err)
	}
	// INSERT IGNORE means all 3 are "migrated" (no error) but duplicates don't actually insert
	// The count should still be 3
	_ = migratedIssues2
	_ = skippedIssues2

	var finalCount int
	if err := doltDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps").Scan(&finalCount); err != nil {
		t.Fatalf("failed to count wisps after idempotent run: %v", err)
	}
	if finalCount != 3 {
		t.Errorf("idempotency check: expected 3 wisps, got %d", finalCount)
	}
}

// wispsTableSchemaForTest is the same schema used by migration 004
const wispsTableSchemaForTest = `CREATE TABLE IF NOT EXISTS wisps (
    id VARCHAR(255) PRIMARY KEY,
    content_hash VARCHAR(64),
    title VARCHAR(500) NOT NULL,
    description TEXT NOT NULL,
    design TEXT NOT NULL,
    acceptance_criteria TEXT NOT NULL,
    notes TEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'open',
    priority INT NOT NULL DEFAULT 2,
    issue_type VARCHAR(32) NOT NULL DEFAULT 'task',
    assignee VARCHAR(255),
    estimated_minutes INT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    owner VARCHAR(255) DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    closed_at DATETIME,
    closed_by_session VARCHAR(255) DEFAULT '',
    external_ref VARCHAR(255),
    spec_id VARCHAR(1024),
    compaction_level INT DEFAULT 0,
    compacted_at DATETIME,
    compacted_at_commit VARCHAR(64),
    original_size INT,
    sender VARCHAR(255) DEFAULT '',
    ephemeral TINYINT(1) DEFAULT 0,
    wisp_type VARCHAR(32) DEFAULT '',
    pinned TINYINT(1) DEFAULT 0,
    is_template TINYINT(1) DEFAULT 0,
    crystallizes TINYINT(1) DEFAULT 0,
    mol_type VARCHAR(32) DEFAULT '',
    work_type VARCHAR(32) DEFAULT 'mutex',
    quality_score DOUBLE,
    source_system VARCHAR(255) DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    source_repo VARCHAR(512) DEFAULT '',
    close_reason TEXT DEFAULT '',
    event_kind VARCHAR(32) DEFAULT '',
    actor VARCHAR(255) DEFAULT '',
    target VARCHAR(255) DEFAULT '',
    payload TEXT DEFAULT '',
    await_type VARCHAR(32) DEFAULT '',
    await_id VARCHAR(255) DEFAULT '',
    timeout_ns BIGINT DEFAULT 0,
    waiters TEXT DEFAULT '',
    hook_bead VARCHAR(255) DEFAULT '',
    role_bead VARCHAR(255) DEFAULT '',
    agent_state VARCHAR(32) DEFAULT '',
    last_activity DATETIME,
    role_type VARCHAR(32) DEFAULT '',
    rig VARCHAR(255) DEFAULT '',
    due_at DATETIME,
    defer_until DATETIME
)`

func init() {
	// Ensure test data directory exists
	_ = os.MkdirAll(os.TempDir(), 0o755)
}
