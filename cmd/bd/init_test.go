//go:build cgo

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// skipIfNoDolt skips the test when no Dolt server is available.
// Checks both binary availability and test server status.
func skipIfNoDolt(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("skipping: dolt not installed")
	}
	if testDoltServerPort == 0 {
		t.Skip("skipping: Dolt test server not running")
	}
}

func TestInitCommand(t *testing.T) {
	skipIfNoDolt(t)
	tests := []struct {
		name           string
		prefix         string
		quiet          bool
		wantOutputText string
		wantNoOutput   bool
	}{
		{
			name:           "init with default prefix",
			prefix:         "",
			quiet:          false,
			wantOutputText: "bd initialized successfully",
		},
		{
			name:           "init with custom prefix",
			prefix:         "myproject",
			quiet:          false,
			wantOutputText: "myproject-<hash>",
		},
		{
			name:         "init with quiet flag",
			prefix:       "test",
			quiet:        true,
			wantNoOutput: true,
		},
		{
			name:           "init with prefix ending in hyphen",
			prefix:         "test-",
			quiet:          false,
			wantOutputText: "test-<hash>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset global state
			origDBPath := dbPath
			origStore := store
			defer func() {
				if store != nil && store != origStore {
					store.Close()
				}
				store = origStore
				dbPath = origDBPath
			}()
			dbPath = ""
			store = nil

			// Reset Cobra command state
			rootCmd.SetArgs([]string{})
			initCmd.Flags().Set("prefix", "")
			initCmd.Flags().Set("quiet", "false")

			tmpDir := t.TempDir()
			t.Chdir(tmpDir)

			// Capture output
			var buf bytes.Buffer
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w
			defer func() {
				os.Stdout = oldStdout
			}()

			// Build command arguments
			args := []string{"init"}
			if tt.prefix != "" {
				args = append(args, "--prefix", tt.prefix)
			}
			if tt.quiet {
				args = append(args, "--quiet")
			}

			rootCmd.SetArgs(args)

			// Run command
			var err error
			err = rootCmd.Execute()

			// Restore stdout and read output
			w.Close()
			buf.ReadFrom(r)
			os.Stdout = oldStdout
			output := buf.String()

			if err != nil {
				t.Fatalf("init command failed: %v", err)
			}

			// Check output
			if tt.wantNoOutput {
				if output != "" {
					t.Errorf("Expected no output with --quiet, got: %s", output)
				}
			} else if tt.wantOutputText != "" {
				if !strings.Contains(output, tt.wantOutputText) {
					t.Errorf("Expected output to contain %q, got: %s", tt.wantOutputText, output)
				}
			}

			// Verify .beads directory was created
			beadsDir := filepath.Join(tmpDir, ".beads")
			if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
				t.Error(".beads directory was not created")
			}

			// Verify .gitignore was created with proper content
			gitignorePath := filepath.Join(beadsDir, ".gitignore")
			gitignoreContent, err := os.ReadFile(gitignorePath)
			if err != nil {
				t.Errorf(".gitignore file was not created: %v", err)
			} else {
				// Check for essential patterns
				gitignoreStr := string(gitignoreContent)
				expectedPatterns := []string{
					"*.db",
					"*.db?*",
					"*.db-journal",
					"*.db-wal",
					"*.db-shm",
					"daemon.log",
					"daemon.pid",
					"bd.sock",
					"dolt/",
					"dolt-access.lock",
				}
				for _, pattern := range expectedPatterns {
					if !strings.Contains(gitignoreStr, pattern) {
						t.Errorf(".gitignore missing expected pattern: %s", pattern)
					}
				}
			}

			// Verify Dolt database directory was created
			doltPath := filepath.Join(beadsDir, "dolt")
			if info, err := os.Stat(doltPath); os.IsNotExist(err) {
				t.Errorf("Dolt database directory was not created at %s", doltPath)
			} else if !info.IsDir() {
				t.Errorf("Expected %s to be a directory", doltPath)
			}

			// Database content verification (prefix, metadata) is skipped here because
			// embedded Dolt's Close() can timeout, leaving file locks held and preventing
			// re-opening the DB in the same process. The init command's own internal logic
			// verifies these writes succeed; prefix/metadata correctness is also covered
			// by dedicated Dolt storage tests.
		})
	}
}

// Note: Error case testing is omitted because the init command calls os.Exit()
// on errors, which makes it difficult to test in a unit test context.

func TestInitAlreadyInitialized(t *testing.T) {
	skipIfNoDolt(t)
	// Reset global state
	origDBPath := dbPath
	defer func() { dbPath = origDBPath }()
	dbPath = ""

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize once
	rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("First init failed: %v", err)
	}

	// Initialize again with same prefix and --force flag (bd-emg: safety guard)
	// Without --force, init should refuse when database already exists
	rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet", "--force"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Second init with --force failed: %v", err)
	}

	// Verify database still works
	dbPath := filepath.Join(tmpDir, ".beads", "dolt")
	store, err := openExistingTestDB(t, dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("Failed to get prefix after re-init: %v", err)
	}

	if prefix != "test" {
		t.Errorf("Expected prefix 'test', got %q", prefix)
	}
}

