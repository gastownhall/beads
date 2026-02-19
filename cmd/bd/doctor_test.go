//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/git"
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
			result := doctor.DetectHashBasedIDs(db, tt.sampleIDs)
			if result != tt.expected {
				t.Errorf("detectHashBasedIDs() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestCheckIDFormat(t *testing.T) {
	t.Skip("SQLite-specific: creates SQLite database directly; Dolt backend can't read it")
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
			expectedStatus: doctor.StatusOK,
		},
		{
			name:           "hash IDs all numeric with leading zeros",
			issueIDs:       []string{"bd-0088", "bd-02a4", "bd-05a1", "bd-0458"},
			createTable:    false,
			expectedStatus: doctor.StatusOK,
		},
		{
			name:           "hash IDs with child_counters table",
			issueIDs:       []string{"bd-123", "bd-456"},
			createTable:    true,
			expectedStatus: doctor.StatusOK,
		},
		{
			name:           "sequential IDs",
			issueIDs:       []string{"bd-1", "bd-2", "bd-3", "bd-4"},
			createTable:    false,
			expectedStatus: doctor.StatusWarning,
		},
		{
			name:           "mixed: mostly hash IDs",
			issueIDs:       []string{"bd-0088", "bd-0134cc5a", "bd-02a4"},
			createTable:    false,
			expectedStatus: doctor.StatusOK, // Variable length = hash IDs
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
			check := doctor.CheckIDFormat(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}

			if tt.expectedStatus == doctor.StatusOK && check.Status == doctor.StatusOK {
				if !strings.Contains(check.Message, "hash-based") {
					t.Errorf("Expected hash-based message, got: %s", check.Message)
				}
			}

			if tt.expectedStatus == doctor.StatusWarning && check.Status == doctor.StatusWarning {
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
	check := doctor.CheckInstallation(tmpDir)

	if check.Status != doctor.StatusError {
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

	check = doctor.CheckInstallation(tmpDir)
	if check.Status != doctor.StatusOK {
		t.Errorf("Expected ok status, got %s", check.Status)
	}
}

func TestCheckDatabaseVersionJSONLMode(t *testing.T) {
	// Dolt backend doesn't have a "JSONL-only" mode; it reports fresh clone
	// when no dolt/ directory exists but JSONL is present.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("no-db: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := doctor.CheckDatabaseVersion(tmpDir, Version)

	// Dolt backend sees JSONL without dolt/ dir → fresh clone warning
	if check.Status != doctor.StatusWarning {
		t.Errorf("Expected warning status for Dolt fresh clone, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "Fresh clone") {
		t.Errorf("Expected fresh clone message, got %s", check.Message)
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

	check := doctor.CheckDatabaseVersion(tmpDir, Version)

	if check.Status != doctor.StatusWarning {
		t.Errorf("Expected warning status for fresh clone, got %s", check.Status)
	}
	if !strings.Contains(check.Message, "Fresh clone detected") {
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
		{"0.20.1", "0.20.1", 0},  // Equal
		{"0.20.1", "0.20.0", 1},  // v1 > v2
		{"0.20.0", "0.20.1", -1}, // v1 < v2
		{"0.10.0", "0.9.9", 1},   // Major.minor comparison
		{"1.0.0", "0.99.99", 1},  // Major version difference
		{"0.20.1", "0.3.0", 1},   // String comparison would fail this
		{"1.2", "1.2.0", 0},      // Different length, equal
		{"1.2.1", "1.2", 1},      // Different length, v1 > v2
	}

	for _, tc := range tests {
		result := doctor.CompareVersions(tc.v1, tc.v2)
		if result != tc.expected {
			t.Errorf("doctor.CompareVersions(%q, %q) = %d, expected %d", tc.v1, tc.v2, result, tc.expected)
		}
	}
}

func TestCheckPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	check := doctor.CheckPermissions(tmpDir)

	if check.Status != doctor.StatusOK {
		t.Errorf("Expected ok status for writable directory, got %s: %s", check.Status, check.Message)
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

	count, prefixes, err := doctor.CountJSONLIssues(jsonlPath)

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
			expectedStatus: doctor.StatusOK,
			expectWarning:  false,
		},
		{
			name:           "all hooks installed",
			hasGitDir:      true,
			installedHooks: []string{"pre-commit", "post-merge", "pre-push"},
			expectedStatus: doctor.StatusOK,
			expectWarning:  false,
		},
		{
			name:           "no hooks installed",
			hasGitDir:      true,
			installedHooks: []string{},
			expectedStatus: doctor.StatusWarning,
			expectWarning:  true,
		},
		{
			name:           "some hooks installed",
			hasGitDir:      true,
			installedHooks: []string{"pre-commit"},
			expectedStatus: doctor.StatusWarning,
			expectWarning:  true,
		},
		{
			name:           "partial hooks installed",
			hasGitDir:      true,
			installedHooks: []string{"pre-commit", "post-merge"},
			expectedStatus: doctor.StatusWarning,
			expectWarning:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			runInDir(t, tmpDir, func() {
				if tc.hasGitDir {
					// Copy cached git template (bd-ktng optimization)
					initGitTemplate()
					if gitTemplateErr != nil {
						t.Fatalf("git template init failed: %v", gitTemplateErr)
					}
					if err := copyGitDir(gitTemplateDir, tmpDir); err != nil {
						t.Fatalf("failed to copy git template: %v", err)
					}

					gitDir, err := git.GetGitDir()
					if err != nil {
						t.Fatalf("git.GetGitDir() failed: %v", err)
					}
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

				check := doctor.CheckGitHooks(Version)

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
		})
	}
}

func TestCheckClaudePlugin(t *testing.T) {
	tests := []struct {
		name           string
		claudeCodeEnv  string
		expectedStatus string
		expectedMsg    string
	}{
		{
			name:           "not running in claude code",
			claudeCodeEnv:  "",
			expectedStatus: doctor.StatusOK,
			expectedMsg:    "N/A (not running in Claude Code)",
		},
		{
			name:           "not running in claude code (0)",
			claudeCodeEnv:  "0",
			expectedStatus: doctor.StatusOK,
			expectedMsg:    "N/A (not running in Claude Code)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Save original env
			origEnv := os.Getenv("CLAUDECODE")
			defer func() {
				if origEnv == "" {
					os.Unsetenv("CLAUDECODE")
				} else {
					os.Setenv("CLAUDECODE", origEnv)
				}
			}()

			// Set test env
			if tc.claudeCodeEnv == "" {
				os.Unsetenv("CLAUDECODE")
			} else {
				os.Setenv("CLAUDECODE", tc.claudeCodeEnv)
			}

			check := doctor.CheckClaudePlugin()

			if check.Status != tc.expectedStatus {
				t.Errorf("Expected status %s, got %s", tc.expectedStatus, check.Status)
			}

			if check.Message != tc.expectedMsg {
				t.Errorf("Expected message %q, got %q", tc.expectedMsg, check.Message)
			}
		})
	}
}

func TestGetClaudePluginVersion(t *testing.T) {
	tests := []struct {
		name            string
		pluginJSON      string
		expectInstalled bool
		expectVersion   string
		expectError     bool
	}{
		{
			name: "plugin installed v1 format",
			pluginJSON: `{
				"version": 1,
				"plugins": {
					"beads@beads-marketplace": {
						"version": "0.21.3"
					}
				}
			}`,
			expectInstalled: true,
			expectVersion:   "0.21.3",
			expectError:     false,
		},
		{
			name: "plugin installed v2 format (GH#741)",
			pluginJSON: `{
				"version": 2,
				"plugins": {
					"beads@beads-marketplace": [
						{
							"scope": "user",
							"installPath": "/path/to/plugin",
							"version": "1.0.0",
							"installedAt": "2025-11-25T19:20:27.889Z",
							"lastUpdated": "2025-11-25T19:20:27.889Z",
							"gitCommitSha": "abc123",
							"isLocal": true
						}
					]
				}
			}`,
			expectInstalled: true,
			expectVersion:   "1.0.0",
			expectError:     false,
		},
		{
			name: "plugin not installed v2 format",
			pluginJSON: `{
				"version": 2,
				"plugins": {
					"other-plugin@marketplace": [
						{
							"scope": "user",
							"version": "2.0.0"
						}
					]
				}
			}`,
			expectInstalled: false,
			expectVersion:   "",
			expectError:     false,
		},
		{
			name: "plugin not installed v1 format",
			pluginJSON: `{
				"version": 1,
				"plugins": {
					"other-plugin@marketplace": {
						"version": "1.0.0"
					}
				}
			}`,
			expectInstalled: false,
			expectVersion:   "",
			expectError:     false,
		},
		{
			name:            "invalid json",
			pluginJSON:      `{invalid json`,
			expectInstalled: false,
			expectVersion:   "",
			expectError:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create temp dir with plugin file
			tmpHome := t.TempDir()
			pluginDir := filepath.Join(tmpHome, ".claude", "plugins")
			if err := os.MkdirAll(pluginDir, 0750); err != nil {
				t.Fatal(err)
			}
			pluginPath := filepath.Join(pluginDir, "installed_plugins.json")
			if err := os.WriteFile(pluginPath, []byte(tc.pluginJSON), 0600); err != nil {
				t.Fatal(err)
			}

			// Temporarily override home directory
			origHome := os.Getenv("HOME")
			os.Setenv("HOME", tmpHome)
			defer os.Setenv("HOME", origHome)

			version, installed, err := doctor.GetClaudePluginVersion()

			if tc.expectError && err == nil {
				t.Error("Expected error, got nil")
			}
			if !tc.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if installed != tc.expectInstalled {
				t.Errorf("Expected installed=%v, got %v", tc.expectInstalled, installed)
			}
			if version != tc.expectVersion {
				t.Errorf("Expected version %q, got %q", tc.expectVersion, version)
			}
		})
	}
}

func TestCheckMetadataVersionTracking(t *testing.T) {
	// GH#662: Tests updated to use .local_version file instead of metadata.json:LastBdVersion
	tests := []struct {
		name           string
		setupVersion   func(beadsDir string) error
		expectedStatus string
		expectWarning  bool
	}{
		{
			name: "valid current version",
			setupVersion: func(beadsDir string) error {
				return os.WriteFile(filepath.Join(beadsDir, ".local_version"), []byte(Version+"\n"), 0644)
			},
			expectedStatus: doctor.StatusOK,
			expectWarning:  false,
		},
		{
			name: "slightly outdated version",
			setupVersion: func(beadsDir string) error {
				// Use a version that's less than 10 minor versions behind current
				return os.WriteFile(filepath.Join(beadsDir, ".local_version"), []byte("0.43.0\n"), 0644)
			},
			expectedStatus: doctor.StatusOK,
			expectWarning:  false,
		},
		{
			name: "very old version",
			setupVersion: func(beadsDir string) error {
				// Use a version that's 10+ minor versions behind current (triggers warning)
				return os.WriteFile(filepath.Join(beadsDir, ".local_version"), []byte("0.29.0\n"), 0644)
			},
			expectedStatus: doctor.StatusWarning,
			expectWarning:  true,
		},
		{
			name: "empty version file",
			setupVersion: func(beadsDir string) error {
				return os.WriteFile(filepath.Join(beadsDir, ".local_version"), []byte(""), 0644)
			},
			expectedStatus: doctor.StatusWarning,
			expectWarning:  true,
		},
		{
			name: "invalid version format",
			setupVersion: func(beadsDir string) error {
				return os.WriteFile(filepath.Join(beadsDir, ".local_version"), []byte("invalid-version\n"), 0644)
			},
			expectedStatus: doctor.StatusWarning,
			expectWarning:  true,
		},
		{
			name: "missing .local_version file",
			setupVersion: func(beadsDir string) error {
				// Don't create .local_version
				return nil
			},
			expectedStatus: doctor.StatusWarning,
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

			// Setup .local_version file
			if err := tc.setupVersion(beadsDir); err != nil {
				t.Fatal(err)
			}

			check := doctor.CheckMetadataVersionTracking(tmpDir, Version)

			if check.Status != tc.expectedStatus {
				t.Errorf("Expected status %s, got %s (message: %s)", tc.expectedStatus, check.Status, check.Message)
			}

			if tc.expectWarning && check.Status == doctor.StatusWarning && check.Fix == "" {
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
		{"0.1", true},      // Major.minor is valid
		{"1", true},        // Just major is valid
		{"", false},        // Empty is invalid
		{"invalid", false}, // Non-numeric is invalid
		{"0.a.2", false},   // Letters in parts are invalid
		{"1.2.3.4", true},  // Extra parts are ok
	}

	for _, tc := range tests {
		result := doctor.IsValidSemver(tc.version)
		if result != tc.expected {
			t.Errorf("doctor.IsValidSemver(%q) = %v, expected %v", tc.version, result, tc.expected)
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
		result := doctor.ParseVersionParts(tc.version)
		if len(result) != len(tc.expected) {
			t.Errorf("doctor.ParseVersionParts(%q) returned %d parts, expected %d", tc.version, len(result), len(tc.expected))
			continue
		}
		for i := range result {
			if result[i] != tc.expected[i] {
				t.Errorf("doctor.ParseVersionParts(%q)[%d] = %d, expected %d", tc.version, i, result[i], tc.expected[i])
			}
		}
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

// TestDoctor_WithBEADS_DIR tests that doctor respects BEADS_DIR environment variable
func TestDoctor_WithBEADS_DIR(t *testing.T) {
	// Reset Cobra flags to avoid interference
	defer func() {
		doctorFix = false
		doctorYes = false
		doctorInteractive = false
		doctorDryRun = false
	}()

	// Create target directory (where BEADS_DIR points)
	targetDir := t.TempDir()
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	if err := os.Mkdir(targetBeadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Create CWD directory (where we run from - should be ignored)
	cwdDir := t.TempDir()
	// Explicitly do NOT create .beads here - doctor should not check CWD

	// Save original working directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Change to CWD (no .beads)
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(origDir)
	}()

	// Set BEADS_DIR to point to target/.beads
	t.Setenv("BEADS_DIR", targetBeadsDir)

	// The doctor command's Run function determines path from BEADS_DIR
	// We test that by verifying runDiagnostics receives the parent of BEADS_DIR
	// Direct call: runDiagnostics uses the path it's given
	// Through cobra: Run function computes path from BEADS_DIR

	// Test the path resolution logic directly
	beadsDir := os.Getenv("BEADS_DIR")
	if beadsDir == "" {
		t.Fatal("BEADS_DIR should be set")
	}
	checkPath := filepath.Dir(beadsDir) // Parent of .beads

	// Verify we get the target directory, not CWD
	if checkPath != targetDir {
		t.Errorf("Expected checkPath to be %s, got %s", targetDir, checkPath)
	}

	// Run diagnostics on the computed path (simulates what cobra Run does)
	result := runDiagnostics(checkPath)

	// Should find .beads at target location
	if len(result.Checks) == 0 {
		t.Fatal("Expected at least one check")
	}
	installCheck := result.Checks[0]
	if installCheck.Status != "ok" {
		t.Errorf("Expected Installation to pass (found .beads at BEADS_DIR parent), got status=%s, message=%s",
			installCheck.Status, installCheck.Message)
	}
}

// TestDoctor_ExplicitPathOverridesBEADS_DIR tests that explicit path arg takes precedence
func TestDoctor_ExplicitPathOverridesBEADS_DIR(t *testing.T) {
	// Create two directories: one for explicit path, one for BEADS_DIR
	explicitDir := t.TempDir()
	explicitBeadsDir := filepath.Join(explicitDir, ".beads")
	if err := os.Mkdir(explicitBeadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	// Mark explicit with a file so we can verify it was used
	if err := os.WriteFile(filepath.Join(explicitBeadsDir, "explicit-marker"), []byte("explicit"), 0644); err != nil {
		t.Fatal(err)
	}

	beadsDirTarget := t.TempDir()
	beadsDirBeads := filepath.Join(beadsDirTarget, ".beads")
	if err := os.Mkdir(beadsDirBeads, 0750); err != nil {
		t.Fatal(err)
	}

	// Set BEADS_DIR
	t.Setenv("BEADS_DIR", beadsDirBeads)

	// Test precedence: explicit arg > BEADS_DIR > CWD
	// When explicit path is given, BEADS_DIR should be ignored

	// Simulate the logic from doctor.go's Run function:
	args := []string{explicitDir}
	var checkPath string
	if len(args) > 0 {
		checkPath = args[0]
	} else if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		checkPath = filepath.Dir(beadsDir)
	} else {
		checkPath = "."
	}

	// Should use explicit path, not BEADS_DIR
	if checkPath != explicitDir {
		t.Errorf("Expected explicit path %s to take precedence, got %s", explicitDir, checkPath)
	}

	// Verify the marker file exists at the chosen path
	markerPath := filepath.Join(checkPath, ".beads", "explicit-marker")
	if _, err := os.Stat(markerPath); os.IsNotExist(err) {
		t.Error("Expected to find explicit-marker in chosen path - wrong directory was selected")
	}
}
