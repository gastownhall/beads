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
	// Save original env var
	originalEnv := os.Getenv("BEADS_DB")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DB", originalEnv)
		} else {
			os.Unsetenv("BEADS_DB")
		}
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

	t.Chdir(subDir)

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
	// Save original env var
	originalEnv := os.Getenv("BEADS_DB")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DB", originalEnv)
		} else {
			os.Unsetenv("BEADS_DB")
		}
	}()

	// Clear env var
	os.Unsetenv("BEADS_DB")

	// Create temporary directory without .beads
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Chdir(tmpDir)

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

// TestFindJSONLPathSkipsDeletions verifies that FindJSONLPath skips deletions.jsonl
// and merge artifacts to prevent corruption (bd-tqo fix)
func TestFindJSONLPathSkipsDeletions(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		expected string
	}{
		{
			name:     "prefers issues.jsonl over deletions.jsonl",
			files:    []string{"deletions.jsonl", "issues.jsonl"},
			expected: "issues.jsonl",
		},
		{
			name:     "skips deletions.jsonl when only option",
			files:    []string{"deletions.jsonl"},
			expected: "issues.jsonl", // Falls back to default
		},
		{
			name:     "skips merge artifacts",
			files:    []string{"beads.base.jsonl", "beads.left.jsonl", "issues.jsonl"},
			expected: "issues.jsonl",
		},
		{
			name:     "prefers issues over beads",
			files:    []string{"beads.jsonl", "issues.jsonl"},
			expected: "issues.jsonl",
		},
		{
			name:     "uses beads.jsonl as legacy fallback",
			files:    []string{"beads.jsonl", "deletions.jsonl"},
			expected: "beads.jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "beads-jsonl-test-*")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(tmpDir)

			// Create test files
			for _, file := range tt.files {
				path := filepath.Join(tmpDir, file)
				if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			dbPath := filepath.Join(tmpDir, "test.db")
			result := FindJSONLPath(dbPath)
			expected := filepath.Join(tmpDir, tt.expected)

			if result != expected {
				t.Errorf("FindJSONLPath() = %q, want %q", result, expected)
			}
		})
	}
}

