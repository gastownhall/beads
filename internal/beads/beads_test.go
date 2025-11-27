package beads

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindDatabasePathEnvVar(t *testing.T) {
	// Save original env var
	originalEnv := os.Getenv("BEADS_DB")
	defer func() {
		if originalEnv != "" {
			_ = os.Setenv("BEADS_DB", originalEnv)
		} else {
			_ = os.Unsetenv("BEADS_DB")
		}
	}()

	// Set env var to a test path (platform-agnostic)
	testPath := filepath.Join("test", "path", "test.db")
	_ = os.Setenv("BEADS_DB", testPath)

	result := FindDatabasePath()
	// FindDatabasePath canonicalizes to absolute path
	expectedPath, _ := filepath.Abs(testPath)
	if result != expectedPath {
		t.Errorf("Expected '%s', got '%s'", expectedPath, result)
	}
}

func TestFindDatabasePathInTree(t *testing.T) {
	// Save original env var and working directory
	originalEnv := os.Getenv("BEADS_DB")
	originalWd, _ := os.Getwd()
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DB", originalEnv)
		} else {
			os.Unsetenv("BEADS_DB")
		}
		os.Chdir(originalWd)
	}()

	// Clear env var
	os.Unsetenv("BEADS_DB")

	// Create temporary directory structure
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create .beads directory with a database file
	beadsDir := filepath.Join(tmpDir, ".beads")
	err = os.MkdirAll(beadsDir, 0o750)
	if err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	f, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("Failed to create db file: %v", err)
	}
	f.Close()

	// Create a subdirectory and change to it
	subDir := filepath.Join(tmpDir, "sub", "nested")
	err = os.MkdirAll(subDir, 0o750)
	if err != nil {
		t.Fatalf("Failed to create subdirectory: %v", err)
	}

	err = os.Chdir(subDir)
	if err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	// Should find the database in the parent directory tree
	result := FindDatabasePath()

	// Resolve symlinks for both paths (macOS uses /private/var symlinked to /var)
	expectedPath, err := filepath.EvalSymlinks(dbPath)
	if err != nil {
		expectedPath = dbPath
	}
	resultPath, err := filepath.EvalSymlinks(result)
	if err != nil {
		resultPath = result
	}

	if resultPath != expectedPath {
		t.Errorf("Expected '%s', got '%s'", expectedPath, resultPath)
	}
}

func TestFindDatabasePathNotFound(t *testing.T) {
	// Save original env var and working directory
	originalEnv := os.Getenv("BEADS_DB")
	originalWd, _ := os.Getwd()
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DB", originalEnv)
		} else {
			os.Unsetenv("BEADS_DB")
		}
		os.Chdir(originalWd)
	}()

	// Clear env var
	os.Unsetenv("BEADS_DB")

	// Create temporary directory without .beads
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	err = os.Chdir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	// Should return empty string (no database found)
	result := FindDatabasePath()
	// Result might be the home directory default if it exists, or empty string
	// Just verify it doesn't error
	_ = result
}

func TestFindJSONLPathWithExistingFile(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a .jsonl file
	jsonlPath := filepath.Join(tmpDir, "custom.jsonl")
	f, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatalf("Failed to create jsonl file: %v", err)
	}
	f.Close()

	// Create a fake database path in the same directory
	dbPath := filepath.Join(tmpDir, "test.db")

	// Should find the existing .jsonl file
	result := FindJSONLPath(dbPath)
	if result != jsonlPath {
		t.Errorf("Expected '%s', got '%s'", jsonlPath, result)
	}
}

func TestFindJSONLPathDefault(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a fake database path (no .jsonl files exist)
	dbPath := filepath.Join(tmpDir, "test.db")

	// bd-6xd: Should return default issues.jsonl (canonical name)
	result := FindJSONLPath(dbPath)
	expected := filepath.Join(tmpDir, "issues.jsonl")
	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

func TestFindJSONLPathEmpty(t *testing.T) {
	// Empty database path should return empty string
	result := FindJSONLPath("")
	if result != "" {
		t.Errorf("Expected empty string for empty db path, got '%s'", result)
	}
}

func TestFindJSONLPathMultipleFiles(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create multiple .jsonl files
	jsonlFiles := []string{"issues.jsonl", "backup.jsonl", "archive.jsonl"}
	for _, filename := range jsonlFiles {
		f, err := os.Create(filepath.Join(tmpDir, filename))
		if err != nil {
			t.Fatalf("Failed to create jsonl file: %v", err)
		}
		f.Close()
	}

	// Create a fake database path
	dbPath := filepath.Join(tmpDir, "test.db")

	// Should return the first .jsonl file found (lexicographically sorted by Glob)
	result := FindJSONLPath(dbPath)
	// Verify it's one of the .jsonl files we created
	found := false
	for _, filename := range jsonlFiles {
		if result == filepath.Join(tmpDir, filename) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected one of the created .jsonl files, got '%s'", result)
	}
}

func TestFindDatabasePathHomeDefault(t *testing.T) {
	// This test verifies that if no database is found, it falls back to home directory
	// We can't reliably test this without modifying the home directory, so we'll skip
	// creating the file and just verify the function doesn't crash

	originalEnv := os.Getenv("BEADS_DB")
	originalWd, _ := os.Getwd()
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DB", originalEnv)
		} else {
			os.Unsetenv("BEADS_DB")
		}
		os.Chdir(originalWd)
	}()

	os.Unsetenv("BEADS_DB")

	// Create an empty temp directory and cd to it
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	err = os.Chdir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	// Call FindDatabasePath - it might return home dir default or empty string
	result := FindDatabasePath()

	// If result is not empty, verify it contains .beads
	if result != "" && !filepath.IsAbs(result) {
		t.Errorf("Expected absolute path or empty string, got '%s'", result)
	}
}