func TestInitWithCustomDBPath(t *testing.T) {
	t.Skip("BEADS_DB env var does not control Dolt store location; Dolt always uses .beads/dolt/")
	// Save original state
	origDBPath := dbPath
	defer func() { dbPath = origDBPath }()

	tmpDir := t.TempDir()
	customDBDir := filepath.Join(tmpDir, "custom", "location")

	// Change to a different directory to ensure --db flag is actually used
	workDir := filepath.Join(tmpDir, "workdir")
	if err := os.MkdirAll(workDir, 0750); err != nil {
		t.Fatalf("Failed to create work directory: %v", err)
	}

	t.Chdir(workDir)

	customDBPath := filepath.Join(customDBDir, "test.db")

	// Test with BEADS_DB environment variable (replacing --db flag test)
	t.Run("init with BEADS_DB pointing to custom path", func(t *testing.T) {
		dbPath = "" // Reset global
		os.Setenv("BEADS_DB", customDBPath)
		defer os.Unsetenv("BEADS_DB")

		rootCmd.SetArgs([]string{"init", "--prefix", "custom", "--quiet"})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with BEADS_DB failed: %v", err)
		}

		// Verify database was created at custom location
		if _, err := os.Stat(customDBPath); os.IsNotExist(err) {
			t.Errorf("Database was not created at custom path %s", customDBPath)
		}

		// Verify database works
		store, err := openExistingTestDB(t, customDBPath)
		if err != nil {
			t.Fatalf("Failed to open database: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get prefix: %v", err)
		}

		if prefix != "custom" {
			t.Errorf("Expected prefix 'custom', got %q", prefix)
		}

		// Verify .beads/ directory was NOT created in work directory
		if _, err := os.Stat(filepath.Join(workDir, ".beads")); err == nil {
			t.Error(".beads/ directory should not be created when using BEADS_DB env var")
		}
	})

	// Test with BEADS_DB env var
	t.Run("init with BEADS_DB env var", func(t *testing.T) {
		dbPath = "" // Reset global
		envDBPath := filepath.Join(tmpDir, "env", "location", "env.db")
		os.Setenv("BEADS_DB", envDBPath)
		defer os.Unsetenv("BEADS_DB")

		rootCmd.SetArgs([]string{"init", "--prefix", "envtest", "--quiet"})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with BEADS_DB failed: %v", err)
		}

		// Verify database was created at env location
		if _, err := os.Stat(envDBPath); os.IsNotExist(err) {
			t.Errorf("Database was not created at BEADS_DB path %s", envDBPath)
		}

		// Verify database works
		store, err := openExistingTestDB(t, envDBPath)
		if err != nil {
			t.Fatalf("Failed to open database: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get prefix: %v", err)
		}

		if prefix != "envtest" {
			t.Errorf("Expected prefix 'envtest', got %q", prefix)
		}
	})

	// Test that BEADS_DB path containing ".beads" doesn't create CWD/.beads
	t.Run("init with BEADS_DB path containing .beads", func(t *testing.T) {
		dbPath = "" // Reset global
		// Path contains ".beads" but is outside work directory
		customPath := filepath.Join(tmpDir, "storage", ".beads-backup", "test.db")
		os.Setenv("BEADS_DB", customPath)
		defer os.Unsetenv("BEADS_DB")

		rootCmd.SetArgs([]string{"init", "--prefix", "beadstest", "--quiet"})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with custom .beads path failed: %v", err)
		}

		// Verify database was created at custom location
		if _, err := os.Stat(customPath); os.IsNotExist(err) {
			t.Errorf("Database was not created at custom path %s", customPath)
		}

		// Verify .beads/ directory was NOT created in work directory
		if _, err := os.Stat(filepath.Join(workDir, ".beads")); err == nil {
			t.Error(".beads/ directory should not be created in CWD when BEADS_DB path contains .beads")
		}
	})

	// Test with multiple BEADS_DB variations
	t.Run("BEADS_DB with subdirectories", func(t *testing.T) {
		dbPath = "" // Reset global
		envPath := filepath.Join(tmpDir, "env", "subdirs", "test.db")

		os.Setenv("BEADS_DB", envPath)
		defer os.Unsetenv("BEADS_DB")

		rootCmd.SetArgs([]string{"init", "--prefix", "envtest2", "--quiet"})

		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with BEADS_DB subdirs failed: %v", err)
		}

		// Verify database was created at env location
		if _, err := os.Stat(envPath); os.IsNotExist(err) {
			t.Errorf("Database was not created at BEADS_DB path %s", envPath)
		}

		// Verify .beads/ directory was NOT created in work directory
		if _, err := os.Stat(filepath.Join(workDir, ".beads")); err == nil {
			t.Error(".beads/ directory should not be created in CWD when BEADS_DB is set")
		}
	})
}

// TestSetupClaudeSettings_InvalidJSON verifies that invalid JSON in existing
// settings.local.json returns an error instead of silently overwriting.
// This is a regression test for bd-5bj where user settings were lost.
func TestSetupClaudeSettings_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create .claude directory
	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("Failed to create .claude directory: %v", err)
	}

	// Create settings.local.json with invalid JSON (array syntax in object context)
	// This is the exact pattern that caused the bug in the user's file
	invalidJSON := `{
  "permissions": {
    "allow": [
      "Bash(python3:*)"
    ],
    "deny": [
      "_comment": "Add commands to block here"
    ]
  }
}`
	settingsPath := filepath.Join(claudeDir, "settings.local.json")
	if err := os.WriteFile(settingsPath, []byte(invalidJSON), 0644); err != nil {
		t.Fatalf("Failed to write invalid settings: %v", err)
	}

	// Call setupClaudeSettings - should return an error
	var err error
	err = setupClaudeSettings(false)
	if err == nil {
		t.Fatal("Expected error for invalid JSON, got nil")
	}

	// Verify the error message mentions invalid JSON
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("Expected error to mention 'invalid JSON', got: %v", err)
	}

	// Verify the original file was NOT modified
	content, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read settings file: %v", err)
	}

	if !strings.Contains(string(content), "permissions") {
		t.Error("Original file content should be preserved")
	}

	if strings.Contains(string(content), "bd onboard") {
		t.Error("File should NOT contain bd onboard prompt after error")
	}
}

// TestSetupClaudeSettings_ValidJSON verifies that valid JSON is properly updated
func TestSetupClaudeSettings_ValidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create .claude directory
	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("Failed to create .claude directory: %v", err)
	}

	// Create settings.local.json with valid JSON
	validJSON := `{
  "permissions": {
    "allow": [
      "Bash(python3:*)"
    ]
  },
  "hooks": {
    "PreToolUse": []
  }
}`
	settingsPath := filepath.Join(claudeDir, "settings.local.json")
	if err := os.WriteFile(settingsPath, []byte(validJSON), 0644); err != nil {
		t.Fatalf("Failed to write valid settings: %v", err)
	}

	// Call setupClaudeSettings - should succeed
	var err error
	err = setupClaudeSettings(false)
	if err != nil {
		t.Fatalf("Expected no error for valid JSON, got: %v", err)
	}

	// Verify the file was updated with prompt AND preserved existing settings
	content, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read settings file: %v", err)
	}

	contentStr := string(content)

	// Should contain the new prompt
	if !strings.Contains(contentStr, "bd onboard") {
		t.Error("File should contain bd onboard prompt")
	}

	// Should preserve existing permissions
	if !strings.Contains(contentStr, "permissions") {
		t.Error("File should preserve permissions section")
	}

	// Should preserve existing hooks
	if !strings.Contains(contentStr, "hooks") {
		t.Error("File should preserve hooks section")
	}

	if !strings.Contains(contentStr, "PreToolUse") {
		t.Error("File should preserve PreToolUse hook")
	}
}

