package doctor

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// setupTestDatabase creates a minimal valid SQLite database for testing
func setupTestDatabase(t *testing.T, dir string) string {
	t.Helper()
	dbPath := filepath.Join(dir, ".beads", "beads.db")

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	defer db.Close()

	// Create minimal issues table
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS issues (
		id TEXT PRIMARY KEY,
		title TEXT,
		status TEXT,
		ephemeral INTEGER DEFAULT 0
	)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	return dbPath
}

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
				// No database file created
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no database)",
		},
		{
			name: "valid database",
			setup: func(t *testing.T, dir string) {
				// SQLite DB is invisible to Dolt backend; no dolt/ dir → "no database"
				setupTestDatabase(t, dir)
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no database)",
		},
		{
			name: "corrupt database",
			setup: func(t *testing.T, dir string) {
				dbPath := filepath.Join(dir, ".beads", "beads.db")
				// SQLite garbage file is invisible to Dolt backend; no dolt/ dir → "no database"
				if err := os.WriteFile(dbPath, []byte("not a sqlite database"), 0600); err != nil {
					t.Fatalf("failed to create corrupt db: %v", err)
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
		{
			name: "minimal schema",
			setup: func(t *testing.T, dir string) {
				// SQLite DB invisible to Dolt backend; no dolt/ dir → "no database"
				setupTestDatabase(t, dir)
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

// Edge case tests

func TestCheckDatabaseIntegrity_EdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string) string
		expectedStatus string
	}{
		{
			name: "locked database file",
			setup: func(t *testing.T, dir string) string {
				dbPath := setupTestDatabase(t, dir)

				// Open a connection with an exclusive lock
				db, err := sql.Open("sqlite3", dbPath)
				if err != nil {
					t.Fatalf("failed to open database: %v", err)
				}

				// Start a transaction to hold a lock
				tx, err := db.Begin()
				if err != nil {
					db.Close()
					t.Fatalf("failed to begin transaction: %v", err)
				}

				// Write some data to ensure the lock is held
				_, err = tx.Exec("INSERT INTO issues (id, title, status) VALUES ('lock-test', 'Lock Test', 'open')")
				if err != nil {
					tx.Rollback()
					db.Close()
					t.Fatalf("failed to insert test data: %v", err)
				}

				// Keep the transaction open by returning a cleanup function via test context
				t.Cleanup(func() {
					tx.Rollback()
					db.Close()
				})

				return dbPath
			},
			expectedStatus: "ok", // Should still succeed with busy_timeout
		},
		{
			name: "read-only database file",
			setup: func(t *testing.T, dir string) string {
				dbPath := setupTestDatabase(t, dir)

				// Make the database file read-only
				if err := os.Chmod(dbPath, 0400); err != nil {
					t.Fatalf("failed to chmod database: %v", err)
				}

				return dbPath
			},
			expectedStatus: "ok", // Integrity check uses read-only mode
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
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
		})
	}
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
