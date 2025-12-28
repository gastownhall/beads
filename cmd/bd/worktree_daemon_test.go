package main

import (
	"database/sql"
	"os"
	"os/exec"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"

	// Import SQLite driver for test database creation
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// TestShouldDisableDaemonForWorktree tests the worktree daemon disable logic.
// The function should return true (disable daemon) when:
// - In a git worktree AND sync-branch is NOT configured
// The function should return false (allow daemon) when:
// - Not in a worktree (regular repo)
// - In a worktree but sync-branch IS configured
func TestShouldDisableDaemonForWorktree(t *testing.T) {
	// Initialize config for tests
	if err := config.Initialize(); err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}

	// Save and restore environment variables
	origSyncBranch := os.Getenv("BEADS_SYNC_BRANCH")
	defer func() {
		if origSyncBranch != "" {
			os.Setenv("BEADS_SYNC_BRANCH", origSyncBranch)
		} else {
			os.Unsetenv("BEADS_SYNC_BRANCH")
		}
	}()

	t.Run("returns false in regular repo without sync-branch", func(t *testing.T) {
		// Create a regular git repo (not a worktree) using existing helper
		repoPath, cleanup := setupGitRepo(t)
		defer cleanup()
		_ = repoPath // repoPath is the current directory after setupGitRepo

		// No sync-branch configured
		os.Unsetenv("BEADS_SYNC_BRANCH")

		result := shouldDisableDaemonForWorktree()
		if result {
			t.Error("Expected shouldDisableDaemonForWorktree() to return false in regular repo")
		}
	})

	t.Run("returns false in regular repo with sync-branch", func(t *testing.T) {
		// Create a regular git repo (not a worktree) using existing helper
		_, cleanup := setupGitRepo(t)
		defer cleanup()

		// Sync-branch configured
		os.Setenv("BEADS_SYNC_BRANCH", "beads-metadata")

		result := shouldDisableDaemonForWorktree()
		if result {
			t.Error("Expected shouldDisableDaemonForWorktree() to return false in regular repo with sync-branch")
		}
	})

	t.Run("returns true in worktree without sync-branch", func(t *testing.T) {
		// Create a git repo with a worktree
		mainDir, worktreeDir := setupWorktreeTestRepo(t)

		// Change to the worktree directory
		origDir, _ := os.Getwd()
		defer func() {
			_ = os.Chdir(origDir)
			// Reset git caches after changing directory
			git.ResetCaches()
			// Reinitialize config to restore original state
			_ = config.Initialize()
		}()
		if err := os.Chdir(worktreeDir); err != nil {
			t.Fatalf("Failed to change to worktree dir: %v", err)
		}
		git.ResetCaches()

		// Reset git caches after changing directory (required for IsWorktree to re-detect)
		git.ResetCaches()

		// No sync-branch configured
		os.Unsetenv("BEADS_SYNC_BRANCH")

		// Reinitialize config to pick up the new directory's config.yaml
		if err := config.Initialize(); err != nil {
			t.Fatalf("Failed to reinitialize config: %v", err)
		}

		// Debug: verify we're actually in a worktree
		isWorktree := isGitWorktree()
		t.Logf("isGitWorktree() = %v, worktreeDir = %s", isWorktree, worktreeDir)

		result := shouldDisableDaemonForWorktree()
		if !result {
			t.Errorf("Expected shouldDisableDaemonForWorktree() to return true in worktree without sync-branch (isWorktree=%v)", isWorktree)
		}

		// Cleanup
		cleanupTestWorktree(t, mainDir, worktreeDir)
	})

	t.Run("returns false in worktree with sync-branch configured", func(t *testing.T) {
		// Create a git repo with a worktree
		mainDir, worktreeDir := setupWorktreeTestRepo(t)

		// Change to the worktree directory
		origDir, _ := os.Getwd()
		defer func() {
			_ = os.Chdir(origDir)
			git.ResetCaches()
			_ = config.Initialize()
		}()
		if err := os.Chdir(worktreeDir); err != nil {
			t.Fatalf("Failed to change to worktree dir: %v", err)
		}
		git.ResetCaches()

		// Reset git caches after changing directory
		git.ResetCaches()

		// Reinitialize config to pick up the new directory's config.yaml
		if err := config.Initialize(); err != nil {
			t.Fatalf("Failed to reinitialize config: %v", err)
		}

		// Sync-branch configured via environment variable
		os.Setenv("BEADS_SYNC_BRANCH", "beads-metadata")

		result := shouldDisableDaemonForWorktree()
		if result {
			t.Error("Expected shouldDisableDaemonForWorktree() to return false in worktree with sync-branch")
		}

		// Cleanup
		cleanupTestWorktree(t, mainDir, worktreeDir)
	})

	t.Run("returns false in worktree with sync-branch in database config", func(t *testing.T) {
		// Create a git repo with a worktree AND a database with sync.branch config
		mainDir, worktreeDir := setupWorktreeTestRepoWithDB(t, "beads-metadata")

		// Change to the worktree directory
		origDir, _ := os.Getwd()
		defer func() {
			_ = os.Chdir(origDir)
			git.ResetCaches()
			_ = config.Initialize()
		}()
		if err := os.Chdir(worktreeDir); err != nil {
			t.Fatalf("Failed to change to worktree dir: %v", err)
		}
		git.ResetCaches()

		// Reset git caches after changing directory
		git.ResetCaches()

		// Reinitialize config to pick up the new directory's config.yaml
		if err := config.Initialize(); err != nil {
			t.Fatalf("Failed to reinitialize config: %v", err)
		}

		// NO env var or config.yaml sync-branch - only database config
		os.Unsetenv("BEADS_SYNC_BRANCH")

		result := shouldDisableDaemonForWorktree()
		if result {
			t.Error("Expected shouldDisableDaemonForWorktree() to return false in worktree with sync-branch in database")
		}

		// Cleanup
		cleanupTestWorktree(t, mainDir, worktreeDir)
	})
}