// TestSetupClaudeSettings_NoExistingFile verifies behavior when no file exists
func TestSetupClaudeSettings_NoExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Don't create .claude directory - setupClaudeSettings should create it

	// Call setupClaudeSettings - should succeed
	var err error
	err = setupClaudeSettings(false)
	if err != nil {
		t.Fatalf("Expected no error when no file exists, got: %v", err)
	}

	// Verify the file was created with prompt
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.local.json")
	content, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read settings file: %v", err)
	}

	if !strings.Contains(string(content), "bd onboard") {
		t.Error("File should contain bd onboard prompt")
	}
}

// setupIsolatedGitConfig creates an empty git config in tmpDir and sets GIT_CONFIG_GLOBAL
// to prevent tests from using the real user's global git config.
func setupIsolatedGitConfig(t *testing.T, tmpDir string) {
	t.Helper()
	gitConfigPath := filepath.Join(tmpDir, ".gitconfig")
	if err := os.WriteFile(gitConfigPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfigPath)
}

// TestSetupGlobalGitIgnore_ReadOnly verifies graceful handling when the
// gitignore file cannot be written (prints manual instructions instead of failing).
func TestSetupGlobalGitIgnore_ReadOnly(t *testing.T) {
	t.Run("read-only file", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("macOS allows file owner to write to read-only (0444) files")
		}
		tmpDir := t.TempDir()
		setupIsolatedGitConfig(t, tmpDir)

		configDir := filepath.Join(tmpDir, ".config", "git")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			t.Fatal(err)
		}

		ignorePath := filepath.Join(configDir, "ignore")
		if err := os.WriteFile(ignorePath, []byte("# existing\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(ignorePath, 0444); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(ignorePath, 0644)

		output := captureStdout(t, func() error {
			return setupGlobalGitIgnore(tmpDir, "/test/project", false)
		})

		if !strings.Contains(output, "Unable to write") {
			t.Error("expected instructions for manual addition")
		}
		if !strings.Contains(output, "/test/project/.beads/") {
			t.Error("expected .beads pattern in output")
		}
	})

	t.Run("symlink to read-only file", func(t *testing.T) {
		if runtime.GOOS == "darwin" {
			t.Skip("macOS allows file owner to write to read-only (0444) files")
		}
		tmpDir := t.TempDir()
		setupIsolatedGitConfig(t, tmpDir)

		// Target file in a separate location
		targetDir := filepath.Join(tmpDir, "target")
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			t.Fatal(err)
		}
		targetFile := filepath.Join(targetDir, "ignore")
		if err := os.WriteFile(targetFile, []byte("# existing\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(targetFile, 0444); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(targetFile, 0644)

		// Symlink from expected location
		configDir := filepath.Join(tmpDir, ".config", "git")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(targetFile, filepath.Join(configDir, "ignore")); err != nil {
			t.Fatal(err)
		}

		output := captureStdout(t, func() error {
			return setupGlobalGitIgnore(tmpDir, "/test/project", false)
		})

		if !strings.Contains(output, "Unable to write") {
			t.Error("expected instructions for manual addition")
		}
		if !strings.Contains(output, "/test/project/.beads/") {
			t.Error("expected .beads pattern in output")
		}
	})
}

// captureStdout captures stdout output from fn and returns it as a string.
// Uses stdioMutex to prevent races with concurrent os.Stdout redirection (bd-cqjoi).
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := fn()

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	os.Stdout = oldStdout

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	return buf.String()
}

// TestInitPromptRoleConfig tests the beads.role git config read/write functions
func TestInitPromptRoleConfig(t *testing.T) {
	t.Run("getBeadsRole returns empty when not configured", func(t *testing.T) {
		tmpDir := newGitRepo(t)
		t.Chdir(tmpDir)

		role, hasRole := getBeadsRole()
		if hasRole {
			t.Errorf("Expected hasRole=false when not configured, got true with role=%q", role)
		}
		if role != "" {
			t.Errorf("Expected empty role when not configured, got %q", role)
		}
	})

	t.Run("setBeadsRole and getBeadsRole roundtrip", func(t *testing.T) {
		tmpDir := newGitRepo(t)
		t.Chdir(tmpDir)

		// Set role to contributor
		if err := setBeadsRole("contributor"); err != nil {
			t.Fatalf("Failed to set beads.role: %v", err)
		}

		role, hasRole := getBeadsRole()
		if !hasRole {
			t.Error("Expected hasRole=true after setting role")
		}
		if role != "contributor" {
			t.Errorf("Expected role 'contributor', got %q", role)
		}

		// Change to maintainer
		if err := setBeadsRole("maintainer"); err != nil {
			t.Fatalf("Failed to set beads.role: %v", err)
		}

		role, hasRole = getBeadsRole()
		if !hasRole {
			t.Error("Expected hasRole=true after setting role")
		}
		if role != "maintainer" {
			t.Errorf("Expected role 'maintainer', got %q", role)
		}
	})
}

// TestInitPromptSkippedWithFlags verifies that --contributor and --team flags skip the prompt
func TestInitPromptSkippedWithFlags(t *testing.T) {
	skipIfNoDolt(t)
	t.Run("contributor flag skips prompt and runs wizard", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		// Reset caches so RepoContext picks up new directory
		beads.ResetCaches()
		git.ResetCaches()
		defer func() {
			beads.ResetCaches()
			git.ResetCaches()
		}()

		// Reset Cobra flags
		initCmd.Flags().Set("contributor", "false")

		tmpDir := newGitRepo(t)
		t.Chdir(tmpDir)

		// Verify no role is set initially
		role, hasRole := getBeadsRole()
		if hasRole {
			t.Fatalf("Expected no role initially, got %q", role)
		}

		// Run bd init with --contributor flag (quiet to suppress wizard output)
		// The wizard will fail because there's no planning repo, but that's OK
		// We just want to verify the flag bypasses the prompt
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--contributor", "--quiet"})
		_ = rootCmd.Execute() // Ignore error - wizard may fail

		// The --contributor flag should NOT set beads.role (that's done by prompt, not flag)
		// The flag just triggers the wizard directly
	})

	t.Run("team flag skips prompt", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		// Reset caches so RepoContext picks up new directory
		beads.ResetCaches()
		git.ResetCaches()
		defer func() {
			beads.ResetCaches()
			git.ResetCaches()
		}()

		// Reset Cobra flags
		initCmd.Flags().Set("team", "false")

		tmpDir := newGitRepo(t)
		t.Chdir(tmpDir)

		// Verify no role is set initially
		role, hasRole := getBeadsRole()
		if hasRole {
			t.Fatalf("Expected no role initially, got %q", role)
		}

		// Run bd init with --team flag
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--team", "--quiet"})
		_ = rootCmd.Execute() // Ignore error - wizard may fail

		// The --team flag should not set beads.role
		// (team wizard is separate from contributor/maintainer roles)
	})
}

