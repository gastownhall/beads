package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckDatabaseIntegrity(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectMessage  string
	}{
		{
			name: "no database",
			setup: func(t *testing.T, dir string) {
				// No database directory created
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no database)",
		},
		{
			name: "stale beads.db file ignored",
			setup: func(t *testing.T, dir string) {
				// A stale beads.db FILE (not directory) is invisible to Dolt backend
				dbPath := filepath.Join(dir, ".beads", "beads.db")
				if err := os.WriteFile(dbPath, []byte("stale sqlite file"), 0600); err != nil {
					t.Fatalf("failed to create stale db file: %v", err)
				}
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no database)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			tt.setup(t, tmpDir)

			check := CheckDatabaseIntegrity(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q", tt.expectedStatus, check.Status)
			}
			if tt.expectMessage != "" && check.Message != tt.expectMessage {
				t.Errorf("expected message %q, got %q", tt.expectMessage, check.Message)
			}
		})
	}
}

func TestCheckDatabaseVersion(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
	}{
		{
			name: "no database no jsonl",
			setup: func(t *testing.T, dir string) {
				// No database, no JSONL - error (need to run bd init)
			},
			expectedStatus: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			tt.setup(t, tmpDir)

			check := CheckDatabaseVersion(tmpDir, "0.1.0")

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
		})
	}
}

func TestCheckSchemaCompatibility(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
	}{
		{
			name: "no database",
			setup: func(t *testing.T, dir string) {
				// No database created
			},
			expectedStatus: "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			tt.setup(t, tmpDir)

			check := CheckSchemaCompatibility(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
		})
	}
}

func TestCheckDatabaseIntegrity_EdgeCases(t *testing.T) {
	t.Skip("SQLite-specific edge cases (locked/read-only files); Dolt backend uses server connections")
}

func TestCheckDatabaseVersion_EdgeCases(t *testing.T) {
	t.Skip("SQLite version tests; Dolt backend checks dolt/ directory, not beads.db")
}

func TestCheckSchemaCompatibility_EdgeCases(t *testing.T) {
	t.Skip("SQLite schema tests; Dolt backend uses different schema validation")
}

func TestClassifyDatabaseError(t *testing.T) {
	tests := []struct {
		name             string
		errMsg           string
		expectedType     string
		containsRecovery string
	}{
		{
			name:             "locked database",
			errMsg:           "database is locked",
			expectedType:     "Database is locked",
			containsRecovery: "Kill any stale processes",
		},
		{
			name:             "not a database",
			errMsg:           "file is not a database",
			expectedType:     "File is not a valid SQLite database",
			containsRecovery: "bd init",
		},
		{
			name:             "migration failed",
			errMsg:           "migration failed",
			expectedType:     "Database migration or validation failed",
			containsRecovery: "bd init",
		},
		{
			name:             "generic error",
			errMsg:           "some unknown error",
			expectedType:     "Failed to open database",
			containsRecovery: "bd doctor --fix --force",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errorType, recoverySteps := classifyDatabaseError(tt.errMsg)
			if errorType != tt.expectedType {
				t.Errorf("expected error type %q, got %q", tt.expectedType, errorType)
			}
			if tt.containsRecovery != "" {
				found := false
				if len(recoverySteps) > 0 {
					for _, substr := range []string{tt.containsRecovery} {
						if len(recoverySteps) > 0 && containsStr(recoverySteps, substr) {
							found = true
							break
						}
					}
				}
				if !found {
					t.Errorf("expected recovery steps to contain %q, got %q", tt.containsRecovery, recoverySteps)
				}
			}
		})
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
