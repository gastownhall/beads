//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// TestShimExtract_NoSQLite verifies the shim is a no-op when no SQLite DB exists.
func TestShimExtract_NoSQLite(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	doShimMigrate(beadsDir)
	// Should return without doing anything — no panic, no error
}

// TestShimExtract_DoltAlreadyExists verifies leftover SQLite is renamed when Dolt exists.
func TestShimExtract_DoltAlreadyExists(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}
	sqlitePath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(sqlitePath, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}

	doShimMigrate(beadsDir)

	// beads.db should be renamed to beads.db.migrated
	if _, err := os.Stat(sqlitePath); !os.IsNotExist(err) {
		t.Error("beads.db should have been renamed")
	}
	if _, err := os.Stat(sqlitePath + ".migrated"); err != nil {
		t.Errorf("beads.db.migrated should exist: %v", err)
	}
}

// TestShimExtract_CorruptedFile verifies graceful handling of a non-SQLite file.
func TestShimExtract_CorruptedFile(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	sqlitePath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(sqlitePath, []byte("this is not a sqlite database at all"), 0600); err != nil {
		t.Fatal(err)
	}

	doShimMigrate(beadsDir)

	// beads.db should still exist (migration failed gracefully)
	if _, err := os.Stat(sqlitePath); err != nil {
		t.Error("beads.db should still exist after failed migration")
	}
	// dolt/ should not exist
	if _, err := os.Stat(filepath.Join(beadsDir, "dolt")); !os.IsNotExist(err) {
		t.Error("dolt/ should not exist after failed migration")
	}
}

// TestShimExtract_QueryJSON verifies the sqlite3 CLI JSON extraction works.
func TestShimExtract_QueryJSON(t *testing.T) {
	// Create a real SQLite database using the CGO driver (for test setup)
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	sqlitePath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, sqlitePath, "shim", 3)

	// Test queryJSON
	rows, err := queryJSON(sqlitePath, "SELECT key, value FROM config")
	if err != nil {
		t.Fatalf("queryJSON failed: %v", err)
	}

	found := false
	for _, row := range rows {
		k, _ := row["key"].(string)
		v, _ := row["value"].(string)
		if k == "issue_prefix" && v == "shim" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected config row with key=issue_prefix, value=shim; got %v", rows)
	}
}

// TestShimExtract_ExtractViaSQLiteCLI verifies full extraction from SQLite via CLI.
func TestShimExtract_ExtractViaSQLiteCLI(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	sqlitePath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, sqlitePath, "ext2", 5)

	ctx := t.Context()
	data, err := extractViaSQLiteCLI(ctx, sqlitePath)
	if err != nil {
		t.Fatalf("extractViaSQLiteCLI failed: %v", err)
	}

	if data.prefix != "ext2" {
		t.Errorf("expected prefix 'ext2', got %q", data.prefix)
	}
	if data.issueCount != 5 {
		t.Errorf("expected 5 issues, got %d", data.issueCount)
	}
	if len(data.issues) != 5 {
		t.Errorf("expected 5 issues in slice, got %d", len(data.issues))
	}

	// Verify labels were loaded
	hasLabels := false
	for _, issue := range data.issues {
		if len(issue.Labels) > 0 {
			hasLabels = true
			break
		}
	}
	if !hasLabels {
		t.Error("expected at least one issue to have labels")
	}

	// Verify config was loaded
	if data.config["issue_prefix"] != "ext2" {
		t.Errorf("config should contain issue_prefix=ext2, got %v", data.config)
	}
}

// TestShimExtract_FullMigration does an end-to-end shim migration with a real Dolt server.
func TestShimExtract_FullMigration(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write metadata.json with server config so migration can connect
	cfg := &configfile.Config{
		Database:       "beads.db",
		Backend:        "sqlite",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to write test metadata.json: %v", err)
	}

	// Create SQLite database with test data (using CGO driver for setup)
	sqlitePath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, sqlitePath, "shimmig", 3)

	// Run shim migration
	doShimMigrate(beadsDir)

	// Verify: beads.db renamed
	if _, err := os.Stat(sqlitePath); !os.IsNotExist(err) {
		t.Error("beads.db should have been renamed to .migrated")
	}
	if _, err := os.Stat(sqlitePath + ".migrated"); err != nil {
		t.Errorf("beads.db.migrated should exist: %v", err)
	}

	// Verify: metadata.json updated
	updatedCfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("failed to load updated config: %v", err)
	}
	if updatedCfg.Backend != configfile.BackendDolt {
		t.Errorf("backend should be 'dolt', got %q", updatedCfg.Backend)
	}
	if updatedCfg.DoltDatabase != "shimmig" {
		t.Errorf("dolt_database should be 'shimmig', got %q", updatedCfg.DoltDatabase)
	}

	// Verify: config.yaml has sync.mode
	configYaml := filepath.Join(beadsDir, "config.yaml")
	if data, err := os.ReadFile(configYaml); err == nil {
		if !strings.Contains(string(data), "dolt-native") {
			t.Error("config.yaml should contain sync.mode = dolt-native")
		}
	}

	// Clean up Dolt test database
	dropTestDatabase("shimmig", testDoltServerPort)
}

