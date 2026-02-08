package fix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// createDoltWorkspace creates a test workspace configured for the Dolt backend.
// Returns the workspace root path.
func createDoltWorkspace(t *testing.T) string {
	t.Helper()
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")

	// Write metadata.json for Dolt backend
	cfg := &configfile.Config{
		Backend:  configfile.BackendDolt,
		Database: "dolt",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save metadata.json: %v", err)
	}

	return dir
}

// createDoltDir creates a fake dolt directory to simulate a corrupted database.
func createDoltDir(t *testing.T, dir string) string {
	t.Helper()
	doltPath := filepath.Join(dir, ".beads", "dolt")
	if err := os.MkdirAll(doltPath, 0755); err != nil {
		t.Fatalf("failed to create dolt directory: %v", err)
	}
	// Create a fake .dolt subdir to look like a real dolt database
	dotDolt := filepath.Join(doltPath, "beads_test", ".dolt")
	if err := os.MkdirAll(dotDolt, 0755); err != nil {
		t.Fatalf("failed to create .dolt directory: %v", err)
	}
	// Write a corrupt marker file
	if err := os.WriteFile(filepath.Join(dotDolt, "noms"), []byte("corrupt"), 0600); err != nil {
		t.Fatalf("failed to create corrupt marker: %v", err)
	}
	return doltPath
}

// createTestJSONL creates a JSONL file with test issues.
func createTestJSONL(t *testing.T, dir string, issueCount int) string {
	t.Helper()
	jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")

	var lines []byte
	for i := 0; i < issueCount; i++ {
		issue := map[string]interface{}{
			"id":     "test-" + string(rune('a'+i)),
			"title":  "Test Issue",
			"status": "open",
		}
		data, _ := json.Marshal(issue)
		lines = append(lines, data...)
		lines = append(lines, '\n')
	}

	if err := os.WriteFile(jsonlPath, lines, 0644); err != nil {
		t.Fatalf("failed to create JSONL: %v", err)
	}
	return jsonlPath
}

