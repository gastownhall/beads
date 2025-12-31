package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// setupTestDatabase creates a minimal valid SQLite database for testing
func setupTestDatabase(t *testing.T, dir string) string {
	t.Helper()
	dbPath := filepath.Join(dir, ".beads", "beads.db")

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to create database: %v", err)
	}
	defer db.Close()

	// Create minimal issues table
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS issues (
		id TEXT PRIMARY KEY,
		title TEXT,
		status TEXT
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
				setupTestDatabase(t, dir)
			},
			expectedStatus: "ok",
			expectMessage:  "No corruption detected",
		},
		{
			name: "corrupt database",
			setup: func(t *testing.T, dir string) {
				dbPath := filepath.Join(dir, ".beads", "beads.db")
				// Write garbage that isn't a valid SQLite file
				if err := os.WriteFile(dbPath, []byte("not a sqlite database"), 0600); err != nil {
					t.Fatalf("failed to create corrupt db: %v", err)
				}
			},
			expectedStatus: "error",
			expectMessage:  "", // message varies based on sqlite driver error
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

func TestCheckDatabaseJSONLSync(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectMessage  string
	}{
		{
			name: "no database",
			setup: func(t *testing.T, dir string) {
				// No database, but create JSONL
				jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
				if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test"}`+"\n"), 0600); err != nil {
					t.Fatalf("failed to create JSONL: %v", err)
				}
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no database)",
		},
		{
			name: "no JSONL",
			setup: func(t *testing.T, dir string) {
				setupTestDatabase(t, dir)
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no JSONL file)",
		},
		{
			name: "both exist with same count",
			setup: func(t *testing.T, dir string) {
				// Create database with one issue
				dbPath := setupTestDatabase(t, dir)
				db, _ := sql.Open("sqlite3", dbPath)
				defer db.Close()
				_, _ = db.Exec(`INSERT INTO issues (id, title, status) VALUES ('test-1', 'Test Issue', 'open')`)

				// Create JSONL with one issue
				jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
				if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test Issue","status":"open"}`+"\n"), 0600); err != nil {
					t.Fatalf("failed to create JSONL: %v", err)
				}
			},
			expectedStatus: "warning", // Warning because db doesn't have full schema for prefix check
			expectMessage:  "",
		},
		{
			name: "count mismatch",
			setup: func(t *testing.T, dir string) {
				// Create database with one issue
				dbPath := setupTestDatabase(t, dir)
				db, _ := sql.Open("sqlite3", dbPath)
				defer db.Close()
				_, _ = db.Exec(`INSERT INTO issues (id, title, status) VALUES ('test-1', 'Test Issue', 'open')`)

				// Create JSONL with two issues
				jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
				content := `{"id":"test-1","title":"Test Issue 1","status":"open"}
{"id":"test-2","title":"Test Issue 2","status":"open"}
`
				if err := os.WriteFile(jsonlPath, []byte(content), 0600); err != nil {
					t.Fatalf("failed to create JSONL: %v", err)
				}
			},
			expectedStatus: "warning",
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

			check := CheckDatabaseJSONLSync(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
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
			name: "fresh clone with JSONL",
			setup: func(t *testing.T, dir string) {
				// No database but JSONL exists - fresh clone warning
				jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
				if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test"}`+"\n"), 0600); err != nil {
					t.Fatalf("failed to create JSONL: %v", err)
				}
			},
			expectedStatus: "warning", // Warning for fresh clone needing init
		},
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
				// Our minimal test database doesn't have full schema
				setupTestDatabase(t, dir)
			},
			expectedStatus: "error", // Error because schema is incomplete
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

func TestCountJSONLIssues(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectedCount int
		expectError   bool
	}{
		{
			name:          "empty file",
			content:       "",
			expectedCount: 0,
			expectError:   false,
		},
		{
			name:          "single issue",
			content:       `{"id":"test-1","title":"Test"}` + "\n",
			expectedCount: 1,
			expectError:   false,
		},
		{
			name: "multiple issues",
			content: `{"id":"test-1","title":"Test 1"}
{"id":"test-2","title":"Test 2"}
{"id":"test-3","title":"Test 3"}
`,
			expectedCount: 3,
			expectError:   false,
		},
		{
			name: "counts all including tombstones",
			content: `{"id":"test-1","title":"Test 1","status":"open"}
{"id":"test-2","title":"Test 2","status":"tombstone"}
{"id":"test-3","title":"Test 3","status":"closed"}
`,
			expectedCount: 3, // CountJSONLIssues counts all records including tombstones
			expectError:   false,
		},
		{
			name: "skips empty lines",
			content: `{"id":"test-1","title":"Test 1"}

{"id":"test-2","title":"Test 2"}
`,
			expectedCount: 2,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
			if err := os.WriteFile(jsonlPath, []byte(tt.content), 0600); err != nil {
				t.Fatalf("failed to create JSONL: %v", err)
			}

			count, _, err := CountJSONLIssues(jsonlPath)

			if tt.expectError && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if count != tt.expectedCount {
				t.Errorf("expected count %d, got %d", tt.expectedCount, count)
			}
		})
	}
}

