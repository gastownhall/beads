//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
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