func TestDoltCorruptionRecovery_NoJSONL(t *testing.T) {
	dir := createDoltWorkspace(t)
	createDoltDir(t, dir)

	beadsDir := filepath.Join(dir, ".beads")
	err := doltCorruptionRecovery(dir, beadsDir)
	if err == nil {
		t.Fatal("expected error when no JSONL exists")
	}
	if !strings.Contains(err.Error(), "no JSONL backup found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoltCorruptionRecovery_EmptyJSONL(t *testing.T) {
	dir := createDoltWorkspace(t)
	createDoltDir(t, dir)

	// Create empty JSONL
	jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create JSONL: %v", err)
	}

	beadsDir := filepath.Join(dir, ".beads")
	err := doltCorruptionRecovery(dir, beadsDir)
	if err == nil {
		t.Fatal("expected error when JSONL is empty")
	}
	if !strings.Contains(err.Error(), "JSONL is empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoltCorruptionRecovery_NoDoltDir(t *testing.T) {
	dir := createDoltWorkspace(t)

	beadsDir := filepath.Join(dir, ".beads")
	err := doltCorruptionRecovery(dir, beadsDir)
	if err == nil {
		t.Fatal("expected error when no dolt directory exists")
	}
	if !strings.Contains(err.Error(), "no Dolt database to recover") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoltCorruptionRecovery_BackupCreated(t *testing.T) {
	// This test validates that backup is created before recovery attempt.
	// The actual bd init will fail (test binary), but we verify the backup logic.
	dir := createDoltWorkspace(t)
	doltPath := createDoltDir(t, dir)
	createTestJSONL(t, dir, 3)

	beadsDir := filepath.Join(dir, ".beads")
	err := doltCorruptionRecovery(dir, beadsDir)

	// Will fail because we're running as test binary, but backup should exist
	if err == nil {
		t.Skip("bd init succeeded in test mode - cannot verify backup logic")
	}

	// Check that the original dolt directory was restored (rollback on failure)
	if _, statErr := os.Stat(doltPath); os.IsNotExist(statErr) {
		t.Error("dolt directory should be restored after recovery failure")
	}
}

func TestDoltCorruptionRecoveryWithOptions_ForceAndSourceDB(t *testing.T) {
	dir := createDoltWorkspace(t)
	createDoltDir(t, dir)
	createTestJSONL(t, dir, 3)

	beadsDir := filepath.Join(dir, ".beads")
	err := doltCorruptionRecoveryWithOptions(dir, beadsDir, true, "db")
	if err == nil {
		t.Fatal("expected error for --force --source=db contradiction")
	}
	if !strings.Contains(err.Error(), "contradictory") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoltCorruptionRecoveryWithOptions_SourceDBNoDatabase(t *testing.T) {
	dir := createDoltWorkspace(t)
	// No dolt directory created

	beadsDir := filepath.Join(dir, ".beads")
	err := doltCorruptionRecoveryWithOptions(dir, beadsDir, false, "db")
	if err == nil {
		t.Fatal("expected error when --source=db but no database")
	}
	if !strings.Contains(err.Error(), "no Dolt database found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoltCorruptionRecoveryWithOptions_SourceJSONLNoFile(t *testing.T) {
	dir := createDoltWorkspace(t)
	createDoltDir(t, dir)

	beadsDir := filepath.Join(dir, ".beads")
	err := doltCorruptionRecoveryWithOptions(dir, beadsDir, false, "jsonl")
	if err == nil {
		t.Fatal("expected error when --source=jsonl but no JSONL")
	}
	if !strings.Contains(err.Error(), "no JSONL file found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoltCorruptionRecoveryWithOptions_InvalidSource(t *testing.T) {
	dir := createDoltWorkspace(t)
	createDoltDir(t, dir)

	beadsDir := filepath.Join(dir, ".beads")
	err := doltCorruptionRecoveryWithOptions(dir, beadsDir, false, "invalid")
	if err == nil {
		t.Fatal("expected error for invalid --source value")
	}
	if !strings.Contains(err.Error(), "invalid --source value") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoltCorruptionRecoveryWithOptions_SourceDB_SkipsRecovery(t *testing.T) {
	dir := createDoltWorkspace(t)
	createDoltDir(t, dir)
	createTestJSONL(t, dir, 3)

	beadsDir := filepath.Join(dir, ".beads")
	err := doltCorruptionRecoveryWithOptions(dir, beadsDir, false, "db")
	if err != nil {
		t.Errorf("expected no error when source=db (skip recovery), got: %v", err)
	}
}

func TestDoltCorruptionRecoveryWithOptions_AutoNeitherExists(t *testing.T) {
	dir := createDoltWorkspace(t)
	// No dolt dir, no JSONL

	beadsDir := filepath.Join(dir, ".beads")
	err := doltCorruptionRecoveryWithOptions(dir, beadsDir, false, "auto")
	if err == nil {
		t.Fatal("expected error when neither exists")
	}
	if !strings.Contains(err.Error(), "neither Dolt database nor JSONL found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoltCorruptionRecoveryWithOptions_ForceNoJSONL(t *testing.T) {
	dir := createDoltWorkspace(t)
	createDoltDir(t, dir)

	beadsDir := filepath.Join(dir, ".beads")
	err := doltCorruptionRecoveryWithOptions(dir, beadsDir, true, "auto")
	if err == nil {
		t.Fatal("expected error when --force but no JSONL")
	}
	if !strings.Contains(err.Error(), "JSONL for recovery but no JSONL file found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDatabaseCorruptionRecovery_RoutesDolt(t *testing.T) {
	dir := createDoltWorkspace(t)
	// No dolt dir or JSONL - should hit doltCorruptionRecovery and fail

	err := databaseCorruptionRecovery(dir)
	if err == nil {
		t.Fatal("expected error for Dolt recovery without resources")
	}
	// Should reach Dolt-specific error, not "skipped (dolt backend)"
	if strings.Contains(err.Error(), "skipped") {
		t.Errorf("should not skip Dolt recovery anymore: %v", err)
	}
}

func TestDatabaseCorruptionRecoveryWithOptions_RoutesDolt(t *testing.T) {
	dir := createDoltWorkspace(t)
	// No dolt dir or JSONL - should hit doltCorruptionRecoveryWithOptions

	err := DatabaseCorruptionRecoveryWithOptions(dir, false, "auto")
	if err == nil {
		t.Fatal("expected error for Dolt recovery without resources")
	}
	// Should reach Dolt-specific error, not "skipped (dolt backend)"
	if strings.Contains(err.Error(), "skipped") {
		t.Errorf("should not skip Dolt recovery anymore: %v", err)
	}
}

func TestDatabaseIntegrity_RoutesDolt(t *testing.T) {
	dir := createDoltWorkspace(t)
	// No dolt dir - should reach doltCorruptionRecovery

	err := DatabaseIntegrity(dir)
	if err == nil {
		t.Fatal("expected error for Dolt recovery without resources")
	}
	// Should reach Dolt-specific error, not generic SQLite code path
	if strings.Contains(err.Error(), "no JSONL export found") {
		t.Errorf("should not hit SQLite code path for Dolt workspace: %v", err)
	}
}
