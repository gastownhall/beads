package fix

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestDatabaseConfigFix_JSONLMismatch tests that DatabaseConfig fixes JSONL mismatches.
// bd-6xd: Verify auto-fix for metadata.json jsonl_export mismatch
func TestDatabaseConfigFix_JSONLMismatch(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create issues.jsonl file (actual JSONL - canonical name)
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-123"}`), 0644); err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}

	// Create metadata.json with wrong JSONL filename (beads.jsonl)
	cfg := &configfile.Config{
		Database:    "beads.db",
		JSONLExport: "beads.jsonl", // Wrong - should be issues.jsonl
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Run the fix
	if err := DatabaseConfig(tmpDir); err != nil {
		t.Fatalf("DatabaseConfig failed: %v", err)
	}

	// Verify the config was updated
	updatedCfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("Failed to load updated config: %v", err)
	}

	if updatedCfg.JSONLExport != "issues.jsonl" {
		t.Errorf("Expected JSONLExport to be 'issues.jsonl', got %q", updatedCfg.JSONLExport)
	}
}

// TestDatabaseConfigFix_PrefersIssuesJSONL tests that DatabaseConfig prefers issues.jsonl over beads.jsonl.
// bd-6xd: issues.jsonl is the canonical filename
func TestDatabaseConfigFix_PrefersIssuesJSONL(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create both beads.jsonl and issues.jsonl
	beadsJSONL := filepath.Join(beadsDir, "beads.jsonl")
	if err := os.WriteFile(beadsJSONL, []byte(`{"id":"test-123"}`), 0644); err != nil {
		t.Fatalf("Failed to create beads.jsonl: %v", err)
	}

	issuesJSONL := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesJSONL, []byte(`{"id":"test-456"}`), 0644); err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}

	// Create metadata.json with wrong JSONL filename (old.jsonl)
	cfg := &configfile.Config{
		Database:    "beads.db",
		JSONLExport: "old.jsonl", // Wrong - should prefer issues.jsonl
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Run the fix
	if err := DatabaseConfig(tmpDir); err != nil {
		t.Fatalf("DatabaseConfig failed: %v", err)
	}

	// Verify the config was updated to issues.jsonl (canonical name)
	updatedCfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("Failed to load updated config: %v", err)
	}

	if updatedCfg.JSONLExport != "issues.jsonl" {
		t.Errorf("Expected JSONLExport to be 'issues.jsonl', got %q", updatedCfg.JSONLExport)
	}
}

// TestFindActualJSONLFile_SkipsBackups tests that backup files are skipped.
// bd-6xd: issues.jsonl is the canonical filename
func TestFindActualJSONLFile_SkipsBackups(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()

	// Create issues.jsonl and various backup files
	files := []string{
		"issues.jsonl",
		"issues.jsonl.backup",
		"backup_issues.jsonl",
		"issues.jsonl.orig",
		"issues.jsonl.bak",
		"issues.jsonl~",
	}

	for _, name := range files {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(`{"id":"test"}`), 0644); err != nil {
			t.Fatalf("Failed to create %s: %v", name, err)
		}
	}

	// findActualJSONLFile should return issues.jsonl (not backups)
	result := findActualJSONLFile(tmpDir)
	if result != "issues.jsonl" {
		t.Errorf("Expected 'issues.jsonl', got %q", result)
	}
}