// TestShimExtract_VerifySQLiteFile checks magic byte validation.
func TestShimExtract_VerifySQLiteFile(t *testing.T) {
	// Valid SQLite file
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	sqlitePath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, sqlitePath, "verify", 1)

	if err := verifySQLiteFile(sqlitePath); err != nil {
		t.Errorf("verifySQLiteFile should succeed for valid DB: %v", err)
	}

	// Invalid file
	badPath := filepath.Join(beadsDir, "bad.db")
	if err := os.WriteFile(badPath, []byte("not a database file at all!!!"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySQLiteFile(badPath); err == nil {
		t.Error("verifySQLiteFile should fail for non-SQLite file")
	}

	// Too-small file
	tinyPath := filepath.Join(beadsDir, "tiny.db")
	if err := os.WriteFile(tinyPath, []byte("hi"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySQLiteFile(tinyPath); err == nil {
		t.Error("verifySQLiteFile should fail for tiny file")
	}
}

// TestShimExtract_ParityWithCGO verifies that the shim extraction produces
// the same data as the CGO extractFromSQLite for the same database.
func TestShimExtract_ParityWithCGO(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	sqlitePath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, sqlitePath, "parity", 5)

	ctx := context.Background()

	// Extract via CGO
	cgoData, err := extractFromSQLite(ctx, sqlitePath)
	if err != nil {
		t.Fatalf("extractFromSQLite failed: %v", err)
	}

	// Extract via shim
	shimData, err := extractViaSQLiteCLI(ctx, sqlitePath)
	if err != nil {
		t.Fatalf("extractViaSQLiteCLI failed: %v", err)
	}

	// Compare counts
	if cgoData.issueCount != shimData.issueCount {
		t.Errorf("issue count mismatch: CGO=%d, shim=%d", cgoData.issueCount, shimData.issueCount)
	}
	if cgoData.prefix != shimData.prefix {
		t.Errorf("prefix mismatch: CGO=%q, shim=%q", cgoData.prefix, shimData.prefix)
	}
	if len(cgoData.labelsMap) != len(shimData.labelsMap) {
		t.Errorf("labels map size mismatch: CGO=%d, shim=%d", len(cgoData.labelsMap), len(shimData.labelsMap))
	}
	if len(cgoData.config) != len(shimData.config) {
		t.Errorf("config map size mismatch: CGO=%d, shim=%d", len(cgoData.config), len(shimData.config))
	}

	// Compare individual issues
	cgoIssues := make(map[string]string)
	for _, issue := range cgoData.issues {
		cgoIssues[issue.ID] = issue.Title
	}
	for _, issue := range shimData.issues {
		expected, ok := cgoIssues[issue.ID]
		if !ok {
			t.Errorf("shim has issue %s not found in CGO extraction", issue.ID)
			continue
		}
		if issue.Title != expected {
			t.Errorf("title mismatch for %s: CGO=%q, shim=%q", issue.ID, expected, issue.Title)
		}
	}
}

func TestShimExtract_OptionalFieldsAndOrdering(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	sqlitePath := filepath.Join(beadsDir, "beads.db")
	db, err := sql.Open("sqlite3", sqlitePath)
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		`CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			content_hash TEXT DEFAULT '',
			title TEXT DEFAULT '',
			description TEXT DEFAULT '',
			design TEXT DEFAULT '',
			acceptance_criteria TEXT DEFAULT '',
			notes TEXT DEFAULT '',
			status TEXT DEFAULT 'open',
			priority INTEGER DEFAULT 2,
			issue_type TEXT DEFAULT 'task',
			assignee TEXT DEFAULT '',
			estimated_minutes INTEGER,
			created_at TEXT DEFAULT '',
			created_by TEXT DEFAULT '',
			owner TEXT DEFAULT '',
			updated_at TEXT DEFAULT '',
			closed_at TEXT,
			external_ref TEXT,
			spec_id TEXT DEFAULT '',
			compaction_level INTEGER DEFAULT 0,
			compacted_at TEXT DEFAULT '',
			compacted_at_commit TEXT,
			original_size INTEGER DEFAULT 0,
			sender TEXT DEFAULT '',
			ephemeral INTEGER DEFAULT 0,
			wisp_type TEXT DEFAULT '',
			pinned INTEGER DEFAULT 0,
			is_template INTEGER DEFAULT 0,
			crystallizes INTEGER DEFAULT 0,
			mol_type TEXT DEFAULT '',
			work_type TEXT DEFAULT '',
			quality_score REAL,
			source_system TEXT DEFAULT '',
			source_repo TEXT DEFAULT '',
			close_reason TEXT DEFAULT '',
			closed_by_session TEXT DEFAULT '',
			event_kind TEXT DEFAULT '',
			actor TEXT DEFAULT '',
			target TEXT DEFAULT '',
			payload TEXT DEFAULT '',
			await_type TEXT DEFAULT '',
			await_id TEXT DEFAULT '',
			timeout_ns INTEGER DEFAULT 0,
			waiters TEXT DEFAULT '',
			hook_bead TEXT DEFAULT '',
			role_bead TEXT DEFAULT '',
			agent_state TEXT DEFAULT '',
			last_activity TEXT DEFAULT '',
			role_type TEXT DEFAULT '',
			rig TEXT DEFAULT '',
			due_at TEXT DEFAULT '',
			defer_until TEXT DEFAULT '',
			metadata TEXT DEFAULT '{}'
		)`,
		`CREATE TABLE labels (issue_id TEXT, label TEXT)`,
		`CREATE TABLE dependencies (
			issue_id TEXT,
			depends_on_id TEXT,
			type TEXT DEFAULT '',
			created_by TEXT DEFAULT '',
			created_at TEXT DEFAULT '',
			metadata TEXT DEFAULT '{}',
			thread_id TEXT DEFAULT ''
		)`,
		`CREATE TABLE events (
			issue_id TEXT,
			event_type TEXT DEFAULT '',
			actor TEXT DEFAULT '',
			old_value TEXT,
			new_value TEXT,
			comment TEXT,
			created_at TEXT DEFAULT ''
		)`,
		`CREATE TABLE comments (
			issue_id TEXT,
			author TEXT,
			text TEXT,
			created_at TEXT DEFAULT ''
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create test schema: %v", err)
		}
	}

	if _, err := db.Exec(`INSERT INTO config (key, value) VALUES ('issue_prefix', 'opt')`); err != nil {
		t.Fatalf("failed to insert config: %v", err)
	}

	ts := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO issues (
			id, title, created_at, updated_at, status, priority, issue_type,
			spec_id, wisp_type, closed_by_session, metadata
		) VALUES
		('opt-b', 'B issue', ?, ?, 'open', 2, 'task', 'spec-b', 'ping', '', '{"k":"b"}'),
		('opt-a', 'A issue', ?, ?, 'open', 2, 'task', 'spec-a', 'heartbeat', 'sess-123', '{"k":"a"}')
	`, ts, ts, ts, ts); err != nil {
		t.Fatalf("failed to insert issues: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO labels (issue_id, label) VALUES ('opt-a', 'alpha')`); err != nil {
		t.Fatalf("failed to insert label: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at, metadata, thread_id)
		VALUES ('opt-a', 'external:rig:42', 'blocks', 'tester', ?, '{"edge":"external"}', 'thread-1')
	`, ts); err != nil {
		t.Fatalf("failed to insert dependency: %v", err)
	}

	// Same created_at timestamp + insertion order should remain stable via ORDER BY created_at,rowid
	if _, err := db.Exec(`
		INSERT INTO events (issue_id, event_type, actor, comment, created_at)
		VALUES
		('opt-a', 'commented', 'actor-b', 'second logical', ?),
		('opt-a', 'commented', 'actor-a', 'first logical', ?)
	`, ts, ts); err != nil {
		t.Fatalf("failed to insert events: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO comments (issue_id, author, text, created_at)
		VALUES
		('opt-a', 'author-b', 'second', ?),
		('opt-a', 'author-a', 'first', ?)
	`, ts, ts); err != nil {
		t.Fatalf("failed to insert comments: %v", err)
	}

	ctx := context.Background()
	cgoData, err := extractFromSQLite(ctx, sqlitePath)
	if err != nil {
		t.Fatalf("extractFromSQLite failed: %v", err)
	}
	shimData, err := extractViaSQLiteCLI(ctx, sqlitePath)
	if err != nil {
		t.Fatalf("extractViaSQLiteCLI failed: %v", err)
	}

	if len(cgoData.issues) != 2 || len(shimData.issues) != 2 {
		t.Fatalf("unexpected issue counts cgo=%d shim=%d", len(cgoData.issues), len(shimData.issues))
	}
	if cgoData.issues[0].ID != "opt-a" || shimData.issues[0].ID != "opt-a" {
		t.Fatalf("issues are not ordered deterministically by id: cgo=%s shim=%s", cgoData.issues[0].ID, shimData.issues[0].ID)
	}

	var cgoIssueA, shimIssueA *types.Issue
	for _, issue := range cgoData.issues {
		if issue.ID == "opt-a" {
			cgoIssueA = issue
			break
		}
	}
	for _, issue := range shimData.issues {
		if issue.ID == "opt-a" {
			shimIssueA = issue
			break
		}
	}
	if cgoIssueA == nil || shimIssueA == nil {
		t.Fatal("opt-a issue not found in extracted data")
	}
	if cgoIssueA.SpecID != "spec-a" || shimIssueA.SpecID != "spec-a" {
		t.Fatalf("spec_id not preserved: cgo=%q shim=%q", cgoIssueA.SpecID, shimIssueA.SpecID)
	}
	if cgoIssueA.WispType != "heartbeat" || shimIssueA.WispType != "heartbeat" {
		t.Fatalf("wisp_type not preserved: cgo=%q shim=%q", cgoIssueA.WispType, shimIssueA.WispType)
	}
	if cgoIssueA.ClosedBySession != "sess-123" || shimIssueA.ClosedBySession != "sess-123" {
		t.Fatalf("closed_by_session not preserved: cgo=%q shim=%q", cgoIssueA.ClosedBySession, shimIssueA.ClosedBySession)
	}
	if !json.Valid(cgoIssueA.Metadata) || !json.Valid(shimIssueA.Metadata) {
		t.Fatal("metadata is not valid json")
	}

	cgoDeps := cgoData.depsMap["opt-a"]
	shimDeps := shimData.depsMap["opt-a"]
	if len(cgoDeps) != 1 || len(shimDeps) != 1 {
		t.Fatalf("dependency count mismatch: cgo=%d shim=%d", len(cgoDeps), len(shimDeps))
	}
	if cgoDeps[0].ThreadID != "thread-1" || shimDeps[0].ThreadID != "thread-1" {
		t.Fatalf("thread_id not preserved: cgo=%q shim=%q", cgoDeps[0].ThreadID, shimDeps[0].ThreadID)
	}
	if cgoDeps[0].Metadata != `{"edge":"external"}` || shimDeps[0].Metadata != `{"edge":"external"}` {
		t.Fatalf("dependency metadata not preserved: cgo=%q shim=%q", cgoDeps[0].Metadata, shimDeps[0].Metadata)
	}

	cgoEvents := cgoData.eventsMap["opt-a"]
	shimEvents := shimData.eventsMap["opt-a"]
	if len(cgoEvents) != 2 || len(shimEvents) != 2 {
		t.Fatalf("event count mismatch: cgo=%d shim=%d", len(cgoEvents), len(shimEvents))
	}
	if cgoEvents[0].Actor != "actor-b" || shimEvents[0].Actor != "actor-b" {
		t.Fatalf("event ordering mismatch: cgo_first=%q shim_first=%q", cgoEvents[0].Actor, shimEvents[0].Actor)
	}

	cgoComments := cgoData.commentsMap["opt-a"]
	shimComments := shimData.commentsMap["opt-a"]
	if len(cgoComments) != 2 || len(shimComments) != 2 {
		t.Fatalf("comment count mismatch: cgo=%d shim=%d", len(cgoComments), len(shimComments))
	}
	if cgoComments[0].Author != "author-b" || shimComments[0].Author != "author-b" {
		t.Fatalf("comment ordering mismatch: cgo_first=%q shim_first=%q", cgoComments[0].Author, shimComments[0].Author)
	}
}
