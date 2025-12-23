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