// TestHasBeadsProjectFiles verifies that hasBeadsProjectFiles correctly
// distinguishes between project directories and daemon-only directories (bd-420)
func TestHasBeadsProjectFiles(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		expected bool
	}{
		{
			name:     "empty directory",
			files:    []string{},
			expected: false,
		},
		{
			name:     "daemon registry only",
			files:    []string{"registry.json", "registry.lock"},
			expected: false,
		},
		{
			name:     "has database",
			files:    []string{"beads.db"},
			expected: true,
		},
		{
			name:     "has issues.jsonl",
			files:    []string{"issues.jsonl"},
			expected: true,
		},
		{
			name:     "has metadata.json",
			files:    []string{"metadata.json"},
			expected: true,
		},
		{
			name:     "has config.yaml",
			files:    []string{"config.yaml"},
			expected: true,
		},
		{
			name:     "ignores backup db",
			files:    []string{"beads.backup.db"},
			expected: false,
		},
		{
			name:     "ignores vc.db",
			files:    []string{"vc.db"},
			expected: false,
		},
		{
			name:     "real db with backup",
			files:    []string{"beads.db", "beads.backup.db"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "beads-project-test-*")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(tmpDir)

			// Create test files
			for _, file := range tt.files {
				path := filepath.Join(tmpDir, file)
				if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			result := hasBeadsProjectFiles(tmpDir)
			if result != tt.expected {
				t.Errorf("hasBeadsProjectFiles() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestFindBeadsDirSkipsDaemonRegistry verifies that FindBeadsDir skips
// directories containing only daemon registry files (bd-420)
func TestFindBeadsDirSkipsDaemonRegistry(t *testing.T) {
	// Save original state
	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()
	os.Unsetenv("BEADS_DIR")

	// Create temp directory structure
	tmpDir, err := os.MkdirTemp("", "beads-daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create .beads with only daemon registry files (should be skipped)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "registry.json"), []byte("[]"), 0644); err != nil {
		t.Fatal(err)
	}

	// Change to temp dir
	t.Chdir(tmpDir)

	// Should NOT find the daemon-only directory
	result := FindBeadsDir()
	if result != "" {
		// Resolve symlinks for comparison
		resultResolved, _ := filepath.EvalSymlinks(result)
		beadsDirResolved, _ := filepath.EvalSymlinks(beadsDir)
		if resultResolved == beadsDirResolved {
			t.Errorf("FindBeadsDir() should skip daemon-only directory, got %q", result)
		}
	}
}

// TestFindBeadsDirValidatesBeadsDirEnv verifies that BEADS_DIR env var
// is validated for project files (bd-420)
func TestFindBeadsDirValidatesBeadsDirEnv(t *testing.T) {
	// Save original state
	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()

	// Create temp directory with only daemon registry files
	tmpDir, err := os.MkdirTemp("", "beads-env-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "registry.json"), []byte("[]"), 0644); err != nil {
		t.Fatal(err)
	}

	// Set BEADS_DIR to daemon-only directory
	os.Setenv("BEADS_DIR", tmpDir)

	// Should NOT return the daemon-only directory
	result := FindBeadsDir()
	if result != "" {
		resultResolved, _ := filepath.EvalSymlinks(result)
		tmpDirResolved, _ := filepath.EvalSymlinks(tmpDir)
		if resultResolved == tmpDirResolved {
			t.Errorf("FindBeadsDir() should skip BEADS_DIR with only daemon files, got %q", result)
		}
	}

	// Now add a project file
	if err := os.WriteFile(filepath.Join(tmpDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Should now return the directory
	result = FindBeadsDir()
	if result == "" {
		t.Error("FindBeadsDir() should return BEADS_DIR with project files")
	}
}

func TestFindDatabasePathHomeDefault(t *testing.T) {
	// This test verifies that if no database is found, it falls back to home directory
	// We can't reliably test this without modifying the home directory, so we'll skip
	// creating the file and just verify the function doesn't crash

	originalEnv := os.Getenv("BEADS_DB")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DB", originalEnv)
		} else {
			os.Unsetenv("BEADS_DB")
		}
	}()

	os.Unsetenv("BEADS_DB")

	// Create an empty temp directory and cd to it
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Chdir(tmpDir)

	// Call FindDatabasePath - it might return home dir default or empty string
	result := FindDatabasePath()

	// If result is not empty, verify it contains .beads
	if result != "" && !filepath.IsAbs(result) {
		t.Errorf("Expected absolute path or empty string, got '%s'", result)
	}
}

// TestFollowRedirect tests the redirect file functionality
func TestFollowRedirect(t *testing.T) {
	tests := []struct {
		name           string
		setupFunc      func(t *testing.T, tmpDir string) (stubDir, targetDir string)
		expectRedirect bool
	}{
		{
			name: "no redirect file - returns original",
			setupFunc: func(t *testing.T, tmpDir string) (string, string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.MkdirAll(beadsDir, 0755); err != nil {
					t.Fatal(err)
				}
				return beadsDir, ""
			},
			expectRedirect: false,
		},
		{
			name: "relative path redirect",
			setupFunc: func(t *testing.T, tmpDir string) (string, string) {
				// Create stub .beads with redirect
				stubDir := filepath.Join(tmpDir, "project", ".beads")
				if err := os.MkdirAll(stubDir, 0755); err != nil {
					t.Fatal(err)
				}

				// Create target .beads directory
				targetDir := filepath.Join(tmpDir, "actual", ".beads")
				if err := os.MkdirAll(targetDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(targetDir, "beads.db"), []byte{}, 0644); err != nil {
					t.Fatal(err)
				}

				// Write redirect file with relative path
				redirectPath := filepath.Join(stubDir, "redirect")
				if err := os.WriteFile(redirectPath, []byte("../actual/.beads\n"), 0644); err != nil {
					t.Fatal(err)
				}

				return stubDir, targetDir
			},
			expectRedirect: true,
		},
		{
			name: "absolute path redirect",
			setupFunc: func(t *testing.T, tmpDir string) (string, string) {
				// Create stub .beads with redirect
				stubDir := filepath.Join(tmpDir, "project", ".beads")
				if err := os.MkdirAll(stubDir, 0755); err != nil {
					t.Fatal(err)
				}

				// Create target .beads directory
				targetDir := filepath.Join(tmpDir, "actual", ".beads")
				if err := os.MkdirAll(targetDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(targetDir, "beads.db"), []byte{}, 0644); err != nil {
					t.Fatal(err)
				}

				// Write redirect file with absolute path
				redirectPath := filepath.Join(stubDir, "redirect")
				if err := os.WriteFile(redirectPath, []byte(targetDir+"\n"), 0644); err != nil {
					t.Fatal(err)
				}

				return stubDir, targetDir
			},
			expectRedirect: true,
		},
		{
			name: "redirect with comments",
			setupFunc: func(t *testing.T, tmpDir string) (string, string) {
				// Create stub .beads with redirect
				stubDir := filepath.Join(tmpDir, "project", ".beads")
				if err := os.MkdirAll(stubDir, 0755); err != nil {
					t.Fatal(err)
				}

				// Create target .beads directory
				targetDir := filepath.Join(tmpDir, "actual", ".beads")
				if err := os.MkdirAll(targetDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(targetDir, "beads.db"), []byte{}, 0644); err != nil {
					t.Fatal(err)
				}

				// Write redirect file with comments
				redirectPath := filepath.Join(stubDir, "redirect")
				content := "# Redirect to actual beads location\n# This is a workspace redirect\n" + targetDir + "\n"
				if err := os.WriteFile(redirectPath, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}

				return stubDir, targetDir
			},
			expectRedirect: true,
		},
		{
			name: "redirect to non-existent directory - returns original",
			setupFunc: func(t *testing.T, tmpDir string) (string, string) {
				stubDir := filepath.Join(tmpDir, "project", ".beads")
				if err := os.MkdirAll(stubDir, 0755); err != nil {
					t.Fatal(err)
				}

				// Write redirect to non-existent path
				redirectPath := filepath.Join(stubDir, "redirect")
				if err := os.WriteFile(redirectPath, []byte("/nonexistent/path/.beads\n"), 0644); err != nil {
					t.Fatal(err)
				}

				return stubDir, ""
			},
			expectRedirect: false, // Should fall back to original
		},
		{
			name: "empty redirect file - returns original",
			setupFunc: func(t *testing.T, tmpDir string) (string, string) {
				stubDir := filepath.Join(tmpDir, "project", ".beads")
				if err := os.MkdirAll(stubDir, 0755); err != nil {
					t.Fatal(err)
				}

				// Write empty redirect file
				redirectPath := filepath.Join(stubDir, "redirect")
				if err := os.WriteFile(redirectPath, []byte(""), 0644); err != nil {
					t.Fatal(err)
				}

				return stubDir, ""
			},
			expectRedirect: false,
		},
		{
			name: "redirect file with only comments - returns original",
			setupFunc: func(t *testing.T, tmpDir string) (string, string) {
				stubDir := filepath.Join(tmpDir, "project", ".beads")
				if err := os.MkdirAll(stubDir, 0755); err != nil {
					t.Fatal(err)
				}

				// Write redirect file with only comments
				redirectPath := filepath.Join(stubDir, "redirect")
				if err := os.WriteFile(redirectPath, []byte("# Just a comment\n# Another comment\n"), 0644); err != nil {
					t.Fatal(err)
				}

				return stubDir, ""
			},
			expectRedirect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "beads-redirect-test-*")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(tmpDir)

			stubDir, targetDir := tt.setupFunc(t, tmpDir)

			result := followRedirect(stubDir)

			// Resolve symlinks for comparison (macOS uses /private/var)
			resultResolved, _ := filepath.EvalSymlinks(result)
			stubResolved, _ := filepath.EvalSymlinks(stubDir)

			if tt.expectRedirect {
				targetResolved, _ := filepath.EvalSymlinks(targetDir)
				if resultResolved != targetResolved {
					t.Errorf("followRedirect() = %q, want %q", result, targetDir)
				}
			} else {
				if resultResolved != stubResolved {
					t.Errorf("followRedirect() = %q, want original %q", result, stubDir)
				}
			}
		})
	}
}

// TestFindDatabasePathWithRedirect tests that FindDatabasePath follows redirects
func TestFindDatabasePathWithRedirect(t *testing.T) {
	// Save original state
	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()
	os.Unsetenv("BEADS_DIR")

	// Create temp directory structure
	tmpDir, err := os.MkdirTemp("", "beads-redirect-finddb-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create stub .beads with redirect
	stubDir := filepath.Join(tmpDir, "project", ".beads")
	if err := os.MkdirAll(stubDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create target .beads directory with actual database
	targetDir := filepath.Join(tmpDir, "actual", ".beads")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetDB := filepath.Join(targetDir, "beads.db")
	if err := os.WriteFile(targetDB, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Write redirect file
	redirectPath := filepath.Join(stubDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../actual/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Change to project directory
	projectDir := filepath.Join(tmpDir, "project")
	t.Chdir(projectDir)

	// FindDatabasePath should follow the redirect
	result := FindDatabasePath()

	// Resolve symlinks for comparison
	resultResolved, _ := filepath.EvalSymlinks(result)
	targetDBResolved, _ := filepath.EvalSymlinks(targetDB)

	if resultResolved != targetDBResolved {
		t.Errorf("FindDatabasePath() = %q, want %q (via redirect)", result, targetDB)
	}
}

// TestFindBeadsDirWithRedirect tests that FindBeadsDir follows redirects
func TestFindBeadsDirWithRedirect(t *testing.T) {
	// Save original state
	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()
	os.Unsetenv("BEADS_DIR")

	// Create temp directory structure
	tmpDir, err := os.MkdirTemp("", "beads-redirect-finddir-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create stub .beads with redirect
	stubDir := filepath.Join(tmpDir, "project", ".beads")
	if err := os.MkdirAll(stubDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create target .beads directory with project files
	targetDir := filepath.Join(tmpDir, "actual", ".beads")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "issues.jsonl"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Write redirect file
	redirectPath := filepath.Join(stubDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../actual/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Change to project directory
	projectDir := filepath.Join(tmpDir, "project")
	t.Chdir(projectDir)

	// FindBeadsDir should follow the redirect
	result := FindBeadsDir()

	// Resolve symlinks for comparison
	resultResolved, _ := filepath.EvalSymlinks(result)
	targetDirResolved, _ := filepath.EvalSymlinks(targetDir)

	if resultResolved != targetDirResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (via redirect)", result, targetDir)
	}
}
