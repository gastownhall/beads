package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIntegrityChecks_NoBeadsDir verifies all integrity check functions handle
// missing .beads directories gracefully. This replaces 4 individual subtests.
func TestIntegrityChecks_NoBeadsDir(t *testing.T) {
	checks := []struct {
		name     string
		fn       func(string) DoctorCheck
		wantName string
	}{
		{"IDFormat", CheckIDFormat, "Issue IDs"},
		{"DependencyCycles", CheckDependencyCycles, "Dependency Cycles"},
		{"Tombstones", CheckTombstones, "Tombstones"},
		{"DeletionsManifest", CheckDeletionsManifest, "Deletions Manifest"},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			result := tc.fn(tmpDir)

			if result.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", result.Name, tc.wantName)
			}
		})
	}
}

// TestIntegrityChecks_EmptyBeadsDir verifies all integrity check functions return OK
// when .beads directory exists but is empty (no database/files to check).
func TestIntegrityChecks_EmptyBeadsDir(t *testing.T) {
	checks := []struct {
		name string
		fn   func(string) DoctorCheck
	}{
		{"IDFormat", CheckIDFormat},
		{"DependencyCycles", CheckDependencyCycles},
		{"Tombstones", CheckTombstones},
		{"DeletionsManifest", CheckDeletionsManifest},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.Mkdir(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			result := tc.fn(tmpDir)

			if result.Status != StatusOK {
				t.Errorf("Status = %q, want %q", result.Status, StatusOK)
			}
		})
	}
}

// TestCheckDeletionsManifest_LegacyFile tests the specific case where a legacy
// deletions.jsonl file exists and should trigger a warning.
func TestCheckDeletionsManifest_LegacyFile(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a deletions.jsonl file
	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")
	if err := os.WriteFile(deletionsPath, []byte(`{"id":"test-1"}`), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckDeletionsManifest(tmpDir)

	// Should warn about legacy deletions file
	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
}
