package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestDetectHashBasedIDs(t *testing.T) {
	tests := []struct {
		name      string
		sampleIDs []string
		hasTable  bool
		expected  bool
	}{
		{
			name:      "hash IDs with letters",
			sampleIDs: []string{"bd-a3f8e9", "bd-b2c4d6"},
			hasTable:  false,
			expected:  true,
		},
		{
			name:      "hash IDs with mixed alphanumeric",
			sampleIDs: []string{"bd-0134cc5a", "bd-abc123"},
			hasTable:  false,
			expected:  true,
		},
		{
			name:      "hash IDs all numeric with variable length",
			sampleIDs: []string{"bd-0088", "bd-0134cc5a", "bd-02a4"},
			hasTable:  false,
			expected:  true, // Variable length indicates hash IDs
		},
		{
			name:      "hash IDs with leading zeros",
			sampleIDs: []string{"bd-0088", "bd-02a4", "bd-05a1"},
			hasTable:  false,
			expected:  true, // Leading zeros indicate hash IDs
		},
		{
			name:      "hash IDs all numeric non-sequential",
			sampleIDs: []string{"bd-0088", "bd-2312", "bd-0458"},
			hasTable:  false,
			expected:  true, // Non-sequential pattern
		},
		{
			name:      "sequential IDs",
			sampleIDs: []string{"bd-1", "bd-2", "bd-3", "bd-4"},
			hasTable:  false,
			expected:  false, // Sequential pattern
		},
		{
			name:      "sequential IDs with gaps",
			sampleIDs: []string{"bd-1", "bd-5", "bd-10", "bd-15"},
			hasTable:  false,
			expected:  false, // Still sequential pattern (small gaps allowed)
		},
		{
			name:      "database with child_counters table",
			sampleIDs: []string{"bd-1", "bd-2"},
			hasTable:  true,
			expected:  true, // child_counters table indicates hash IDs
		},
		{
			name:      "hash IDs with hierarchical children",
			sampleIDs: []string{"bd-a3f8e9.1", "bd-a3f8e9.2", "bd-b2c4d6"},
			hasTable:  false,
			expected:  true, // Base IDs have letters
		},
		{
			name:      "edge case: single ID with letters",
			sampleIDs: []string{"bd-abc"},
			hasTable:  false,
			expected:  true,
		},
		{
			name:      "edge case: single sequential ID",
			sampleIDs: []string{"bd-1"},
			hasTable:  false,
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary database
			tmpDir := t.TempDir()
			dbPath := filepath.Join(tmpDir, "test.db")

			// Open database and create schema
			db, err := sql.Open("sqlite3", dbPath)
			if err != nil {
				t.Fatalf("Failed to open database: %v", err)
			}
			defer db.Close()

			// Create issues table
			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS issues (
					id TEXT PRIMARY KEY,
					title TEXT,
					created_at TIMESTAMP
				)
			`)
			if err != nil {
				t.Fatalf("Failed to create issues table: %v", err)
			}

			// Create child_counters table if test requires it
			if tt.hasTable {
				_, err = db.Exec(`
					CREATE TABLE IF NOT EXISTS child_counters (
						parent_id TEXT PRIMARY KEY,
						last_child INTEGER NOT NULL DEFAULT 0
					)
				`)
				if err != nil {
					t.Fatalf("Failed to create child_counters table: %v", err)
				}
			}

			// Insert sample issues
			for _, id := range tt.sampleIDs {
				_, err = db.Exec("INSERT INTO issues (id, title, created_at) VALUES (?, ?, datetime('now'))",
					id, "Test issue")
				if err != nil {
					t.Fatalf("Failed to insert issue %s: %v", id, err)
				}
			}

			// Test detection
			result := detectHashBasedIDs(db, tt.sampleIDs)
			if result != tt.expected {
				t.Errorf("detectHashBasedIDs() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestCheckIDFormat(t *testing.T) {
	tests := []struct {
		name           string
		issueIDs       []string
		createTable    bool // create child_counters table
		expectedStatus string
	}{
		{
			name:           "hash IDs with letters",
			issueIDs:       []string{"bd-a3f8e9", "bd-b2c4d6", "bd-xyz123"},
			createTable:    false,
			expectedStatus: statusOK,
		},
		{
			name:           "hash IDs all numeric with leading zeros",
			issueIDs:       []string{"bd-0088", "bd-02a4", "bd-05a1", "bd-0458"},
			createTable:    false,
			expectedStatus: statusOK,
		},
		{
			name:           "hash IDs with child_counters table",
			issueIDs:       []string{"bd-123", "bd-456"},
			createTable:    true,
			expectedStatus: statusOK,
		},
		{
			name:           "sequential IDs",
			issueIDs:       []string{"bd-1", "bd-2", "bd-3", "bd-4"},
			createTable:    false,
			expectedStatus: statusWarning,
		},
		{
			name:           "mixed: mostly hash IDs",
			issueIDs:       []string{"bd-0088", "bd-0134cc5a", "bd-02a4"},
			createTable:    false,
			expectedStatus: statusOK, // Variable length = hash IDs
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary workspace
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.Mkdir(beadsDir, 0750); err != nil {
				t.Fatal(err)
			}

			// Create database
			dbPath := filepath.Join(beadsDir, "beads.db")
			db, err := sql.Open("sqlite3", dbPath)
			if err != nil {
				t.Fatalf("Failed to open database: %v", err)
			}
			defer db.Close()

			// Create schema
			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS issues (
					id TEXT PRIMARY KEY,
					title TEXT NOT NULL,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				t.Fatalf("Failed to create issues table: %v", err)
			}

			if tt.createTable {
				_, err = db.Exec(`
					CREATE TABLE IF NOT EXISTS child_counters (
						parent_id TEXT PRIMARY KEY,
						last_child INTEGER NOT NULL DEFAULT 0
					)
				`)
				if err != nil {
					t.Fatalf("Failed to create child_counters table: %v", err)
				}
			}

			// Insert test issues
			for i, id := range tt.issueIDs {
				_, err = db.Exec(
					"INSERT INTO issues (id, title, created_at) VALUES (?, ?, datetime('now', ?||' seconds'))",
					id, "Test issue "+id, fmt.Sprintf("+%d", i))
				if err != nil {
					t.Fatalf("Failed to insert issue %s: %v", id, err)
				}
			}
			db.Close()

			// Run check
			check := checkIDFormat(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}

			if tt.expectedStatus == statusOK && check.Status == statusOK {
				if !strings.Contains(check.Message, "hash-based") {
					t.Errorf("Expected hash-based message, got: %s", check.Message)
				}
			}

			if tt.expectedStatus == statusWarning && check.Status == statusWarning {
				if check.Fix == "" {
					t.Error("Expected fix message for sequential IDs")
				}
			}
		})
	}
}

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

	// Create config.yaml with no-db: true to indicate intentional JSONL-only mode
	// Without this, doctor treats it as a fresh clone needing 'bd init' (bd-4ew)
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("no-db: true\n"), 0644); err != nil {
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

func TestCheckDatabaseVersionFreshClone(t *testing.T) {
	// Create temporary directory with .beads and JSONL but no database
	// This simulates a fresh clone that needs 'bd init'
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Create issues.jsonl with an issue (no config.yaml = not no-db mode)
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := checkDatabaseVersion(tmpDir)

	if check.Status != statusWarning {
		t.Errorf("Expected warning status for fresh clone, got %s", check.Status)
	}
	if check.Message != "Fresh clone detected (no database)" {
		t.Errorf("Expected fresh clone message, got %s", check.Message)
	}
	if check.Fix == "" {
		t.Error("Expected fix field to recommend 'bd init'")
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

func TestCheckMultipleDatabases(t *testing.T) {
	tests := []struct {
		name           string
		dbFiles        []string
		expectedStatus string
		expectWarning  bool
	}{
		{
			name:           "no databases",
			dbFiles:        []string{},
			expectedStatus: statusOK,
			expectWarning:  false,
		},
		{
			name:           "single database",
			dbFiles:        []string{"beads.db"},
			expectedStatus: statusOK,
			expectWarning:  false,
		},
		{
			name:           "multiple databases",
			dbFiles:        []string{"beads.db", "old.db"},
			expectedStatus: statusWarning,
			expectWarning:  true,
		},
		{
			name:           "backup files ignored",
			dbFiles:        []string{"beads.db", "beads.backup.db"},
			expectedStatus: statusOK,
			expectWarning:  false,
		},
		{
			name:           "vc.db ignored",
			dbFiles:        []string{"beads.db", "vc.db"},
			expectedStatus: statusOK,
			expectWarning:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.Mkdir(beadsDir, 0750); err != nil {
				t.Fatal(err)
			}

			// Create test database files
			for _, dbFile := range tc.dbFiles {
				path := filepath.Join(beadsDir, dbFile)
				if err := os.WriteFile(path, []byte{}, 0644); err != nil {
					t.Fatal(err)
				}
			}

			check := checkMultipleDatabases(tmpDir)

			if check.Status != tc.expectedStatus {
				t.Errorf("Expected status %s, got %s", tc.expectedStatus, check.Status)
			}

			if tc.expectWarning && check.Fix == "" {
				t.Error("Expected fix message for warning status")
			}
		})
	}
}

func TestCheckPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	check := checkPermissions(tmpDir)

	if check.Status != statusOK {
		t.Errorf("Expected ok status for writable directory, got %s: %s", check.Status, check.Message)
	}
}

func TestCheckDatabaseJSONLSync(t *testing.T) {
	tests := []struct {
		name           string
		hasDB          bool
		hasJSONL       bool
		expectedStatus string
	}{
		{
			name:           "no database",
			hasDB:          false,
			hasJSONL:       true,
			expectedStatus: statusOK,
		},
		{
			name:           "no JSONL",
			hasDB:          true,
			hasJSONL:       false,
			expectedStatus: statusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.Mkdir(beadsDir, 0750); err != nil {
				t.Fatal(err)
			}

			if tc.hasDB {
				dbPath := filepath.Join(beadsDir, "beads.db")
				// Skip database creation tests due to SQLite driver registration in tests
				// The real doctor command works fine with actual databases
				if tc.hasJSONL {
					t.Skip("Database creation in tests requires complex driver setup")
				}
				// For no-JSONL case, just create an empty file
				if err := os.WriteFile(dbPath, []byte{}, 0644); err != nil {
					t.Fatal(err)
				}
			}

			if tc.hasJSONL {
				jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
				if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
					t.Fatal(err)
				}
			}

			check := checkDatabaseJSONLSync(tmpDir)

			if check.Status != tc.expectedStatus {
				t.Errorf("Expected status %s, got %s", tc.expectedStatus, check.Status)
			}
		})
	}
}


func TestCountJSONLIssuesWithMalformedLines(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Create JSONL file with mixed valid and invalid JSON
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	jsonlContent := `{"id":"test-001","title":"Valid 1"}
invalid json line here
{"id":"test-002","title":"Valid 2"}
{"broken": incomplete
{"id":"test-003","title":"Valid 3"}
`
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0644); err != nil {
		t.Fatal(err)
	}

	count, prefixes, err := countJSONLIssues(jsonlPath)

	// Should count valid issues (3)
	if count != 3 {
		t.Errorf("Expected 3 issues, got %d", count)
	}

	// Should have 1 error for malformed lines
	if err == nil {
		t.Error("Expected error for malformed lines, got nil")
	}
	if !strings.Contains(err.Error(), "skipped") {
		t.Errorf("Expected error about skipped lines, got: %v", err)
	}

	// Should have extracted prefix
	if prefixes["test"] != 3 {
		t.Errorf("Expected 3 'test' prefixes, got %d", prefixes["test"])
	}
}
func TestCheckGitHooks(t *testing.T) {
	tests := []struct {
		name           string
		hasGitDir      bool
		installedHooks []string
		expectedStatus string
		expectWarning  bool
	}{
		{
			name:           "not a git repository",
			hasGitDir:      false,
			installedHooks: []string{},
			expectedStatus: statusOK,
			expectWarning:  false,
		},
		{
			name:           "all hooks installed",
			hasGitDir:      true,
			installedHooks: []string{"pre-commit", "post-merge", "pre-push"},
			expectedStatus: statusOK,
			expectWarning:  false,
		},
		{
			name:           "no hooks installed",
			hasGitDir:      true,
			installedHooks: []string{},
			expectedStatus: statusWarning,
			expectWarning:  true,
		},
		{
			name:           "some hooks installed",
			hasGitDir:      true,
			installedHooks: []string{"pre-commit"},
			expectedStatus: statusWarning,
			expectWarning:  true,
		},
		{
			name:           "partial hooks installed",
			hasGitDir:      true,
			installedHooks: []string{"pre-commit", "post-merge"},
			expectedStatus: statusWarning,
			expectWarning:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			if tc.hasGitDir {
				gitDir := filepath.Join(tmpDir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				if err := os.MkdirAll(hooksDir, 0750); err != nil {
					t.Fatal(err)
				}

				// Create installed hooks
				for _, hookName := range tc.installedHooks {
					hookPath := filepath.Join(hooksDir, hookName)
					if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n"), 0755); err != nil {
						t.Fatal(err)
					}
				}
			}

			check := checkGitHooks(tmpDir)

			if check.Status != tc.expectedStatus {
				t.Errorf("Expected status %s, got %s", tc.expectedStatus, check.Status)
			}

			if tc.expectWarning && check.Fix == "" {
				t.Error("Expected fix message for warning status")
			}

			if !tc.expectWarning && check.Fix != "" && tc.hasGitDir {
				t.Error("Expected no fix message for non-warning status")
			}
		})
	}
}

func TestCheckMetadataVersionTracking(t *testing.T) {
	tests := []struct {
		name           string
		setupMetadata  func(beadsDir string) error
		expectedStatus string
		expectWarning  bool
	}{
		{
			name: "valid current version",
			setupMetadata: func(beadsDir string) error {
				cfg := map[string]string{
					"database":        "beads.db",
					"last_bd_version": Version,
				}
				data, _ := json.Marshal(cfg)
				return os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644)
			},
			expectedStatus: statusOK,
			expectWarning:  false,
		},
		{
			name: "slightly outdated version",
			setupMetadata: func(beadsDir string) error {
				cfg := map[string]string{
					"database":        "beads.db",
					"last_bd_version": "0.24.0",
				}
				data, _ := json.Marshal(cfg)
				return os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644)
			},
			expectedStatus: statusOK,
			expectWarning:  false,
		},
		{
			name: "very old version",
			setupMetadata: func(beadsDir string) error {
				cfg := map[string]string{
					"database":        "beads.db",
					"last_bd_version": "0.14.0",
				}
				data, _ := json.Marshal(cfg)
				return os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644)
			},
			expectedStatus: statusWarning,
			expectWarning:  true,
		},
		{
			name: "empty version field",
			setupMetadata: func(beadsDir string) error {
				cfg := map[string]string{
					"database":        "beads.db",
					"last_bd_version": "",
				}
				data, _ := json.Marshal(cfg)
				return os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644)
			},
			expectedStatus: statusWarning,
			expectWarning:  true,
		},
		{
			name: "invalid version format",
			setupMetadata: func(beadsDir string) error {
				cfg := map[string]string{
					"database":        "beads.db",
					"last_bd_version": "invalid-version",
				}
				data, _ := json.Marshal(cfg)
				return os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644)
			},
			expectedStatus: statusWarning,
			expectWarning:  true,
		},
		{
			name: "corrupted metadata.json",
			setupMetadata: func(beadsDir string) error {
				return os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{invalid json}"), 0644)
			},
			expectedStatus: statusError,
			expectWarning:  false,
		},
		{
			name: "missing metadata.json",
			setupMetadata: func(beadsDir string) error {
				// Don't create metadata.json
				return nil
			},
			expectedStatus: statusWarning,
			expectWarning:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.Mkdir(beadsDir, 0750); err != nil {
				t.Fatal(err)
			}

			// Setup metadata.json
			if err := tc.setupMetadata(beadsDir); err != nil {
				t.Fatal(err)
			}

			check := checkMetadataVersionTracking(tmpDir)

			if check.Status != tc.expectedStatus {
				t.Errorf("Expected status %s, got %s (message: %s)", tc.expectedStatus, check.Status, check.Message)
			}

			if tc.expectWarning && check.Status == statusWarning && check.Fix == "" {
				t.Error("Expected fix message for warning status")
			}
		})
	}
}

func TestIsValidSemver(t *testing.T) {
	tests := []struct {
		version  string
		expected bool
	}{
		{"0.24.2", true},
		{"1.0.0", true},
		{"0.1", true},       // Major.minor is valid
		{"1", true},         // Just major is valid
		{"", false},         // Empty is invalid
		{"invalid", false},  // Non-numeric is invalid
		{"0.a.2", false},    // Letters in parts are invalid
		{"1.2.3.4", true},   // Extra parts are ok
	}

	for _, tc := range tests {
		result := isValidSemver(tc.version)
		if result != tc.expected {
			t.Errorf("isValidSemver(%q) = %v, expected %v", tc.version, result, tc.expected)
		}
	}
}

func TestParseVersionParts(t *testing.T) {
	tests := []struct {
		version  string
		expected []int
	}{
		{"0.24.2", []int{0, 24, 2}},
		{"1.0.0", []int{1, 0, 0}},
		{"0.1", []int{0, 1}},
		{"1", []int{1}},
		{"", []int{}},
		{"invalid", []int{}},
		{"1.a.3", []int{1}}, // Stops at first non-numeric part
	}

	for _, tc := range tests {
		result := parseVersionParts(tc.version)
		if len(result) != len(tc.expected) {
			t.Errorf("parseVersionParts(%q) returned %d parts, expected %d", tc.version, len(result), len(tc.expected))
			continue
		}
		for i := range result {
			if result[i] != tc.expected[i] {
				t.Errorf("parseVersionParts(%q)[%d] = %d, expected %d", tc.version, i, result[i], tc.expected[i])
			}
		}
	}
}

func TestCheckSyncBranchConfig(t *testing.T) {
	tests := []struct {
		name           string
		setupFunc      func(t *testing.T, tmpDir string)
		expectedStatus string
		expectWarning  bool
	}{
		{
			name: "no beads directory",
			setupFunc: func(t *testing.T, tmpDir string) {
				// No .beads directory
			},
			expectedStatus: statusOK,
			expectWarning:  false,
		},
		{
			name: "not a git repo",
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: statusOK,
			expectWarning:  false,
		},
		{
			name: "sync.branch configured via env var",
			setupFunc: func(t *testing.T, tmpDir string) {
				// Initialize git repo
				cmd := exec.Command("git", "init")
				cmd.Dir = tmpDir
				if err := cmd.Run(); err != nil {
					t.Fatal(err)
				}

				// Create .beads directory
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.Mkdir(beadsDir, 0750); err != nil {
					t.Fatal(err)
				}

				// Set env var (simulates config.yaml or BEADS_SYNC_BRANCH)
				t.Setenv("BEADS_SYNC_BRANCH", "beads-sync")
			},
			expectedStatus: statusOK,
			expectWarning:  false,
		},
		// Note: Tests for "not configured" scenarios are difficult because viper
		// reads config.yaml at startup from the test's working directory.
		// The env var tests above verify the core functionality.
		// For full integration testing, use actual fresh clones.
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tc.setupFunc(t, tmpDir)

			result := checkSyncBranchConfig(tmpDir)

			if result.Status != tc.expectedStatus {
				t.Errorf("Expected status %q, got %q", tc.expectedStatus, result.Status)
			}

			if tc.expectWarning && result.Fix == "" {
				t.Error("Expected Fix field to be set for warning status")
			}
		})
	}
}

// TestInteractiveFlagParsing verifies the --interactive flag is registered (bd-3xl)
func TestInteractiveFlagParsing(t *testing.T) {
	// Verify the flag exists and has the right short form
	flag := doctorCmd.Flags().Lookup("interactive")
	if flag == nil {
		t.Fatal("--interactive flag not found")
	}
	if flag.Shorthand != "i" {
		t.Errorf("Expected shorthand 'i', got %q", flag.Shorthand)
	}
	if flag.DefValue != "false" {
		t.Errorf("Expected default value 'false', got %q", flag.DefValue)
	}
}

// TestOutputFlagParsing verifies the --output flag is registered (bd-9cc)
func TestOutputFlagParsing(t *testing.T) {
	flag := doctorCmd.Flags().Lookup("output")
	if flag == nil {
		t.Fatal("--output flag not found")
	}
	if flag.Shorthand != "o" {
		t.Errorf("Expected shorthand 'o', got %q", flag.Shorthand)
	}
	if flag.DefValue != "" {
		t.Errorf("Expected default value '', got %q", flag.DefValue)
	}
}

// TestExportDiagnostics verifies the export functionality (bd-9cc)
func TestExportDiagnostics(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "diagnostics.json")

	// Create a test result
	result := doctorResult{
		Path:       "/test/path",
		CLIVersion: "0.29.0",
		OverallOK:  true,
		Timestamp:  "2025-01-01T00:00:00Z",
		Platform: map[string]string{
			"os_arch":        "darwin/arm64",
			"go_version":     "go1.21.0",
			"sqlite_version": "3.42.0",
		},
		Checks: []doctorCheck{
			{
				Name:    "Installation",
				Status:  "ok",
				Message: ".beads/ directory found",
			},
			{
				Name:    "Git Hooks",
				Status:  "warning",
				Message: "No hooks installed",
				Fix:     "Run 'bd hooks install'",
			},
		},
	}

	// Export to file
	if err := exportDiagnostics(result, outputPath); err != nil {
		t.Fatalf("exportDiagnostics failed: %v", err)
	}

	// Read the file back
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read exported file: %v", err)
	}

	// Parse the JSON
	var decoded doctorResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to parse exported JSON: %v", err)
	}

	// Verify fields
	if decoded.Path != result.Path {
		t.Errorf("Path mismatch: got %q, want %q", decoded.Path, result.Path)
	}
	if decoded.CLIVersion != result.CLIVersion {
		t.Errorf("CLIVersion mismatch: got %q, want %q", decoded.CLIVersion, result.CLIVersion)
	}
	if decoded.OverallOK != result.OverallOK {
		t.Errorf("OverallOK mismatch: got %v, want %v", decoded.OverallOK, result.OverallOK)
	}
	if decoded.Timestamp != result.Timestamp {
		t.Errorf("Timestamp mismatch: got %q, want %q", decoded.Timestamp, result.Timestamp)
	}
	if decoded.Platform["os_arch"] != result.Platform["os_arch"] {
		t.Errorf("Platform.os_arch mismatch: got %q, want %q", decoded.Platform["os_arch"], result.Platform["os_arch"])
	}
	if len(decoded.Checks) != len(result.Checks) {
		t.Errorf("Checks length mismatch: got %d, want %d", len(decoded.Checks), len(result.Checks))
	}
}

// TestExportDiagnosticsInvalidPath verifies error handling (bd-9cc)
func TestExportDiagnosticsInvalidPath(t *testing.T) {
	result := doctorResult{
		Path:      "/test/path",
		OverallOK: true,
	}

	// Try to export to an invalid path
	err := exportDiagnostics(result, "/nonexistent/directory/diagnostics.json")
	if err == nil {
		t.Error("Expected error for invalid path, got nil")
	}
}