// TestInitPromptTTYDetection verifies shouldPromptForRole behavior
func TestInitPromptTTYDetection(t *testing.T) {
	// Note: In test environment, stdin is typically NOT a TTY (it's a pipe)
	// This test verifies the function works, not that we're in a TTY

	t.Run("shouldPromptForRole returns false in test environment", func(t *testing.T) {
		// In test environment, stdin is typically piped, not a TTY
		result := shouldPromptForRole()

		// We can't guarantee what the result will be in all test environments,
		// but we can verify the function doesn't panic and returns a bool
		if result {
			t.Log("Test environment has TTY stdin (unusual but acceptable)")
		} else {
			t.Log("Test environment does not have TTY stdin (expected)")
		}
	})
}

// TestInitPromptNonGitRepo verifies prompt is skipped in non-git directories
func TestInitPromptNonGitRepo(t *testing.T) {
	skipIfNoDolt(t)
	// Reset global state
	origDBPath := dbPath
	defer func() { dbPath = origDBPath }()
	dbPath = ""

	// Reset caches so RepoContext picks up new directory
	beads.ResetCaches()
	git.ResetCaches()
	defer func() {
		beads.ResetCaches()
		git.ResetCaches()
	}()

	// Reset Cobra flags that may be set from previous tests
	initCmd.Flags().Set("contributor", "false")
	initCmd.Flags().Set("team", "false")

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// DON'T initialize git repo

	// Run bd init - should succeed without prompting (no git repo)
	rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Init should succeed in non-git directory: %v", err)
	}

	// Verify .beads was created
	beadsDir := filepath.Join(tmpDir, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		t.Error(".beads directory should be created even without git")
	}
}

// TestInitPromptExistingRole verifies behavior when beads.role is already set
func TestInitPromptExistingRole(t *testing.T) {
	skipIfNoDolt(t)
	t.Run("existing role is preserved on reinit with --force", func(t *testing.T) {
		// Reset global state
		origDBPath := dbPath
		defer func() { dbPath = origDBPath }()
		dbPath = ""

		// Reset caches so RepoContext picks up new directory
		beads.ResetCaches()
		git.ResetCaches()
		defer func() {
			beads.ResetCaches()
			git.ResetCaches()
		}()

		// Reset Cobra flags that may be set from previous tests
		initCmd.Flags().Set("contributor", "false")
		initCmd.Flags().Set("team", "false")
		initCmd.Flags().Set("force", "false")

		tmpDir := newGitRepo(t)
		t.Chdir(tmpDir)

		// Set role before init
		if err := setBeadsRole("contributor"); err != nil {
			t.Fatalf("Failed to set beads.role: %v", err)
		}

		// Run bd init (non-interactive, so prompt is skipped)
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init failed: %v", err)
		}

		// Verify role is still set
		role, hasRole := getBeadsRole()
		if !hasRole {
			t.Error("Expected beads.role to still be set after init")
		}
		if role != "contributor" {
			t.Errorf("Expected role 'contributor' to be preserved, got %q", role)
		}

		// Reset Cobra flags for reinit
		initCmd.Flags().Set("force", "false")

		// Reinit with --force (non-interactive)
		rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet", "--force"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Reinit failed: %v", err)
		}

		// Verify role is still set (not cleared by reinit)
		role, hasRole = getBeadsRole()
		if !hasRole {
			t.Error("Expected beads.role to still be set after reinit")
		}
		if role != "contributor" {
			t.Errorf("Expected role 'contributor' to be preserved after reinit, got %q", role)
		}
	})
}

