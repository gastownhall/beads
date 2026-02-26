package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

func TestFindDatabasePathEnvVar(t *testing.T) {
	// Save original env vars
	originalDB := os.Getenv("BEADS_DB")
	originalDir := os.Getenv("BEADS_DIR")
	defer func() {
		if originalDB != "" {
			_ = os.Setenv("BEADS_DB", originalDB)
		} else {
			_ = os.Unsetenv("BEADS_DB")
		}
		if originalDir != "" {
			_ = os.Setenv("BEADS_DIR", originalDir)
		} else {
			_ = os.Unsetenv("BEADS_DIR")
		}
	}()

	// Clear BEADS_DIR to prevent it from interfering
	_ = os.Unsetenv("BEADS_DIR")

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
	// Save original env vars
	originalDB := os.Getenv("BEADS_DB")
	originalDir := os.Getenv("BEADS_DIR")
	defer func() {
		if originalDB != "" {
			os.Setenv("BEADS_DB", originalDB)
		} else {
			os.Unsetenv("BEADS_DB")
		}
		if originalDir != "" {
			os.Setenv("BEADS_DIR", originalDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()

	// Clear env vars
	os.Unsetenv("BEADS_DB")

	// Create temporary directory structure
	tmpDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create .beads directory with a dolt database directory
	beadsDir := filepath.Join(tmpDir, ".beads")
	err = os.MkdirAll(beadsDir, 0o750)
	if err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o750); err != nil {
		t.Fatalf("Failed to create dolt dir: %v", err)
	}

	// Set BEADS_DIR to our test .beads directory to override git repo detection
	os.Setenv("BEADS_DIR", beadsDir)

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
	expectedPath, err := filepath.EvalSymlinks(doltDir)
	if err != nil {
		expectedPath = doltDir
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

			result := FollowRedirect(stubDir)

			// Resolve symlinks for comparison (macOS uses /private/var)
			resultResolved, _ := filepath.EvalSymlinks(result)
			stubResolved, _ := filepath.EvalSymlinks(stubDir)

			if tt.expectRedirect {
				targetResolved, _ := filepath.EvalSymlinks(targetDir)
				if resultResolved != targetResolved {
					t.Errorf("FollowRedirect() = %q, want %q", result, targetDir)
				}
			} else {
				if resultResolved != stubResolved {
					t.Errorf("FollowRedirect() = %q, want original %q", result, stubDir)
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

	// Create target .beads directory with actual dolt database
	targetDir := filepath.Join(tmpDir, "actual", ".beads")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetDolt := filepath.Join(targetDir, "dolt")
	if err := os.MkdirAll(targetDolt, 0755); err != nil {
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
	targetDoltResolved, _ := filepath.EvalSymlinks(targetDolt)

	if resultResolved != targetDoltResolved {
		t.Errorf("FindDatabasePath() = %q, want %q (via redirect)", result, targetDolt)
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
	if err := os.WriteFile(filepath.Join(targetDir, "metadata.json"), []byte(`{"database":"dolt"}`), 0644); err != nil {
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

// TestFindGitRoot_RegularRepo tests that findGitRoot returns the correct path
// in a regular git repository (not a worktree).
func TestFindGitRoot_RegularRepo(t *testing.T) {
	// Create temporary directory for our test repo
	tmpDir, err := os.MkdirTemp("", "beads-gitroot-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize a git repository
	repoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Configure git user for the test repo (required for commits)
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = repoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoDir
	_ = cmd.Run()

	// Create a subdirectory and change to it
	subDir := filepath.Join(repoDir, "sub", "nested")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(subDir)
	git.ResetCaches() // Reset after chdir for caching tests

	// findGitRoot should return the repo root
	result := findGitRoot()

	// Resolve symlinks for comparison (macOS /var -> /private/var)
	resultResolved, _ := filepath.EvalSymlinks(result)
	repoDirResolved, _ := filepath.EvalSymlinks(repoDir)

	if resultResolved != repoDirResolved {
		t.Errorf("findGitRoot() = %q, want %q", result, repoDir)
	}
}

// TestFindGitRoot_Worktree tests that findGitRoot returns the worktree root
// (not the main repository root) when inside a git worktree. This is critical
// for bd-745 - ensuring database discovery works correctly in worktrees.
func TestFindGitRoot_Worktree(t *testing.T) {
	// Create temporary directory for our test
	tmpDir, err := os.MkdirTemp("", "beads-worktree-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize a git repository
	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Configure git user for the test repo (required for commits)
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()

	// Create an initial commit (required for worktree)
	dummyFile := filepath.Join(mainRepoDir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "README.md")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// Create a worktree
	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	defer func() {
		// Clean up worktree
		cmd := exec.Command("git", "worktree", "remove", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	// Change to the worktree directory
	t.Chdir(worktreeDir)
	git.ResetCaches() // Reset after chdir for caching tests

	// findGitRoot should return the WORKTREE root, not the main repo root
	result := findGitRoot()

	// Resolve symlinks for comparison
	resultResolved, _ := filepath.EvalSymlinks(result)
	worktreeDirResolved, _ := filepath.EvalSymlinks(worktreeDir)
	mainRepoDirResolved, _ := filepath.EvalSymlinks(mainRepoDir)

	if resultResolved != worktreeDirResolved {
		t.Errorf("findGitRoot() = %q, want worktree %q (not main repo %q)", result, worktreeDir, mainRepoDir)
	}

	// Additional verification: ensure we're NOT returning the main repo
	if resultResolved == mainRepoDirResolved {
		t.Errorf("findGitRoot() returned main repo %q instead of worktree %q - worktree detection is broken!", mainRepoDir, worktreeDir)
	}
}

// TestFindGitRoot_NotGitRepo tests that findGitRoot returns an empty string
// when not inside a git repository.
func TestFindGitRoot_NotGitRepo(t *testing.T) {
	// Create temporary directory that is NOT a git repo
	tmpDir, err := os.MkdirTemp("", "beads-nogit-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	t.Chdir(tmpDir)
	git.ResetCaches() // Reset after chdir for caching tests

	// findGitRoot should return empty string
	result := findGitRoot()

	if result != "" {
		t.Errorf("findGitRoot() = %q, want empty string (not in git repo)", result)
	}
}

// TestFindBeadsDir_Worktree tests that FindBeadsDir correctly finds the .beads
// directory within a git worktree, respecting the worktree boundary and not
// searching into the main repository. This is critical for bd-745.
func TestFindBeadsDir_Worktree(t *testing.T) {
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

	// Create temporary directory for our test
	tmpDir, err := os.MkdirTemp("", "beads-worktree-finddir-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize main git repository
	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Configure git user
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()

	// Create .beads directory in main repo with a database
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainBeadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Create initial commit
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "-A")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// Create a worktree
	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	// Create .beads directory in worktree with its own database
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeBeadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Change to worktree
	t.Chdir(worktreeDir)
	git.ResetCaches() // Reset after chdir for caching tests

	// FindBeadsDir should prioritize the main repo's .beads for worktrees (bd-de6)
	result := FindBeadsDir()

	// Resolve symlinks for comparison
	resultResolved, _ := filepath.EvalSymlinks(result)
	worktreeBeadsDirResolved, _ := filepath.EvalSymlinks(worktreeBeadsDir)
	mainBeadsDirResolved, _ := filepath.EvalSymlinks(mainBeadsDir)

	if resultResolved != mainBeadsDirResolved {
		t.Errorf("FindBeadsDir() = %q, want main repo .beads %q (prioritized for worktrees)", result, mainBeadsDir)
	}

	// Verify we're NOT finding the worktree's .beads (should fall back only if main repo has no .beads)
	if resultResolved == worktreeBeadsDirResolved {
		t.Errorf("FindBeadsDir() returned worktree .beads %q instead of main repo .beads %q - prioritization not working!", worktreeBeadsDir, mainBeadsDir)
	}
}

// TestFindBeadsDir_WorktreeRedirectOverride tests that when a worktree has its
// own .beads/redirect, it takes priority over the main repo's .beads directory.
// This enables per-worktree topic selection via timvisher_EXP_bd_topics set.
func TestFindBeadsDir_WorktreeRedirectOverride(t *testing.T) {
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

	// Create temporary directory for our test
	tmpDir, err := os.MkdirTemp("", "beads-worktree-redirect-override-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize main git repository with .beads containing project files
	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()

	// Create .beads in main repo with project files (would normally win)
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainBeadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Create initial commit
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "-A")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// Create an external topic .beads directory (the redirect target)
	topicBeadsDir := filepath.Join(tmpDir, "topics", "my-topic", ".beads")
	if err := os.MkdirAll(topicBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(topicBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"beads_my_topic"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a worktree
	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	// Create .beads/redirect in the worktree pointing to the external topic
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Resolve symlinks for the redirect target (macOS /private/var)
	topicBeadsDirResolved, _ := filepath.EvalSymlinks(topicBeadsDir)
	if err := os.WriteFile(filepath.Join(worktreeBeadsDir, "redirect"), []byte(topicBeadsDirResolved+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Change to worktree
	t.Chdir(worktreeDir)
	git.ResetCaches()

	// FindBeadsDir should follow the worktree redirect, NOT use main repo's .beads
	result := FindBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	mainBeadsDirResolved, _ := filepath.EvalSymlinks(mainBeadsDir)

	if resultResolved == mainBeadsDirResolved {
		t.Errorf("FindBeadsDir() = main repo .beads %q, want topic .beads %q (worktree redirect should override)", result, topicBeadsDirResolved)
	}

	if resultResolved != topicBeadsDirResolved {
		t.Errorf("FindBeadsDir() = %q, want topic .beads %q (via worktree redirect)", result, topicBeadsDirResolved)
	}
}

// TestFindDatabasePath_WorktreeRedirectOverride tests that findDatabaseInTree
// follows a worktree's .beads/redirect before falling back to the main repo.
// This is the database-discovery counterpart of TestFindBeadsDir_WorktreeRedirectOverride.
func TestFindDatabasePath_WorktreeRedirectOverride(t *testing.T) {
	originalEnv := os.Getenv("BEADS_DIR")
	defer func() {
		if originalEnv != "" {
			os.Setenv("BEADS_DIR", originalEnv)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()
	os.Unsetenv("BEADS_DIR")

	tmpDir, err := os.MkdirTemp("", "beads-db-worktree-redirect-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize main git repo with a .beads database
	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()

	// Main repo has a dolt database (would normally win)
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	mainDoltDir := filepath.Join(mainBeadsDir, "dolt")
	if err := os.MkdirAll(mainDoltDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "-A")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// Create external topic .beads with its own dolt database
	topicBeadsDir := filepath.Join(tmpDir, "topics", "my-topic", ".beads")
	topicDoltDir := filepath.Join(topicBeadsDir, "dolt")
	if err := os.MkdirAll(topicDoltDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a worktree
	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	// Create .beads/redirect in the worktree pointing to the topic
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	topicBeadsDirResolved, _ := filepath.EvalSymlinks(topicBeadsDir)
	if err := os.WriteFile(filepath.Join(worktreeBeadsDir, "redirect"), []byte(topicBeadsDirResolved+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(worktreeDir)
	git.ResetCaches()

	result := FindDatabasePath()

	mainDoltResolved, _ := filepath.EvalSymlinks(mainDoltDir)
	topicDoltResolved, _ := filepath.EvalSymlinks(topicDoltDir)

	if result == "" {
		t.Fatal("FindDatabasePath() returned empty, want topic dolt path")
	}

	resultResolved, _ := filepath.EvalSymlinks(result)
	if resultResolved == mainDoltResolved {
		t.Errorf("FindDatabasePath() = main repo dolt %q, want topic dolt %q (worktree redirect should override)", result, topicDoltResolved)
	}
	if resultResolved != topicDoltResolved {
		t.Errorf("FindDatabasePath() = %q, want topic dolt %q (via worktree redirect)", result, topicDoltResolved)
	}
}

// TestFindBeadsDir_SiblingWorktree tests that FindBeadsDir does not escape past
// the worktree boundary when the worktree is a sibling of the main repo (not a
// child). This is the regression test for GH#1653.
func TestFindBeadsDir_SiblingWorktree(t *testing.T) {
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

	// Create temporary directory for our test
	tmpDir, err := os.MkdirTemp("", "beads-sibling-worktree-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Resolve symlinks (macOS /var -> /private/var)
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Structure: tmpDir/main-repo  (git repo with .beads/)
	//            tmpDir/sibling-wt (worktree, sibling of main-repo)
	//            tmpDir/.beads/    (UNRELATED beads dir that should NOT be found)

	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()

	// Create .beads in main repo
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainBeadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Initial commit
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "-A")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// Create sibling worktree (NOT a child of main-repo)
	siblingDir := filepath.Join(tmpDir, "sibling-wt")
	cmd = exec.Command("git", "worktree", "add", siblingDir, "HEAD")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", siblingDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	// Create an UNRELATED .beads/ in the parent directory (tmpDir)
	// Before the fix, the walk would escape past worktreeRoot and find this
	unrelatedBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(unrelatedBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unrelatedBeadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Change to the sibling worktree
	t.Chdir(siblingDir)
	git.ResetCaches()

	result := FindBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	mainBeadsDirResolved, _ := filepath.EvalSymlinks(mainBeadsDir)
	unrelatedResolved, _ := filepath.EvalSymlinks(unrelatedBeadsDir)

	// Should find main repo's .beads (via the mainRepoRoot check in step 2)
	if resultResolved != mainBeadsDirResolved {
		t.Errorf("FindBeadsDir() = %q, want main repo .beads %q", result, mainBeadsDir)
	}

	// Must NOT find the unrelated parent .beads
	if resultResolved == unrelatedResolved {
		t.Errorf("FindBeadsDir() escaped worktree boundary and found unrelated %q", unrelatedBeadsDir)
	}
}

// TestFindDatabasePath_Worktree tests that FindDatabasePath correctly finds the
// shared database in the main repository when accessed from a git worktree. This is the
// key test for bd-745 - worktrees should share the same .beads database.
func TestFindDatabasePath_Worktree(t *testing.T) {
	// Save original state
	originalEnvDir := os.Getenv("BEADS_DIR")
	originalEnvDB := os.Getenv("BEADS_DB")
	defer func() {
		if originalEnvDir != "" {
			os.Setenv("BEADS_DIR", originalEnvDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		if originalEnvDB != "" {
			os.Setenv("BEADS_DB", originalEnvDB)
		} else {
			os.Unsetenv("BEADS_DB")
		}
	}()
	os.Unsetenv("BEADS_DIR")
	os.Unsetenv("BEADS_DB")

	// Create temporary directory for our test
	tmpDir, err := os.MkdirTemp("", "beads-worktree-finddb-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize main git repository
	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Configure git user
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()

	// Create .beads directory in main repo with dolt database
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	mainDoltDir := filepath.Join(mainBeadsDir, "dolt")
	if err := os.MkdirAll(mainDoltDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create initial commit
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "-A")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// Create a worktree
	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	// Change to worktree subdirectory
	worktreeSubDir := filepath.Join(worktreeDir, "sub", "nested")
	if err := os.MkdirAll(worktreeSubDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(worktreeSubDir)
	git.ResetCaches() // Reset after chdir for caching tests

	// FindDatabasePath should find the main repo's shared database
	result := FindDatabasePath()

	// Resolve symlinks for comparison
	resultResolved, _ := filepath.EvalSymlinks(result)
	mainDoltResolved, _ := filepath.EvalSymlinks(mainDoltDir)

	if resultResolved != mainDoltResolved {
		t.Errorf("FindDatabasePath() = %q, want main repo shared db %q", result, mainDoltDir)
	}
}

// TestFindDatabasePath_WorktreeNoLocalDB tests that when a worktree does NOT have
// its own .beads directory, FindDatabasePath finds the shared database in the main
// repository. This tests the "shared database" behavior for worktrees.
func TestFindDatabasePath_WorktreeNoLocalDB(t *testing.T) {
	// Save original state
	originalEnvDir := os.Getenv("BEADS_DIR")
	originalEnvDB := os.Getenv("BEADS_DB")
	defer func() {
		if originalEnvDir != "" {
			os.Setenv("BEADS_DIR", originalEnvDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		if originalEnvDB != "" {
			os.Setenv("BEADS_DB", originalEnvDB)
		} else {
			os.Unsetenv("BEADS_DB")
		}
	}()
	os.Unsetenv("BEADS_DIR")
	os.Unsetenv("BEADS_DB")

	// Create temporary directory for our test
	tmpDir, err := os.MkdirTemp("", "beads-worktree-nodb-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize main git repository
	mainRepoDir := filepath.Join(tmpDir, "main-repo")
	if err := os.MkdirAll(mainRepoDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Configure git user
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()

	// Create .beads directory in main repo with dolt database
	mainBeadsDir := filepath.Join(mainRepoDir, ".beads")
	if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	mainDoltDir := filepath.Join(mainBeadsDir, "dolt")
	if err := os.MkdirAll(mainDoltDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create initial commit
	if err := os.WriteFile(filepath.Join(mainRepoDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "-A")
	cmd.Dir = mainRepoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// Create a worktree WITHOUT a .beads directory
	worktreeDir := filepath.Join(tmpDir, "worktree")
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	cmd.Dir = mainRepoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	}()

	// Note: We do NOT create .beads in the worktree
	// The worktree got .beads from the commit, so we need to remove it
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.RemoveAll(worktreeBeadsDir); err != nil {
		// May not exist, that's fine
	}

	// Change to worktree
	t.Chdir(worktreeDir)
	git.ResetCaches() // Reset after chdir for caching tests

	// FindDatabasePath should find the main repo's shared database
	result := FindDatabasePath()

	// Resolve symlinks for comparison
	resultResolved, _ := filepath.EvalSymlinks(result)
	mainDoltResolved, _ := filepath.EvalSymlinks(mainDoltDir)

	if resultResolved != mainDoltResolved {
		t.Errorf("FindDatabasePath() = %q, want main repo shared db %q", result, mainDoltDir)
	}
}