// TestShouldAutoStartDaemonWorktreeIntegration tests that shouldAutoStartDaemon
// respects the worktree+sync-branch logic.
func TestShouldAutoStartDaemonWorktreeIntegration(t *testing.T) {
	// Initialize config for tests
	if err := config.Initialize(); err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}

	// Save and restore environment variables
	origNoDaemon := os.Getenv("BEADS_NO_DAEMON")
	origAutoStart := os.Getenv("BEADS_AUTO_START_DAEMON")
	origSyncBranch := os.Getenv("BEADS_SYNC_BRANCH")
	defer func() {
		restoreTestEnv("BEADS_NO_DAEMON", origNoDaemon)
		restoreTestEnv("BEADS_AUTO_START_DAEMON", origAutoStart)
		restoreTestEnv("BEADS_SYNC_BRANCH", origSyncBranch)
	}()

	t.Run("disables auto-start in worktree without sync-branch", func(t *testing.T) {
		// Create a git repo with a worktree
		mainDir, worktreeDir := setupWorktreeTestRepo(t)

		// Change to the worktree directory
		origDir, _ := os.Getwd()
		defer func() {
			_ = os.Chdir(origDir)
			git.ResetCaches()
			_ = config.Initialize()
		}()
		if err := os.Chdir(worktreeDir); err != nil {
			t.Fatalf("Failed to change to worktree dir: %v", err)
		}
		git.ResetCaches()

		// Reset git caches after changing directory
		git.ResetCaches()

		// Clear all daemon-related env vars
		os.Unsetenv("BEADS_NO_DAEMON")
		os.Unsetenv("BEADS_AUTO_START_DAEMON")
		os.Unsetenv("BEADS_SYNC_BRANCH")

		// Reinitialize config to pick up the new directory's config.yaml
		if err := config.Initialize(); err != nil {
			t.Fatalf("Failed to reinitialize config: %v", err)
		}

		result := shouldAutoStartDaemon()
		if result {
			t.Error("Expected shouldAutoStartDaemon() to return false in worktree without sync-branch")
		}

		// Cleanup
		cleanupTestWorktree(t, mainDir, worktreeDir)
	})

	t.Run("enables auto-start in worktree with sync-branch", func(t *testing.T) {
		// Create a git repo with a worktree
		mainDir, worktreeDir := setupWorktreeTestRepo(t)

		// Change to the worktree directory
		origDir, _ := os.Getwd()
		defer func() {
			_ = os.Chdir(origDir)
			git.ResetCaches()
			_ = config.Initialize()
		}()
		if err := os.Chdir(worktreeDir); err != nil {
			t.Fatalf("Failed to change to worktree dir: %v", err)
		}
		git.ResetCaches()

		// Reset git caches after changing directory
		git.ResetCaches()

		// Reinitialize config to pick up the new directory's config.yaml
		if err := config.Initialize(); err != nil {
			t.Fatalf("Failed to reinitialize config: %v", err)
		}

		// Clear daemon env vars but set sync-branch
		os.Unsetenv("BEADS_NO_DAEMON")
		os.Unsetenv("BEADS_AUTO_START_DAEMON")
		os.Setenv("BEADS_SYNC_BRANCH", "beads-metadata")

		result := shouldAutoStartDaemon()
		if !result {
			t.Error("Expected shouldAutoStartDaemon() to return true in worktree with sync-branch")
		}

		// Cleanup
		cleanupTestWorktree(t, mainDir, worktreeDir)
	})

	t.Run("BEADS_NO_DAEMON still takes precedence in worktree", func(t *testing.T) {
		// Create a git repo with a worktree
		mainDir, worktreeDir := setupWorktreeTestRepo(t)

		// Change to the worktree directory
		origDir, _ := os.Getwd()
		defer func() {
			_ = os.Chdir(origDir)
			git.ResetCaches()
			_ = config.Initialize()
		}()
		if err := os.Chdir(worktreeDir); err != nil {
			t.Fatalf("Failed to change to worktree dir: %v", err)
		}
		git.ResetCaches()

		// Reset git caches after changing directory
		git.ResetCaches()

		// Reinitialize config to pick up the new directory's config.yaml
		if err := config.Initialize(); err != nil {
			t.Fatalf("Failed to reinitialize config: %v", err)
		}

		// Set BEADS_NO_DAEMON (should override everything)
		os.Setenv("BEADS_NO_DAEMON", "1")
		os.Setenv("BEADS_SYNC_BRANCH", "beads-metadata")

		result := shouldAutoStartDaemon()
		if result {
			t.Error("Expected BEADS_NO_DAEMON=1 to disable auto-start even with sync-branch")
		}

		// Cleanup
		cleanupTestWorktree(t, mainDir, worktreeDir)
	})
}