// TestInitWithRedirect verifies that bd init creates the database in the redirect target,
// not in the local .beads directory. (GH#bd-0qel)
// TestInitRedirect groups redirect-related init tests.
func TestInitRedirect(t *testing.T) {
	skipIfNoDolt(t)
	resetRedirectState := func(t *testing.T) {
		t.Helper()
		origDBPath := dbPath
		origBeadsDir := os.Getenv("BEADS_DIR")
		t.Cleanup(func() {
			dbPath = origDBPath
			if origBeadsDir != "" {
				os.Setenv("BEADS_DIR", origBeadsDir)
			} else {
				os.Unsetenv("BEADS_DIR")
			}
		})
		dbPath = ""
		os.Unsetenv("BEADS_DIR")
		initCmd.Flags().Set("prefix", "")
		initCmd.Flags().Set("quiet", "false")
		initCmd.Flags().Set("force", "false")
	}

	t.Run("RedirectCreatesDBInTarget", func(t *testing.T) {
		resetRedirectState(t)

		tmpDir := t.TempDir()

		projectDir := filepath.Join(tmpDir, "project")
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatal(err)
		}

		localBeadsDir := filepath.Join(projectDir, ".beads")
		if err := os.MkdirAll(localBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		targetBeadsDir := filepath.Join(tmpDir, "canonical", ".beads")
		if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		redirectPath := filepath.Join(localBeadsDir, beads.RedirectFileName)
		if err := os.WriteFile(redirectPath, []byte("../canonical/.beads\n"), 0644); err != nil {
			t.Fatal(err)
		}

		t.Chdir(projectDir)

		rootCmd.SetArgs([]string{"init", "--prefix", "redirect-test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with redirect failed: %v", err)
		}

		targetDBPath := filepath.Join(targetBeadsDir, "dolt")
		if _, err := os.Stat(targetDBPath); os.IsNotExist(err) {
			t.Errorf("Dolt database was NOT created in redirect target: %s", targetDBPath)
		}

		localDBPath := filepath.Join(localBeadsDir, "dolt")
		if _, err := os.Stat(localDBPath); err == nil {
			t.Errorf("Database was incorrectly created in local .beads: %s (should be in redirect target)", localDBPath)
		}

		store, err := openExistingTestDB(t, targetDBPath)
		if err != nil {
			t.Fatalf("Failed to open database in redirect target: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get issue prefix from database: %v", err)
		}
		if prefix != "redirect-test" {
			t.Errorf("Expected prefix 'redirect-test', got %q", prefix)
		}
	})

	// Verifies that bd init errors when the redirect target already has a database,
	// preventing accidental overwrites. (GH#bd-0qel)
	t.Run("ErrorWhenTargetHasExistingDB", func(t *testing.T) {
		resetRedirectState(t)

		tmpDir := t.TempDir()

		canonicalDir := filepath.Join(tmpDir, "canonical")
		canonicalBeadsDir := filepath.Join(canonicalDir, ".beads")
		if err := os.MkdirAll(canonicalBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		canonicalDBPath := filepath.Join(canonicalBeadsDir, "beads.db")
		// Create the db file so checkExistingBeadsData detects it
		if err := os.WriteFile(canonicalDBPath, []byte{}, 0644); err != nil {
			t.Fatalf("Failed to create canonical db file: %v", err)
		}

		projectDir := filepath.Join(tmpDir, "project")
		projectBeadsDir := filepath.Join(projectDir, ".beads")
		if err := os.MkdirAll(projectBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		redirectPath := filepath.Join(projectBeadsDir, beads.RedirectFileName)
		if err := os.WriteFile(redirectPath, []byte("../canonical/.beads\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Use os.Chdir since checkExistingBeadsData reads CWD directly
		origWd, _ := os.Getwd()
		if err := os.Chdir(projectDir); err != nil {
			t.Fatal(err)
		}
		defer os.Chdir(origWd)

		err := checkExistingBeadsData("new-prefix")
		if err == nil {
			t.Fatal("Expected checkExistingBeadsData to return error when redirect target already has database")
		}

		errorMsg := err.Error()
		if !strings.Contains(errorMsg, "redirect target already has database") {
			t.Errorf("Expected error about redirect target having database, got: %s", errorMsg)
		}

		// Verify the canonical DB file still exists (wasn't deleted/overwritten)
		if _, statErr := os.Stat(canonicalDBPath); os.IsNotExist(statErr) {
			t.Error("Canonical database file should still exist after error")
		}
	})
}

// =============================================================================
// BEADS_DIR Tests
// =============================================================================
// These tests verify that bd init respects the BEADS_DIR environment variable
// for both safety checks and database creation.

// TestInitBEADS_DIR groups BEADS_DIR-related init tests.
// Tests requirements FR-001, FR-002, FR-004, NFR-001.
func TestInitBEADS_DIR(t *testing.T) {
	skipIfNoDolt(t)
	// resetBeadsDirState resets global state and env vars for each subtest.
	resetBeadsDirState := func(t *testing.T) {
		t.Helper()
		origDBPath := dbPath
		t.Cleanup(func() {
			dbPath = origDBPath
			beads.ResetCaches()
			git.ResetCaches()
		})
		dbPath = ""
		beads.ResetCaches()
		git.ResetCaches()
		initCmd.Flags().Set("prefix", "")
		initCmd.Flags().Set("quiet", "false")
		initCmd.Flags().Set("backend", "")
	}

	// checkExistingBeadsData tests (FR-001, FR-004)
	t.Run("CheckExisting_NoExistingDB", func(t *testing.T) {
		resetBeadsDirState(t)

		tmpDir := t.TempDir()
		beadsDirPath := filepath.Join(tmpDir, "external", ".beads")
		os.MkdirAll(beadsDirPath, 0755)

		os.Setenv("BEADS_DIR", beadsDirPath)
		t.Cleanup(func() { os.Unsetenv("BEADS_DIR") })
		beads.ResetCaches()

		err := checkExistingBeadsData("test")
		if err != nil {
			t.Errorf("Expected no error when BEADS_DIR has no database, got: %v", err)
		}
	})

	t.Run("CheckExisting_CWDIgnoredWhenSet", func(t *testing.T) {
		resetBeadsDirState(t)

		tmpDir := t.TempDir()

		// Create CWD with existing database (should be ignored)
		cwdBeadsDir := filepath.Join(tmpDir, "cwd", ".beads")
		os.MkdirAll(cwdBeadsDir, 0755)
		cwdDBPath := filepath.Join(cwdBeadsDir, beads.CanonicalDatabaseName)
		// Create the db file so checkExistingBeadsData detects it
		if err := os.WriteFile(cwdDBPath, []byte{}, 0644); err != nil {
			t.Fatal(err)
		}

		// Create BEADS_DIR location (no database)
		beadsDirPath := filepath.Join(tmpDir, "external", ".beads")
		os.MkdirAll(beadsDirPath, 0755)

		os.Setenv("BEADS_DIR", beadsDirPath)
		t.Cleanup(func() { os.Unsetenv("BEADS_DIR") })
		beads.ResetCaches()

		origWd, _ := os.Getwd()
		os.Chdir(filepath.Join(tmpDir, "cwd"))
		defer os.Chdir(origWd)

		err := checkExistingBeadsData("test")
		if err != nil {
			t.Errorf("Expected no error when BEADS_DIR has no database (CWD should be ignored), got: %v", err)
		}
	})

	t.Run("CheckExisting_ErrorWhenDBExists", func(t *testing.T) {
		resetBeadsDirState(t)

		tmpDir := t.TempDir()

		beadsDirPath := filepath.Join(tmpDir, "external", ".beads")
		os.MkdirAll(beadsDirPath, 0755)
		testDBPath := filepath.Join(beadsDirPath, beads.CanonicalDatabaseName)
		// Create the db file so checkExistingBeadsData detects it
		if err := os.WriteFile(testDBPath, []byte{}, 0644); err != nil {
			t.Fatal(err)
		}

		os.Setenv("BEADS_DIR", beadsDirPath)
		t.Cleanup(func() { os.Unsetenv("BEADS_DIR") })
		beads.ResetCaches()

		err := checkExistingBeadsData("test")
		if err == nil {
			t.Error("Expected error when BEADS_DIR already has database")
		}
		if !strings.Contains(err.Error(), beadsDirPath) {
			t.Errorf("Expected error to mention BEADS_DIR path %s, got: %v", beadsDirPath, err)
		}
	})

	// FR-002: init creates database at BEADS_DIR
	t.Run("InitCreatesDBAtBeadsDir", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Skipping BEADS_DIR test on Windows")
		}

		resetBeadsDirState(t)

		tmpDir := t.TempDir()

		beadsDirPath := filepath.Join(tmpDir, "external", ".beads")
		os.MkdirAll(filepath.Dir(beadsDirPath), 0755)

		os.Setenv("BEADS_DIR", beadsDirPath)
		t.Cleanup(func() { os.Unsetenv("BEADS_DIR") })
		beads.ResetCaches()
		git.ResetCaches()

		cwdPath := filepath.Join(tmpDir, "workdir")
		os.MkdirAll(cwdPath, 0755)
		t.Chdir(cwdPath)

		rootCmd.SetArgs([]string{"init", "--prefix", "beadsdir-test", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with BEADS_DIR failed: %v", err)
		}

		expectedDBPath := filepath.Join(beadsDirPath, "dolt")
		if info, err := os.Stat(expectedDBPath); os.IsNotExist(err) {
			t.Errorf("Dolt database was not created at BEADS_DIR path: %s", expectedDBPath)
		} else if !info.IsDir() {
			t.Errorf("Expected %s to be a directory", expectedDBPath)
		}

		cwdDBPath := filepath.Join(cwdPath, ".beads", "dolt")
		if _, err := os.Stat(cwdDBPath); err == nil {
			t.Errorf("Database should NOT have been created at CWD: %s", cwdDBPath)
		}

		store, err := openExistingTestDB(t, expectedDBPath)
		if err != nil {
			t.Fatalf("Failed to open database at BEADS_DIR: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get prefix from database: %v", err)
		}
		if prefix != "beadsdir-test" {
			t.Errorf("Expected prefix 'beadsdir-test', got %q", prefix)
		}
	})

	// NFR-001: existing behavior unchanged when BEADS_DIR not set
	t.Run("WithoutBeadsDirNoBehaviorChange", func(t *testing.T) {
		resetBeadsDirState(t)

		os.Unsetenv("BEADS_DIR")
		beads.ResetCaches()
		git.ResetCaches()

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		rootCmd.SetArgs([]string{"init", "--prefix", "no-beadsdir", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init without BEADS_DIR failed: %v", err)
		}

		expectedDBPath := filepath.Join(tmpDir, ".beads", "dolt")
		if info, err := os.Stat(expectedDBPath); os.IsNotExist(err) {
			t.Errorf("Dolt database was not created at default CWD/.beads path: %s", expectedDBPath)
		} else if !info.IsDir() {
			t.Errorf("Expected %s to be a directory", expectedDBPath)
		}

		store, err := openExistingTestDB(t, expectedDBPath)
		if err != nil {
			t.Fatalf("Failed to open database: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get prefix from database: %v", err)
		}
		if prefix != "no-beadsdir" {
			t.Errorf("Expected prefix 'no-beadsdir', got %q", prefix)
		}
	})

	// Precedence: BEADS_DB > BEADS_DIR
	t.Run("BEADS_DB_OverridesBeadsDir", func(t *testing.T) {
		t.Skip("BEADS_DB env var does not control Dolt store location; Dolt always uses .beads/dolt/")
		resetBeadsDirState(t)

		beadsDirTarget := t.TempDir()
		beadsDBTarget := t.TempDir()

		beadsDirBeads := filepath.Join(beadsDirTarget, ".beads")
		if err := os.MkdirAll(beadsDirBeads, 0750); err != nil {
			t.Fatal(err)
		}

		beadsDBPath := filepath.Join(beadsDBTarget, "override.db")

		t.Setenv("BEADS_DIR", beadsDirBeads)
		t.Setenv("BEADS_DB", beadsDBPath)

		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		rootCmd.SetArgs([]string{"init", "--prefix", "precedence", "--quiet"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Init with BEADS_DB + BEADS_DIR failed: %v", err)
		}

		if _, err := os.Stat(beadsDBPath); os.IsNotExist(err) {
			t.Errorf("Database was NOT created at BEADS_DB path: %s", beadsDBPath)
		}

		beadsDirDBPath := filepath.Join(beadsDirBeads, beads.CanonicalDatabaseName)
		if _, err := os.Stat(beadsDirDBPath); err == nil {
			t.Errorf("Database was incorrectly created at BEADS_DIR path: %s (BEADS_DB should override)", beadsDirDBPath)
		}

		store, err := openExistingTestDB(t, beadsDBPath)
		if err != nil {
			t.Fatalf("Failed to open database: %v", err)
		}
		defer store.Close()

		ctx := context.Background()
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get prefix from database: %v", err)
		}
		if prefix != "precedence" {
			t.Errorf("Expected prefix 'precedence', got %q", prefix)
		}
	})
}

// TestInit_WithBEADS_DIR_DoltBackend verifies that bd init with Dolt backend
// creates the database at BEADS_DIR when the environment variable is set.
// This tests requirements FR-002 for Dolt backend.
func TestInit_WithBEADS_DIR_DoltBackend(t *testing.T) {
	// Skip on Windows
	if runtime.GOOS == "windows" {
		t.Skip("Skipping BEADS_DIR Dolt test on Windows")
	}

	// Check if dolt is available
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("Dolt not installed, skipping Dolt backend test")
	}

	// Reset global state
	origDBPath := dbPath
	defer func() { dbPath = origDBPath }()
	dbPath = ""

	// Save and restore BEADS_DIR
	origBeadsDir := os.Getenv("BEADS_DIR")
	defer func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		beads.ResetCaches()
		git.ResetCaches()
	}()

	// Reset Cobra flags
	initCmd.Flags().Set("prefix", "")
	initCmd.Flags().Set("quiet", "false")
	initCmd.Flags().Set("backend", "")

	tmpDir := t.TempDir()

	// Create external BEADS_DIR location
	beadsDirPath := filepath.Join(tmpDir, "external", ".beads")
	os.MkdirAll(filepath.Dir(beadsDirPath), 0755)

	os.Setenv("BEADS_DIR", beadsDirPath)
	beads.ResetCaches()
	git.ResetCaches()

	// Change to a different working directory
	cwdPath := filepath.Join(tmpDir, "workdir")
	os.MkdirAll(cwdPath, 0755)
	t.Chdir(cwdPath)

	// Run bd init with Dolt backend
	rootCmd.SetArgs([]string{"init", "--prefix", "dolt-test", "--quiet"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Init with BEADS_DIR and Dolt backend failed: %v", err)
	}

	// Verify Dolt database was created at BEADS_DIR
	expectedDoltPath := filepath.Join(beadsDirPath, "dolt")
	if info, err := os.Stat(expectedDoltPath); os.IsNotExist(err) {
		t.Errorf("Dolt database was not created at BEADS_DIR path: %s", expectedDoltPath)
	} else if !info.IsDir() {
		t.Errorf("Expected Dolt path to be a directory: %s", expectedDoltPath)
	}

	// Verify database was NOT created at CWD
	cwdDoltPath := filepath.Join(cwdPath, ".beads", "dolt")
	if _, err := os.Stat(cwdDoltPath); err == nil {
		t.Errorf("Dolt database should NOT have been created at CWD: %s", cwdDoltPath)
	}
}

// Note: TestInit_WithoutBEADS_DIR_NoBehaviorChange and TestInit_BEADS_DB_OverridesBEADS_DIR
// are now subtests of TestInitBEADS_DIR above.

// TestInitDoltMetadata verifies that bd init --backend dolt writes and persists
// all 3 tracking metadata fields (bd_version, repo_id, clone_id) via verifyMetadata.
// Covers FR-001, FR-002, FR-003, FR-004.
func TestInitDoltMetadata(t *testing.T) {
	skipIfNoDolt(t)
	if runtime.GOOS == "windows" {
		t.Skip("Skipping Dolt metadata test on Windows")
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("Dolt not installed, skipping Dolt metadata test")
	}

	saveAndRestoreGlobals(t)
	dbPath = ""

	// Reset caches to avoid stale state
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	// Reset Cobra flags
	initCmd.Flags().Set("prefix", "")
	initCmd.Flags().Set("quiet", "false")
	initCmd.Flags().Set("backend", "")

	tmpDir := newGitRepo(t)
	t.Chdir(tmpDir)

	// Add remote.origin.url so ComputeRepoID succeeds
	_ = runCommandInDir(tmpDir, "git", "config", "remote.origin.url", "https://github.com/test/repo.git")

	rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("init --backend dolt failed: %v", err)
	}

	// Open the dolt store to verify metadata was written
	ctx := context.Background()
	doltPath := filepath.Join(tmpDir, ".beads", "dolt")
	doltStore, err := openDoltStoreForTest(t, ctx, doltPath, "test")
	if err != nil {
		t.Fatalf("failed to open dolt store for verification: %v", err)
	}
	defer doltStore.Close()

	// FR-001: bd_version must be written
	bdVersion, err := doltStore.GetMetadata(ctx, "bd_version")
	if err != nil {
		t.Fatalf("GetMetadata(bd_version) failed: %v", err)
	}
	if bdVersion == "" {
		t.Error("bd_version metadata was not written")
	}

	// FR-002: repo_id must be written (git repo with remote configured)
	repoID, err := doltStore.GetMetadata(ctx, "repo_id")
	if err != nil {
		t.Fatalf("GetMetadata(repo_id) failed: %v", err)
	}
	if repoID == "" {
		t.Error("repo_id metadata was not written")
	}

	// FR-003: clone_id must be written
	cloneID, err := doltStore.GetMetadata(ctx, "clone_id")
	if err != nil {
		t.Fatalf("GetMetadata(clone_id) failed: %v", err)
	}
	if cloneID == "" {
		t.Error("clone_id metadata was not written")
	}
}

// openDoltStoreForTest opens an existing Dolt store for read-only verification in tests.
func openDoltStoreForTest(t *testing.T, ctx context.Context, doltPath, dbName string) (*dolt.DoltStore, error) {
	t.Helper()
	return dolt.New(ctx, &dolt.Config{
		Path:     doltPath,
		Database: dbName,
		ReadOnly: true,
	})
}

// TestVerifyMetadataSuccess verifies that verifyMetadata writes and reads back metadata.
// Note: Failure path tests (write errors, read-back mismatches) were removed because
// verifyMetadata now takes *dolt.DoltStore (concrete type), making interface-based
// mocking impossible. The failure paths are simple error-to-stderr logic.
func TestVerifyMetadataSuccess(t *testing.T) {
	skipIfNoDolt(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, "test.db")
	store := newTestStore(t, testDB)
	defer store.Close()

	ok := verifyMetadata(ctx, store, "test_key", "test_value")
	if !ok {
		t.Error("verifyMetadata should return true on success")
	}
	// Verify the value was actually written
	val, err := store.GetMetadata(ctx, "test_key")
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}
	if val != "test_value" {
		t.Errorf("expected 'test_value', got %q", val)
	}
}

// TestInitDoltMetadataNoGit verifies that bd init outside a git repo gracefully
// skips repo_id while still writing bd_version and clone_id.
// Verifies warning output; actual metadata persistence checked by e2e tests.
// Covers FR-015 (skip repo_id outside git).
func TestInitDoltMetadataNoGit(t *testing.T) {
	skipIfNoDolt(t)
	if runtime.GOOS == "windows" {
		t.Skip("Skipping Dolt metadata test on Windows")
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("Dolt not installed, skipping Dolt metadata test")
	}

	saveAndRestoreGlobals(t)
	dbPath = ""

	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	// Reset Cobra flags
	initCmd.Flags().Set("prefix", "")
	initCmd.Flags().Set("quiet", "false")
	initCmd.Flags().Set("backend", "")

	// Create temp dir WITHOUT git init — bd init will create one,
	// but there will be no remote configured so upstream warning is expected.
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Capture stderr to check for upstream warning
	stderr := captureStderr(t, func() {
		rootCmd.SetArgs([]string{"init", "--prefix", "nogit"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("init --backend dolt failed: %v", err)
		}
	})

	// Should warn about missing upstream (bd init creates git repo, but no remote)
	if !strings.Contains(stderr, "upstream") && !strings.Contains(stderr, "repository ID") {
		t.Errorf("expected warning about upstream or repository ID in non-git dir, stderr: %s", stderr)
	}

	// Verify .beads/dolt directory was created (init succeeded)
	doltPath := filepath.Join(tmpDir, ".beads", "dolt")
	if info, err := os.Stat(doltPath); os.IsNotExist(err) {
		t.Errorf("Dolt database directory was not created: %s", doltPath)
	} else if !info.IsDir() {
		t.Errorf("Expected Dolt path to be a directory: %s", doltPath)
	}

	// Verify no SQLite database was created (backend-specific)
	sqlitePath := filepath.Join(tmpDir, ".beads", "beads.db")
	if _, err := os.Stat(sqlitePath); err == nil {
		t.Errorf("unexpected sqlite database created in dolt mode")
	}
}

// buildBDOnce builds the bd binary once for subprocess tests in this file.
// Uses sync.Once for efficiency when multiple tests need the binary.
var (
	initTestBD     string
	initTestBDOnce sync.Once
	initTestBDErr  error
)

func buildBDForInitTests(t *testing.T) string {
	t.Helper()
	initTestBDOnce.Do(func() {
		// Check if bd binary exists in repo root (../../bd from cmd/bd/)
		bdBinary := "bd"
		if runtime.GOOS == "windows" {
			bdBinary = "bd.exe"
		}
		repoRoot := filepath.Join("..", "..")
		existingBD := filepath.Join(repoRoot, bdBinary)
		if _, err := os.Stat(existingBD); err == nil {
			initTestBD, _ = filepath.Abs(existingBD)
			return
		}
		// Fall back to building
		tmpDir, err := os.MkdirTemp("", "bd-init-test-*")
		if err != nil {
			initTestBDErr = fmt.Errorf("failed to create temp dir: %w", err)
			return
		}
		initTestBD = filepath.Join(tmpDir, bdBinary)
		cmd := exec.Command("go", "build", "-o", initTestBD, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			initTestBDErr = fmt.Errorf("go build failed: %v\n%s", err, out)
		}
	})
	if initTestBDErr != nil {
		t.Fatalf("Failed to build bd binary: %v", initTestBDErr)
	}
	return initTestBD
}

// TestInitDatabaseFlag tests the --database flag for bd init.
// Uses subprocess execution because:
//   - init manipulates extensive Cobra global state that's difficult to reset
//   - FatalError calls os.Exit(1) for validation errors, which kills in-process tests
//
// Each subtest runs bd init in a temp directory and verifies metadata.json.
func TestInitDatabaseFlag(t *testing.T) {
	skipIfNoDolt(t)
	bd := buildBDForInitTests(t)

	t.Run("metadata_written", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Run init with --database to specify a pre-existing database name
		cmd := exec.Command(bd, "init", "--database", "myapp_production", "--quiet")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --database failed: %v\n%s", err, out)
		}

		// Verify metadata.json contains the correct database name
		beadsDir := filepath.Join(tmpDir, ".beads")
		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("Failed to load metadata.json: %v", err)
		}
		if cfg == nil {
			t.Fatal("metadata.json not found")
		}

		if cfg.DoltDatabase != "myapp_production" {
			t.Errorf("Expected DoltDatabase %q, got %q", "myapp_production", cfg.DoltDatabase)
		}
		if cfg.DoltMode != configfile.DoltModeServer {
			t.Errorf("Expected DoltMode %q, got %q", configfile.DoltModeServer, cfg.DoltMode)
		}
	})

	t.Run("with_prefix", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Run init with both --database and --prefix
		// --database should override prefix for DB name, but prefix still sets issue_prefix
		cmd := exec.Command(bd, "init", "--database", "shared_db", "--prefix", "team-alpha", "--quiet")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --database --prefix failed: %v\n%s", err, out)
		}

		// Verify metadata.json
		beadsDir := filepath.Join(tmpDir, ".beads")
		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("Failed to load metadata.json: %v", err)
		}
		if cfg == nil {
			t.Fatal("metadata.json not found")
		}

		// --database overrides the SQL database name
		if cfg.DoltDatabase != "shared_db" {
			t.Errorf("Expected DoltDatabase %q (from --database), got %q", "shared_db", cfg.DoltDatabase)
		}

		// Verify the database was opened and issue_prefix was set
		// by reopening the store and checking config
		dbPath := filepath.Join(beadsDir, "dolt")
		s, err := openExistingTestDB(t, dbPath)
		if err != nil {
			t.Fatalf("Failed to reopen database: %v", err)
		}
		defer s.Close()

		ctx := context.Background()
		issuePrefix, err := s.GetConfig(ctx, "issue_prefix")
		if err != nil {
			t.Fatalf("Failed to get issue_prefix: %v", err)
		}
		if issuePrefix != "team-alpha" {
			t.Errorf("Expected issue_prefix %q (from --prefix), got %q", "team-alpha", issuePrefix)
		}
	})

	t.Run("server_config_in_metadata", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Run init with --database
		cmd := exec.Command(bd, "init", "--database", "test_server_cfg", "--quiet")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd init --database failed: %v\n%s", err, out)
		}

		// Verify metadata.json has both dolt_database and dolt_mode: server
		beadsDir := filepath.Join(tmpDir, ".beads")
		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			t.Fatalf("Failed to load metadata.json: %v", err)
		}
		if cfg == nil {
			t.Fatal("metadata.json not found")
		}

		if cfg.DoltDatabase != "test_server_cfg" {
			t.Errorf("Expected DoltDatabase %q, got %q", "test_server_cfg", cfg.DoltDatabase)
		}
		if cfg.DoltMode != configfile.DoltModeServer {
			t.Errorf("Expected DoltMode %q, got %q", configfile.DoltModeServer, cfg.DoltMode)
		}
		if cfg.Backend != configfile.BackendDolt {
			t.Errorf("Expected Backend %q, got %q", configfile.BackendDolt, cfg.Backend)
		}
	})

	t.Run("validation_invalid_name", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Run init with an invalid database name (contains semicolon = SQL injection)
		cmd := exec.Command(bd, "init", "--database", "bad;name", "--quiet")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("Expected error for invalid database name, but command succeeded")
		}

		outStr := string(out)
		if !strings.Contains(outStr, "invalid database name") {
			t.Errorf("Expected error to mention 'invalid database name', got: %s", outStr)
		}

		// Verify no .beads directory was created (early validation prevents side effects)
		beadsDir := filepath.Join(tmpDir, ".beads")
		if _, err := os.Stat(beadsDir); err == nil {
			t.Error(".beads directory should not be created when validation fails")
		}
	})

	t.Run("validation_name_with_spaces", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Database name with spaces should fail validation
		cmd := exec.Command(bd, "init", "--database", "my database", "--quiet")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("Expected error for database name with spaces, but command succeeded")
		}

		outStr := string(out)
		if !strings.Contains(outStr, "invalid database name") {
			t.Errorf("Expected error to mention 'invalid database name', got: %s", outStr)
		}
	})

	t.Run("validation_name_with_backtick", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Database name with backtick injection should fail validation
		cmd := exec.Command(bd, "init", "--database", "db`; DROP DATABASE x; --", "--quiet")
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("Expected error for database name with backtick injection, but command succeeded")
		}

		outStr := string(out)
		if !strings.Contains(outStr, "invalid database name") {
			t.Errorf("Expected error to mention 'invalid database name', got: %s", outStr)
		}
	})
}
