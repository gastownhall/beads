package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorNoBeadsDir(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()

	// Run diagnostics
	result := runDiagnostics(tmpDir)

	// Should fail overall
	if result.OverallOK {
		t.Error("Expected OverallOK to be false when .beads/ directory is missing")
	}

	// Check installation check failed
	if len(result.Checks) == 0 {
		t.Fatal("Expected at least one check")
	}

	installCheck := result.Checks[0]
	if installCheck.Name != "Installation" {
		t.Errorf("Expected first check to be Installation, got %s", installCheck.Name)
	}
	if installCheck.Status != "error" {
		t.Errorf("Expected Installation status to be error, got %s", installCheck.Status)
	}
	if installCheck.Fix == "" {
		t.Error("Expected Installation check to have a fix")
	}
}

func TestDoctorWithBeadsDir(t *testing.T) {
	// Create temporary directory with .beads
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Run diagnostics
	result := runDiagnostics(tmpDir)

	// Should have installation check passing
	if len(result.Checks) == 0 {
		t.Fatal("Expected at least one check")
	}

	installCheck := result.Checks[0]
	if installCheck.Name != "Installation" {
		t.Errorf("Expected first check to be Installation, got %s", installCheck.Name)
	}
	if installCheck.Status != "ok" {
		t.Errorf("Expected Installation status to be ok, got %s", installCheck.Status)
	}
}

func TestDoctorJSONOutput(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()

	// Run diagnostics
	result := runDiagnostics(tmpDir)

	// Marshal to JSON to verify structure
	jsonBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal result to JSON: %v", err)
	}

	// Unmarshal back to verify structure
	var decoded doctorResult
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	// Verify key fields
	if decoded.Path != result.Path {
		t.Errorf("Path mismatch: %s != %s", decoded.Path, result.Path)
	}
	if decoded.CLIVersion != result.CLIVersion {
		t.Errorf("CLIVersion mismatch: %s != %s", decoded.CLIVersion, result.CLIVersion)
	}
	if decoded.OverallOK != result.OverallOK {
		t.Errorf("OverallOK mismatch: %v != %v", decoded.OverallOK, result.OverallOK)
	}
	if len(decoded.Checks) != len(result.Checks) {
		t.Errorf("Checks length mismatch: %d != %d", len(decoded.Checks), len(result.Checks))
	}
}

// Note: isHashID is tested in migrate_hash_ids_test.go

func TestCheckInstallation(t *testing.T) {
	// Test with missing .beads directory
	tmpDir := t.TempDir()
	check := checkInstallation(tmpDir)

	if check.Status != statusError {
		t.Errorf("Expected error status, got %s", check.Status)
	}
	if check.Fix == "" {
		t.Error("Expected fix to be provided")
	}

	// Test with existing .beads directory
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	check = checkInstallation(tmpDir)
	if check.Status != statusOK {
		t.Errorf("Expected ok status, got %s", check.Status)
	}
}

func TestCheckDatabaseVersionJSONLMode(t *testing.T) {
	// Create temporary directory with .beads but no database
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Create empty issues.jsonl to simulate --no-db mode
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	check := checkDatabaseVersion(tmpDir)

	if check.Status != statusOK {
		t.Errorf("Expected ok status for JSONL mode, got %s", check.Status)
	}
	if check.Message != "JSONL-only mode" {
		t.Errorf("Expected JSONL-only mode message, got %s", check.Message)
	}
	if check.Detail == "" {
		t.Error("Expected detail field to be set for JSONL mode")
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected int
	}{
		{"0.20.1", "0.20.1", 0},   // Equal
		{"0.20.1", "0.20.0", 1},   // v1 > v2
		{"0.20.0", "0.20.1", -1},  // v1 < v2
		{"0.10.0", "0.9.9", 1},    // Major.minor comparison
		{"1.0.0", "0.99.99", 1},   // Major version difference
		{"0.20.1", "0.3.0", 1},    // String comparison would fail this
		{"1.2", "1.2.0", 0},       // Different length, equal
		{"1.2.1", "1.2", 1},       // Different length, v1 > v2
	}

	for _, tc := range tests {
		result := compareVersions(tc.v1, tc.v2)
		if result != tc.expected {
			t.Errorf("compareVersions(%q, %q) = %d, expected %d", tc.v1, tc.v2, result, tc.expected)
		}
	}
}
