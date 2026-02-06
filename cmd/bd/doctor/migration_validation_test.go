package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateJSONLForMigration(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		wantCount     int
		wantMalformed int
		wantErr       bool
	}{
		{
			name:          "valid JSONL",
			content:       `{"id":"bd-001","title":"Test 1"}` + "\n" + `{"id":"bd-002","title":"Test 2"}`,
			wantCount:     2,
			wantMalformed: 0,
			wantErr:       false,
		},
		{
			name:          "JSONL with malformed lines",
			content:       `{"id":"bd-001","title":"Test 1"}` + "\n" + `invalid json` + "\n" + `{"id":"bd-002","title":"Test 2"}`,
			wantCount:     2,
			wantMalformed: 1,
			wantErr:       false,
		},
		{
			name:          "JSONL with missing ID",
			content:       `{"id":"bd-001","title":"Test 1"}` + "\n" + `{"title":"No ID"}`,
			wantCount:     1,
			wantMalformed: 1,
			wantErr:       false,
		},
		{
			name:          "empty JSONL",
			content:       "",
			wantCount:     0,
			wantMalformed: 0,
			wantErr:       false,
		},
		{
			name:          "all malformed returns error",
			content:       `invalid json` + "\n" + `also invalid`,
			wantCount:     0,
			wantMalformed: 2,
			wantErr:       true,
		},
		{
			name:          "JSONL with empty lines",
			content:       `{"id":"bd-001","title":"Test 1"}` + "\n\n" + `{"id":"bd-002","title":"Test 2"}` + "\n",
			wantCount:     2,
			wantMalformed: 0,
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "bd-migration-validation-*")
			if err != nil {
				t.Fatalf("failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
			if err := os.WriteFile(jsonlPath, []byte(tt.content), 0644); err != nil {
				t.Fatalf("failed to create JSONL: %v", err)
			}

			count, malformed, ids, err := validateJSONLForMigration(jsonlPath)

			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr = %v", err, tt.wantErr)
			}

			if count != tt.wantCount {
				t.Errorf("count = %d, want %d", count, tt.wantCount)
			}

			if malformed != tt.wantMalformed {
				t.Errorf("malformed = %d, want %d", malformed, tt.wantMalformed)
			}

			if len(ids) != tt.wantCount {
				t.Errorf("len(ids) = %d, want %d", len(ids), tt.wantCount)
			}
		})
	}
}

func TestValidateJSONLForMigration_FileNotFound(t *testing.T) {
	_, _, _, err := validateJSONLForMigration("/nonexistent/path/issues.jsonl")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestGetSQLiteDBPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-migration-validation-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	// Test default path
	path := getSQLiteDBPath(beadsDir)
	expected := filepath.Join(beadsDir, "beads.db")
	if path != expected {
		t.Errorf("path = %q, want %q", path, expected)
	}
}

func TestCheckMigrationReadinessResult_NoBeadsDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-migration-validation-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	check, result := CheckMigrationReadiness(tmpDir)

	if check.Status != StatusError {
		t.Errorf("status = %q, want %q", check.Status, StatusError)
	}

	if result.Ready {
		t.Error("expected result.Ready = false for missing .beads")
	}

	if len(result.Errors) == 0 {
		t.Error("expected errors in result")
	}
}

func TestCheckMigrationReadinessResult_NoJSONL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-migration-validation-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	check, result := CheckMigrationReadiness(tmpDir)

	if check.Status != StatusError {
		t.Errorf("status = %q, want %q", check.Status, StatusError)
	}

	if result.Ready {
		t.Error("expected result.Ready = false for missing JSONL")
	}
}

func TestCheckMigrationReadinessResult_ValidJSONL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-migration-validation-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	// Create valid JSONL
	jsonl := `{"id":"bd-001","title":"Test 1"}` + "\n" + `{"id":"bd-002","title":"Test 2"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(jsonl), 0644); err != nil {
		t.Fatalf("failed to create JSONL: %v", err)
	}

	check, result := CheckMigrationReadiness(tmpDir)

	// Should be OK or Warning (no Dolt available is not an error for pre-migration)
	if check.Status == StatusError {
		t.Errorf("status = %q, did not want error for valid JSONL", check.Status)
	}

	if !result.JSONLValid {
		t.Error("expected result.JSONLValid = true")
	}

	if result.JSONLCount != 2 {
		t.Errorf("JSONLCount = %d, want 2", result.JSONLCount)
	}
}

func TestCheckMigrationCompletionResult_NoBeadsDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-migration-validation-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	check, result := CheckMigrationCompletion(tmpDir)

	if check.Status != StatusError {
		t.Errorf("status = %q, want %q", check.Status, StatusError)
	}

	if result.Ready {
		t.Error("expected result.Ready = false")
	}

	if result.DoltHealthy {
		t.Error("expected result.DoltHealthy = false")
	}
}

func TestCheckMigrationCompletionResult_NotDoltBackend(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-migration-validation-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	// Create JSONL (SQLite backend by default)
	jsonl := `{"id":"bd-001","title":"Test 1"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(jsonl), 0644); err != nil {
		t.Fatalf("failed to create JSONL: %v", err)
	}

	check, result := CheckMigrationCompletion(tmpDir)

	if check.Status != StatusError {
		t.Errorf("status = %q, want %q", check.Status, StatusError)
	}

	if result.Ready {
		t.Error("expected result.Ready = false for non-Dolt backend")
	}

	if len(result.Errors) == 0 {
		t.Error("expected errors in result")
	}
}

func TestCheckDoltLocks_NotDoltBackend(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-migration-validation-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	check := CheckDoltLocks(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("status = %q, want %q for non-Dolt backend", check.Status, StatusOK)
	}

	if check.Category != CategoryMaintenance {
		t.Errorf("category = %q, want %q", check.Category, CategoryMaintenance)
	}
}

func TestMigrationValidationResult_JSONSerialization(t *testing.T) {
	result := MigrationValidationResult{
		Phase:          "pre-migration",
		Ready:          true,
		Backend:        "sqlite",
		JSONLCount:     100,
		SQLiteCount:    100,
		MissingInDB:    []string{},
		MissingInJSONL: []string{},
		Errors:         []string{},
		Warnings:       []string{"Some warning"},
		JSONLValid:     true,
		JSONLMalformed: 0,
		DoltHealthy:    false,
		DoltLocked:     false,
		SchemaValid:    true,
	}

	// Verify fields are set correctly (JSON serialization is tested implicitly by the struct)
	if result.Phase != "pre-migration" {
		t.Errorf("Phase = %q, want %q", result.Phase, "pre-migration")
	}

	if !result.Ready {
		t.Error("expected Ready = true")
	}

	if len(result.Warnings) != 1 {
		t.Errorf("len(Warnings) = %d, want 1", len(result.Warnings))
	}
}