// TestLegacyJSONLConfig_MigratesBeadsToIssues tests migration from beads.jsonl to issues.jsonl.
// bd-6xd: issues.jsonl is the canonical filename
func TestLegacyJSONLConfig_MigratesBeadsToIssues(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create beads.jsonl file (legacy name)
	legacyPath := filepath.Join(beadsDir, "beads.jsonl")
	if err := os.WriteFile(legacyPath, []byte(`{"id":"test-123"}`), 0644); err != nil {
		t.Fatalf("Failed to create beads.jsonl: %v", err)
	}

	// Create metadata.json with legacy filename
	cfg := &configfile.Config{
		Database:    "beads.db",
		JSONLExport: "beads.jsonl",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Run the fix
	if err := LegacyJSONLConfig(tmpDir); err != nil {
		t.Fatalf("LegacyJSONLConfig failed: %v", err)
	}

	// Verify the file was renamed
	canonicalPath := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(canonicalPath); os.IsNotExist(err) {
		t.Error("Expected issues.jsonl to exist after migration")
	}
	if _, err := os.Stat(legacyPath); err == nil {
		t.Error("Expected beads.jsonl to be removed after migration")
	}

	// Verify the config was updated
	updatedCfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("Failed to load updated config: %v", err)
	}

	if updatedCfg.JSONLExport != "issues.jsonl" {
		t.Errorf("Expected JSONLExport to be 'issues.jsonl', got %q", updatedCfg.JSONLExport)
	}
}

// TestLegacyJSONLConfig_UpdatesGitattributes tests that .gitattributes is updated during migration.
func TestLegacyJSONLConfig_UpdatesGitattributes(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create beads.jsonl file (legacy name)
	legacyPath := filepath.Join(beadsDir, "beads.jsonl")
	if err := os.WriteFile(legacyPath, []byte(`{"id":"test-123"}`), 0644); err != nil {
		t.Fatalf("Failed to create beads.jsonl: %v", err)
	}

	// Create .gitattributes with legacy reference
	gitattrsPath := filepath.Join(tmpDir, ".gitattributes")
	if err := os.WriteFile(gitattrsPath, []byte(".beads/beads.jsonl merge=beads\n"), 0644); err != nil {
		t.Fatalf("Failed to create .gitattributes: %v", err)
	}

	// Create metadata.json with legacy filename
	cfg := &configfile.Config{
		Database:    "beads.db",
		JSONLExport: "beads.jsonl",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Run the fix
	if err := LegacyJSONLConfig(tmpDir); err != nil {
		t.Fatalf("LegacyJSONLConfig failed: %v", err)
	}

	// Verify .gitattributes was updated
	content, err := os.ReadFile(gitattrsPath)
	if err != nil {
		t.Fatalf("Failed to read .gitattributes: %v", err)
	}

	if string(content) != ".beads/issues.jsonl merge=beads\n" {
		t.Errorf("Expected .gitattributes to reference issues.jsonl, got: %q", string(content))
	}
}

// TestFindActualJSONLFile_SkipsSystemFiles ensures system JSONL files are never treated as JSONL exports.
func TestFindActualJSONLFile_SkipsSystemFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Only system files → no candidates.
	if err := os.WriteFile(filepath.Join(tmpDir, "interactions.jsonl"), []byte(`{"id":"x"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if got := findActualJSONLFile(tmpDir); got != "" {
		t.Fatalf("expected empty result, got %q", got)
	}

	// System + legacy export → legacy wins.
	if err := os.WriteFile(filepath.Join(tmpDir, "beads.jsonl"), []byte(`{"id":"x"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if got := findActualJSONLFile(tmpDir); got != "beads.jsonl" {
		t.Fatalf("expected beads.jsonl, got %q", got)
	}
}

func TestDatabaseConfigFix_RejectsSystemJSONLExport(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(beadsDir, "interactions.jsonl"), []byte(`{"id":"x"}`), 0644); err != nil {
		t.Fatalf("Failed to create interactions.jsonl: %v", err)
	}

	cfg := &configfile.Config{Database: "beads.db", JSONLExport: "interactions.jsonl"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	if err := DatabaseConfig(tmpDir); err != nil {
		t.Fatalf("DatabaseConfig failed: %v", err)
	}

	updated, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("Failed to load updated config: %v", err)
	}
	if updated.JSONLExport != "issues.jsonl" {
		t.Fatalf("expected issues.jsonl, got %q", updated.JSONLExport)
	}
}
