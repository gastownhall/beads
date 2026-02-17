package beads_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads"
)

func TestOpen(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-dolt")

	ctx := context.Background()
	store, err := beads.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	if store == nil {
		t.Error("expected non-nil storage")
	}
}

func TestFindDatabasePath(t *testing.T) {
	// This will return empty string in test environment without a database
	path := beads.FindDatabasePath()
	// Just verify it doesn't panic
	_ = path
}

func TestFindBeadsDir(t *testing.T) {
	// This will return empty string or a valid path
	dir := beads.FindBeadsDir()
	// Just verify it doesn't panic
	_ = dir
}

func TestFindJSONLPath(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "beads.db")

	// Create the directory
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	jsonlPath := beads.FindJSONLPath(dbPath)
	// bd-6xd: Default is now issues.jsonl (canonical name)
	expectedPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")

	if jsonlPath != expectedPath {
		t.Errorf("FindJSONLPath returned %s, expected %s", jsonlPath, expectedPath)
	}
}

func TestOpenFromConfig_Embedded(t *testing.T) {
	// Create a .beads dir with metadata.json configured for embedded mode
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	metadata := `{"backend":"dolt","database":"dolt","dolt_database":"testdb","dolt_mode":"embedded"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	ctx := context.Background()
	store, err := beads.OpenFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("OpenFromConfig (embedded) failed: %v", err)
	}
	defer store.Close()

	if store == nil {
		t.Error("expected non-nil storage")
	}
}

func TestOpenFromConfig_DefaultsToEmbedded(t *testing.T) {
	// metadata.json without dolt_mode should default to embedded
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	metadata := `{"backend":"dolt","database":"dolt"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	ctx := context.Background()
	store, err := beads.OpenFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("OpenFromConfig (default) failed: %v", err)
	}
	defer store.Close()

	if store == nil {
		t.Error("expected non-nil storage")
	}
}

func TestOpenFromConfig_ServerModeFailsWithoutServer(t *testing.T) {
	// Server mode should fail-fast when no server is listening
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Use a port that's very unlikely to have a server
	metadata := `{"backend":"dolt","database":"dolt","dolt_mode":"server","dolt_server_host":"127.0.0.1","dolt_server_port":39999}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	ctx := context.Background()
	_, err := beads.OpenFromConfig(ctx, beadsDir)
	if err == nil {
		t.Fatal("OpenFromConfig (server mode) should fail when no server is running")
	}
	// Should contain "unreachable" from the fail-fast TCP check
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("expected 'unreachable' in error, got: %v", err)
	}
}

func TestOpenFromConfig_NoMetadata(t *testing.T) {
	// Missing metadata.json should use defaults (embedded mode)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	ctx := context.Background()
	store, err := beads.OpenFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("OpenFromConfig (no metadata) failed: %v", err)
	}
	defer store.Close()

	if store == nil {
		t.Error("expected non-nil storage")
	}
}

func TestFindAllDatabases(t *testing.T) {
	// This scans the file system, just verify it doesn't panic
	dbs := beads.FindAllDatabases()
	// Should return a slice (possibly empty)
	if dbs == nil {
		t.Error("expected non-nil slice")
	}
}

// Test that exported constants have correct values
func TestConstants(t *testing.T) {
	// Status constants
	if beads.StatusOpen != "open" {
		t.Errorf("StatusOpen = %q, want %q", beads.StatusOpen, "open")
	}
	if beads.StatusInProgress != "in_progress" {
		t.Errorf("StatusInProgress = %q, want %q", beads.StatusInProgress, "in_progress")
	}
	if beads.StatusBlocked != "blocked" {
		t.Errorf("StatusBlocked = %q, want %q", beads.StatusBlocked, "blocked")
	}
	if beads.StatusClosed != "closed" {
		t.Errorf("StatusClosed = %q, want %q", beads.StatusClosed, "closed")
	}

	// IssueType constants
	if beads.TypeBug != "bug" {
		t.Errorf("TypeBug = %q, want %q", beads.TypeBug, "bug")
	}
	if beads.TypeFeature != "feature" {
		t.Errorf("TypeFeature = %q, want %q", beads.TypeFeature, "feature")
	}
	if beads.TypeTask != "task" {
		t.Errorf("TypeTask = %q, want %q", beads.TypeTask, "task")
	}
	if beads.TypeEpic != "epic" {
		t.Errorf("TypeEpic = %q, want %q", beads.TypeEpic, "epic")
	}

	// DependencyType constants
	if beads.DepBlocks != "blocks" {
		t.Errorf("DepBlocks = %q, want %q", beads.DepBlocks, "blocks")
	}
	if beads.DepRelated != "related" {
		t.Errorf("DepRelated = %q, want %q", beads.DepRelated, "related")
	}
}
