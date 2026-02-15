package fix

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStaleClosedIssues_NoDatabase(t *testing.T) {
	// Create temp directory with .beads but no database
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Should succeed without database
	err := StaleClosedIssues(tmpDir)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestStaleClosedIssues_NoBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Should fail without .beads directory
	err := StaleClosedIssues(tmpDir)
	if err == nil {
		t.Error("expected error for missing .beads directory")
	}
}
