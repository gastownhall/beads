package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckStaleClosedIssues_NoDatabase(t *testing.T) {
	// Create temp directory with .beads but no database
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	check := CheckStaleClosedIssues(tmpDir)

	if check.Name != "Stale Closed Issues" {
		t.Errorf("expected name 'Stale Closed Issues', got %q", check.Name)
	}
	if check.Status != StatusOK {
		t.Errorf("expected status OK, got %q", check.Status)
	}
	if check.Category != CategoryMaintenance {
		t.Errorf("expected category 'Maintenance', got %q", check.Category)
	}
}

func TestCheckExpiredTombstones_NoJSONL(t *testing.T) {
	// Create temp directory with .beads but no JSONL
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	check := CheckExpiredTombstones(tmpDir)

	if check.Name != "Expired Tombstones" {
		t.Errorf("expected name 'Expired Tombstones', got %q", check.Name)
	}
	if check.Status != StatusOK {
		t.Errorf("expected status OK, got %q", check.Status)
	}
	if check.Category != CategoryMaintenance {
		t.Errorf("expected category 'Maintenance', got %q", check.Category)
	}
}

func TestCheckExpiredTombstones_EmptyJSONL(t *testing.T) {
	// Create temp directory with .beads and empty JSONL
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to create issues.jsonl: %v", err)
	}

	check := CheckExpiredTombstones(tmpDir)

	if check.Name != "Expired Tombstones" {
		t.Errorf("expected name 'Expired Tombstones', got %q", check.Name)
	}
	if check.Status != StatusOK {
		t.Errorf("expected status OK, got %q", check.Status)
	}
}

func TestCheckCompactionCandidates_NoDatabase(t *testing.T) {
	// Create temp directory with .beads but no database
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	check := CheckCompactionCandidates(tmpDir)

	if check.Name != "Compaction Candidates" {
		t.Errorf("expected name 'Compaction Candidates', got %q", check.Name)
	}
	if check.Status != StatusOK {
		t.Errorf("expected status OK, got %q", check.Status)
	}
	if check.Category != CategoryMaintenance {
		t.Errorf("expected category 'Maintenance', got %q", check.Category)
	}
}

func TestCheckStaleMQFiles_NoMQDirectory(t *testing.T) {
	// Create temp directory with .beads but no mq subdirectory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	check := CheckStaleMQFiles(tmpDir)

	if check.Name != "Legacy MQ Files" {
		t.Errorf("expected name 'Legacy MQ Files', got %q", check.Name)
	}
	if check.Status != StatusOK {
		t.Errorf("expected status OK, got %q", check.Status)
	}
	if check.Message != "No legacy merge queue files" {
		t.Errorf("expected message about no legacy files, got %q", check.Message)
	}
	if check.Category != CategoryMaintenance {
		t.Errorf("expected category 'Maintenance', got %q", check.Category)
	}
}

func TestCheckStaleMQFiles_EmptyMQDirectory(t *testing.T) {
	// Create temp directory with .beads/mq but no JSON files
	tmpDir := t.TempDir()
	mqDir := filepath.Join(tmpDir, ".beads", "mq")
	if err := os.MkdirAll(mqDir, 0755); err != nil {
		t.Fatalf("failed to create .beads/mq dir: %v", err)
	}

	check := CheckStaleMQFiles(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected status OK for empty mq dir, got %q", check.Status)
	}
}

func TestCheckStaleMQFiles_WithJSONFiles(t *testing.T) {
	// Create temp directory with .beads/mq containing JSON files
	tmpDir := t.TempDir()
	mqDir := filepath.Join(tmpDir, ".beads", "mq")
	if err := os.MkdirAll(mqDir, 0755); err != nil {
		t.Fatalf("failed to create .beads/mq dir: %v", err)
	}

	// Create some stale MQ files
	for _, name := range []string{"mr-abc123.json", "mr-def456.json"} {
		path := filepath.Join(mqDir, name)
		if err := os.WriteFile(path, []byte(`{"id":"test"}`), 0644); err != nil {
			t.Fatalf("failed to create %s: %v", name, err)
		}
	}

	check := CheckStaleMQFiles(tmpDir)

	if check.Name != "Legacy MQ Files" {
		t.Errorf("expected name 'Legacy MQ Files', got %q", check.Name)
	}
	if check.Status != StatusWarning {
		t.Errorf("expected status Warning for mq dir with files, got %q", check.Status)
	}
	if check.Message != "2 stale .beads/mq/*.json file(s)" {
		t.Errorf("expected message about 2 stale files, got %q", check.Message)
	}
	if check.Fix == "" {
		t.Error("expected fix message to be present")
	}
}

func TestFixStaleMQFiles_RemovesDirectory(t *testing.T) {
	// Create temp directory with .beads/mq containing JSON files
	tmpDir := t.TempDir()
	mqDir := filepath.Join(tmpDir, ".beads", "mq")
	if err := os.MkdirAll(mqDir, 0755); err != nil {
		t.Fatalf("failed to create .beads/mq dir: %v", err)
	}

	// Create a stale MQ file
	path := filepath.Join(mqDir, "mr-abc123.json")
	if err := os.WriteFile(path, []byte(`{"id":"test"}`), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	// Verify directory exists before fix
	if _, err := os.Stat(mqDir); os.IsNotExist(err) {
		t.Fatal("mq directory should exist before fix")
	}

	// Apply the fix
	if err := FixStaleMQFiles(tmpDir); err != nil {
		t.Fatalf("FixStaleMQFiles failed: %v", err)
	}

	// Verify directory is removed after fix
	if _, err := os.Stat(mqDir); !os.IsNotExist(err) {
		t.Error("mq directory should not exist after fix")
	}

	// Verify check now passes
	check := CheckStaleMQFiles(tmpDir)
	if check.Status != StatusOK {
		t.Errorf("expected status OK after fix, got %q", check.Status)
	}
}

func TestFixStaleMQFiles_NoDirectory(t *testing.T) {
	// Create temp directory with .beads but no mq subdirectory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Fix should succeed even if directory doesn't exist
	if err := FixStaleMQFiles(tmpDir); err != nil {
		t.Fatalf("FixStaleMQFiles should not fail when directory doesn't exist: %v", err)
	}
}