func TestCountJSONLIssues_NonexistentFile(t *testing.T) {
	count, _, err := CountJSONLIssues("/nonexistent/path/issues.jsonl")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
}

func TestCountJSONLIssues_ExtractsPrefixes(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
	content := `{"id":"bd-123","title":"Test 1"}
{"id":"bd-456","title":"Test 2"}
{"id":"proj-789","title":"Test 3"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to create JSONL: %v", err)
	}

	count, prefixes, err := CountJSONLIssues(jsonlPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected count 3, got %d", count)
	}

	// Check prefixes were extracted
	if _, ok := prefixes["bd"]; !ok {
		t.Error("expected 'bd' prefix to be detected")
	}
	if _, ok := prefixes["proj"]; !ok {
		t.Error("expected 'proj' prefix to be detected")
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

func TestCheckDatabaseJSONLSync_EdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
	}{
		{
			name: "malformed JSONL with some valid entries",
			setup: func(t *testing.T, dir string) {
				dbPath := setupTestDatabase(t, dir)
				db, _ := sql.Open("sqlite3", dbPath)
				defer db.Close()

				// Insert test issue into database
				_, _ = db.Exec(`INSERT INTO issues (id, title, status) VALUES ('test-1', 'Test Issue', 'open')`)

				// Create JSONL with malformed entries
				jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
				content := `{"id":"test-1","title":"Valid Entry"}
{malformed json without quotes
{"id":"test-2","incomplete
{"id":"test-3","title":"Another Valid Entry"}
`
				if err := os.WriteFile(jsonlPath, []byte(content), 0600); err != nil {
					t.Fatalf("failed to create JSONL: %v", err)
				}
			},
			expectedStatus: "warning", // Should warn about malformed lines
		},
		{
			name: "JSONL with mixed valid and invalid JSON",
			setup: func(t *testing.T, dir string) {
				setupTestDatabase(t, dir)

				// Create JSONL with some invalid JSON lines
				jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
				content := `{"id":"test-1","title":"Valid"}
not json at all
{"id":"test-2","title":"Also Valid"}
{"broken": json}
`
				if err := os.WriteFile(jsonlPath, []byte(content), 0600); err != nil {
					t.Fatalf("failed to create JSONL: %v", err)
				}
			},
			expectedStatus: "warning",
		},
		{
			name: "JSONL with entries missing id field",
			setup: func(t *testing.T, dir string) {
				dbPath := setupTestDatabase(t, dir)
				db, _ := sql.Open("sqlite3", dbPath)
				defer db.Close()
				_, _ = db.Exec(`INSERT INTO issues (id, title, status) VALUES ('test-1', 'Test', 'open')`)

				// Create JSONL where some entries don't have id field
				jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
				content := `{"id":"test-1","title":"Has ID"}
{"title":"No ID field","status":"open"}
{"id":"test-2","title":"Has ID"}
`
				if err := os.WriteFile(jsonlPath, []byte(content), 0600); err != nil {
					t.Fatalf("failed to create JSONL: %v", err)
				}
			},
			expectedStatus: "warning", // Count mismatch: db has 1, jsonl counts only 2 valid
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

			check := CheckDatabaseJSONLSync(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
		})
	}
}

func TestCheckDatabaseVersion_EdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		cliVersion     string
		expectedStatus string
		expectMessage  string
	}{
		{
			name: "future database version",
			setup: func(t *testing.T, dir string) {
				dbPath := filepath.Join(dir, ".beads", "beads.db")
				db, err := sql.Open("sqlite3", dbPath)
				if err != nil {
					t.Fatalf("failed to create database: %v", err)
				}
				defer db.Close()

				// Create metadata table with future version
				_, err = db.Exec(`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT)`)
				if err != nil {
					t.Fatalf("failed to create metadata table: %v", err)
				}
				_, err = db.Exec(`INSERT INTO metadata (key, value) VALUES ('bd_version', '99.99.99')`)
				if err != nil {
					t.Fatalf("failed to insert version: %v", err)
				}
			},
			cliVersion:     "0.1.0",
			expectedStatus: "warning",
			expectMessage:  "version 99.99.99 (CLI: 0.1.0)",
		},
		{
			name: "database with metadata table but no version",
			setup: func(t *testing.T, dir string) {
				dbPath := filepath.Join(dir, ".beads", "beads.db")
				db, err := sql.Open("sqlite3", dbPath)
				if err != nil {
					t.Fatalf("failed to create database: %v", err)
				}
				defer db.Close()

				// Create metadata table but don't insert version
				_, err = db.Exec(`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT)`)
				if err != nil {
					t.Fatalf("failed to create metadata table: %v", err)
				}
			},
			cliVersion:     "0.1.0",
			expectedStatus: "error",
			expectMessage:  "Unable to read database version",
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

			check := CheckDatabaseVersion(tmpDir, tt.cliVersion)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
			if tt.expectMessage != "" && check.Message != tt.expectMessage {
				t.Errorf("expected message %q, got %q", tt.expectMessage, check.Message)
			}
		})
	}
}

func TestCheckSchemaCompatibility_EdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectInDetail string
	}{
		{
			name: "partial schema - missing dependencies table",
			setup: func(t *testing.T, dir string) {
				dbPath := filepath.Join(dir, ".beads", "beads.db")
				db, err := sql.Open("sqlite3", dbPath)
				if err != nil {
					t.Fatalf("failed to create database: %v", err)
				}
				defer db.Close()

				// Create only issues table, missing other required tables
				_, err = db.Exec(`CREATE TABLE issues (
					id TEXT PRIMARY KEY,
					title TEXT,
					content_hash TEXT,
					external_ref TEXT,
					compacted_at INTEGER,
					close_reason TEXT
				)`)
				if err != nil {
					t.Fatalf("failed to create issues table: %v", err)
				}
			},
			expectedStatus: "error",
			expectInDetail: "table:dependencies",
		},
		{
			name: "partial schema - missing columns in issues table",
			setup: func(t *testing.T, dir string) {
				dbPath := filepath.Join(dir, ".beads", "beads.db")
				db, err := sql.Open("sqlite3", dbPath)
				if err != nil {
					t.Fatalf("failed to create database: %v", err)
				}
				defer db.Close()

				// Create issues table missing some required columns
				_, err = db.Exec(`CREATE TABLE issues (
					id TEXT PRIMARY KEY,
					title TEXT
				)`)
				if err != nil {
					t.Fatalf("failed to create issues table: %v", err)
				}

				// Create other tables to avoid those errors
				_, err = db.Exec(`CREATE TABLE dependencies (
					issue_id TEXT,
					depends_on_id TEXT,
					type TEXT
				)`)
				if err != nil {
					t.Fatalf("failed to create dependencies table: %v", err)
				}

				_, err = db.Exec(`CREATE TABLE child_counters (
					parent_id TEXT,
					last_child INTEGER
				)`)
				if err != nil {
					t.Fatalf("failed to create child_counters table: %v", err)
				}

				_, err = db.Exec(`CREATE TABLE export_hashes (
					issue_id TEXT,
					content_hash TEXT
				)`)
				if err != nil {
					t.Fatalf("failed to create export_hashes table: %v", err)
				}
			},
			expectedStatus: "error",
			expectInDetail: "issues.content_hash",
		},
		{
			name: "database with no tables",
			setup: func(t *testing.T, dir string) {
				dbPath := filepath.Join(dir, ".beads", "beads.db")
				db, err := sql.Open("sqlite3", dbPath)
				if err != nil {
					t.Fatalf("failed to create database: %v", err)
				}
				// Execute a query to ensure the database file is created
				_, err = db.Exec("SELECT 1")
				if err != nil {
					t.Fatalf("failed to initialize database: %v", err)
				}
				db.Close()
			},
			expectedStatus: "error",
			expectInDetail: "table:",
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
				t.Errorf("expected status %q, got %q (message: %s, detail: %s)",
					tt.expectedStatus, check.Status, check.Message, check.Detail)
			}
			if tt.expectInDetail != "" && !strings.Contains(check.Detail, tt.expectInDetail) {
				t.Errorf("expected detail to contain %q, got %q", tt.expectInDetail, check.Detail)
			}
		})
	}
}

func TestCountJSONLIssues_EdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		setupContent  func() string
		expectedCount int
		expectError   bool
		errorContains string
	}{
		{
			name: "malformed JSON lines",
			setupContent: func() string {
				return `{"id":"valid-1","title":"Valid"}
{this is not json
{"id":"valid-2","title":"Also Valid"}
{malformed: true}
{"id":"valid-3","title":"Third Valid"}
`
			},
			expectedCount: 3,
			expectError:   true,
			errorContains: "malformed",
		},
		{
			name: "very large file with 10000 issues",
			setupContent: func() string {
				var sb strings.Builder
				for i := 0; i < 10000; i++ {
					sb.WriteString(fmt.Sprintf(`{"id":"issue-%d","title":"Issue %d","status":"open"}`, i, i))
					sb.WriteString("\n")
				}
				return sb.String()
			},
			expectedCount: 10000,
			expectError:   false,
		},
		{
			name: "file with unicode and special characters",
			setupContent: func() string {
				return `{"id":"test-1","title":"Issue with émojis 🎉","description":"Unicode: 日本語"}
{"id":"test-2","title":"Quotes \"escaped\" and 'mixed'","status":"open"}
{"id":"test-3","title":"Newlines\nand\ttabs","status":"closed"}
`
			},
			expectedCount: 3,
			expectError:   false,
		},
		{
			name: "file with trailing whitespace",
			setupContent: func() string {
				return `{"id":"test-1","title":"Test"}
  {"id":"test-2","title":"Test 2"}
{"id":"test-3","title":"Test 3"}
`
			},
			expectedCount: 3,
			expectError:   false,
		},
		{
			name: "all lines are malformed",
			setupContent: func() string {
				return `not json
also not json
{still: not valid}
`
			},
			expectedCount: 0,
			expectError:   true,
			errorContains: "malformed",
		},
		{
			name: "valid JSON but missing id in all entries",
			setupContent: func() string {
				return `{"title":"No ID 1","status":"open"}
{"title":"No ID 2","status":"closed"}
{"title":"No ID 3","status":"pending"}
`
			},
			expectedCount: 0,
			expectError:   false,
		},
		{
			name: "entries with numeric ids",
			setupContent: func() string {
				return `{"id":123,"title":"Numeric ID"}
{"id":"valid-1","title":"String ID"}
{"id":null,"title":"Null ID"}
`
			},
			expectedCount: 1, // Only the string ID counts
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
			content := tt.setupContent()
			if err := os.WriteFile(jsonlPath, []byte(content), 0600); err != nil {
				t.Fatalf("failed to create JSONL: %v", err)
			}

			count, _, err := CountJSONLIssues(jsonlPath)

			if tt.expectError && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.expectError && err != nil && tt.errorContains != "" {
				if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errorContains, err.Error())
				}
			}
			if count != tt.expectedCount {
				t.Errorf("expected count %d, got %d", tt.expectedCount, count)
			}
		})
	}
}

// TestCheckDatabaseJSONLSync_MoleculePrefix verifies that molecule/wisp prefixes
// are recognized as valid variants and don't trigger false positive warnings.
// Regression test for GitHub issue #811.
func TestCheckDatabaseJSONLSync_MoleculePrefix(t *testing.T) {
	tests := []struct {
		name           string
		dbPrefix       string
		jsonlContent   string
		expectWarning  bool
		warningMessage string
	}{
		{
			name:     "mol prefix is valid variant",
			dbPrefix: "my-project",
			// 3 out of 4 issues have the -mol prefix (majority)
			jsonlContent: `{"id":"my-project-mol-001","title":"Mol Issue 1"}
{"id":"my-project-mol-002","title":"Mol Issue 2"}
{"id":"my-project-mol-003","title":"Mol Issue 3"}
{"id":"my-project-004","title":"Regular Issue"}
`,
			expectWarning:  false, // Should NOT warn - mol is a valid variant
			warningMessage: "",
		},
		{
			name:     "wisp prefix is valid variant",
			dbPrefix: "my-project",
			jsonlContent: `{"id":"my-project-wisp-001","title":"Wisp Issue 1"}
{"id":"my-project-wisp-002","title":"Wisp Issue 2"}
{"id":"my-project-wisp-003","title":"Wisp Issue 3"}
`,
			expectWarning:  false, // Should NOT warn - wisp is a valid variant
			warningMessage: "",
		},
		{
			name:     "eph prefix is valid variant",
			dbPrefix: "my-project",
			jsonlContent: `{"id":"my-project-eph-001","title":"Ephemeral Issue 1"}
{"id":"my-project-eph-002","title":"Ephemeral Issue 2"}
{"id":"my-project-eph-003","title":"Ephemeral Issue 3"}
`,
			expectWarning:  false, // Should NOT warn - eph is a valid variant
			warningMessage: "",
		},
		{
			name:     "unrelated prefix SHOULD warn",
			dbPrefix: "my-project",
			jsonlContent: `{"id":"other-project-001","title":"Wrong Project 1"}
{"id":"other-project-002","title":"Wrong Project 2"}
{"id":"other-project-003","title":"Wrong Project 3"}
`,
			expectWarning:  true, // SHOULD warn - different project entirely
			warningMessage: "Prefix mismatch",
		},
		{
			name:     "mixed valid variants do not warn",
			dbPrefix: "bd",
			jsonlContent: `{"id":"bd-mol-001","title":"Mol Issue"}
{"id":"bd-wisp-001","title":"Wisp Issue"}
{"id":"bd-001","title":"Regular Issue"}
`,
			expectWarning:  false, // All are valid variants of "bd"
			warningMessage: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			// Create database with config table containing the prefix
			dbPath := filepath.Join(beadsDir, "beads.db")
			db, err := sql.Open("sqlite3", dbPath)
			if err != nil {
				t.Fatalf("failed to create database: %v", err)
			}

			// Create issues table
			_, err = db.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT, status TEXT)`)
			if err != nil {
				db.Close()
				t.Fatalf("failed to create issues table: %v", err)
			}

			// Create config table with prefix
			_, err = db.Exec(`CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)`)
			if err != nil {
				db.Close()
				t.Fatalf("failed to create config table: %v", err)
			}
			_, err = db.Exec(`INSERT INTO config (key, value) VALUES ('issue_prefix', ?)`, tt.dbPrefix)
			if err != nil {
				db.Close()
				t.Fatalf("failed to insert prefix: %v", err)
			}

			// Count issues in JSONL and insert matching count into DB
			lines := strings.Split(strings.TrimSpace(tt.jsonlContent), "\n")
			issueCount := 0
			for _, line := range lines {
				if strings.TrimSpace(line) != "" {
					issueCount++
				}
			}
			for i := 0; i < issueCount; i++ {
				_, err = db.Exec(`INSERT INTO issues (id, title, status) VALUES (?, ?, ?)`,
					fmt.Sprintf("db-issue-%d", i), fmt.Sprintf("DB Issue %d", i), "open")
				if err != nil {
					db.Close()
					t.Fatalf("failed to insert issue: %v", err)
				}
			}
			db.Close()

			// Create JSONL file
			jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
			if err := os.WriteFile(jsonlPath, []byte(tt.jsonlContent), 0600); err != nil {
				t.Fatalf("failed to create JSONL: %v", err)
			}

			check := CheckDatabaseJSONLSync(tmpDir)

			hasPrefixWarning := strings.Contains(check.Message, "Prefix mismatch")

			if tt.expectWarning && !hasPrefixWarning {
				t.Errorf("expected prefix mismatch warning, but got: status=%s, message=%s",
					check.Status, check.Message)
			}
			if !tt.expectWarning && hasPrefixWarning {
				t.Errorf("did NOT expect prefix mismatch warning, but got: status=%s, message=%s",
					check.Status, check.Message)
			}
		})
	}
}

func TestCountJSONLIssues_Performance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping performance test in short mode")
	}

	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "large.jsonl")

	// Create a very large JSONL file (100k issues)
	file, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	for i := 0; i < 100000; i++ {
		line := fmt.Sprintf(`{"id":"perf-%d","title":"Performance Test Issue %d","status":"open","description":"Testing performance with large files"}`, i, i)
		if _, err := file.WriteString(line + "\n"); err != nil {
			file.Close()
			t.Fatalf("failed to write line: %v", err)
		}
	}
	file.Close()

	// Measure time to count issues
	start := time.Now()
	count, prefixes, err := CountJSONLIssues(jsonlPath)
	duration := time.Since(start)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if count != 100000 {
		t.Errorf("expected count 100000, got %d", count)
	}
	if len(prefixes) != 1 || prefixes["perf"] != 100000 {
		t.Errorf("expected single prefix 'perf' with count 100000, got %v", prefixes)
	}

	// Performance should be reasonable (< 5 seconds for 100k issues)
	if duration > 5*time.Second {
		t.Logf("Warning: counting 100k issues took %v (expected < 5s)", duration)
	} else {
		t.Logf("Performance: counted 100k issues in %v", duration)
	}
}