// Helper functions for worktree daemon tests

func restoreTestEnv(key, value string) {
	if value != "" {
		os.Setenv(key, value)
	} else {
		os.Unsetenv(key)
	}
}

// setupWorktreeTestRepo creates a git repo with a worktree for testing.
// Returns the main repo directory and worktree directory.
// Caller is responsible for cleanup via cleanupTestWorktree.
// 
// IMPORTANT: This function also reinitializes the config package to use the
// temp directory's config, avoiding interference from the beads project's own config.
func setupWorktreeTestRepo(t *testing.T) (mainDir, worktreeDir string) {
	t.Helper()

	// Create main repo directory
	mainDir = t.TempDir()

	// Initialize git repo with 'main' as default branch (modern git convention)
	cmd := exec.Command("git", "init", "--initial-branch=main")
	cmd.Dir = mainDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to init git repo: %v\n%s", err, output)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = mainDir
	_ = cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = mainDir
	_ = cmd.Run()

	// Create .beads directory with empty config (no sync-branch)
	beadsDir := mainDir + "/.beads"
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	// Create minimal config.yaml without sync-branch
	configContent := "# Test config\nissue-prefix: \"test\"\n"
	if err := os.WriteFile(beadsDir+"/config.yaml", []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to create config.yaml: %v", err)
	}

	// Create initial commit (required for worktrees)
	if err := os.WriteFile(mainDir+"/README.md", []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = mainDir
	_ = cmd.Run()

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = mainDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create initial commit: %v\n%s", err, output)
	}

	// Create a branch for the worktree
	cmd = exec.Command("git", "branch", "feature-branch")
	cmd.Dir = mainDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create branch: %v\n%s", err, output)
	}

	// Create worktree directory (must be outside main repo)
	worktreeDir = t.TempDir()

	// Add worktree
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "feature-branch")
	cmd.Dir = mainDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create worktree: %v\n%s", err, output)
	}

	return mainDir, worktreeDir
}

// cleanupTestWorktree removes a worktree created by setupWorktreeTestRepo.
func cleanupTestWorktree(t *testing.T, mainDir, worktreeDir string) {
	t.Helper()

	// Remove worktree
	cmd := exec.Command("git", "worktree", "remove", worktreeDir, "--force")
	cmd.Dir = mainDir
	_ = cmd.Run() // Best effort cleanup
}

// setupWorktreeTestRepoWithDB creates a git repo with a worktree AND a database
// that has sync.branch configured. This tests the database config path.
func setupWorktreeTestRepoWithDB(t *testing.T, syncBranch string) (mainDir, worktreeDir string) {
	t.Helper()

	// First create the basic worktree repo
	mainDir, worktreeDir = setupWorktreeTestRepo(t)

	// Now create a database with sync.branch config
	beadsDir := mainDir + "/.beads"
	dbPath := beadsDir + "/beads.db"

	// Create a minimal SQLite database with the config table and sync.branch value
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create config table
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT)`)
	if err != nil {
		t.Fatalf("Failed to create config table: %v", err)
	}

	// Insert sync.branch config
	_, err = db.Exec(`INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)`, "sync.branch", syncBranch)
	if err != nil {
		t.Fatalf("Failed to insert sync.branch config: %v", err)
	}

	return mainDir, worktreeDir
}
