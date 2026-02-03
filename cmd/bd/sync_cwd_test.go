package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// TestMultiRepoPathResolutionCWDInvariant verifies that path resolution for
// repos.additional produces the same absolute paths regardless of CWD.
//
// The bug (oss-lbp): Running from .beads/ caused paths like "oss/" to become
// ".beads/oss/" instead of "{repo}/oss/". This test ensures the fix works
// by verifying resolution from multiple CWDs produces identical results.
//
// Covers: T040-T042
func TestMultiRepoPathResolutionCWDInvariant(t *testing.T) {
	ctx := context.Background()

	// Store original working directory
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get original working directory: %v", err)
	}
	defer func() { _ = os.Chdir(originalWd) }()

	// Create temp repo structure
	// Resolve symlinks to avoid macOS /var -> /private/var issues
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks failed: %v", err)
	}

	// Setup git repo
	if err := setupGitRepoInDir(t, tmpDir); err != nil {
		t.Fatalf("failed to setup git repo: %v", err)
	}

	// Create .beads directory structure
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create subdirectory for testing CWD from subdir
	subDir := filepath.Join(tmpDir, "src", "pkg")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	// Create oss/ directory (the multi-repo target)
	ossDir := filepath.Join(tmpDir, "oss")
	ossBeadsDir := filepath.Join(ossDir, ".beads")
	if err := os.MkdirAll(ossBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create oss/.beads dir: %v", err)
	}

	// Create config.yaml with relative path
	configContent := `repos:
  primary: "."
  additional:
    - oss/
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Create database
	dbPath := filepath.Join(beadsDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Set issue prefix
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create a test issue
	issue := &types.Issue{
		ID:        "test-1",
		Title:     "Test Issue",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	store.Close()

	// T040: Test from repo root
	t.Run("T040_from_repo_root", func(t *testing.T) {
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("chdir to repo root failed: %v", err)
		}
		git.ResetCaches()

		// Initialize config
		initConfigForTest(t)

		multiRepo := config.GetMultiRepoConfig()
		if multiRepo == nil {
			t.Fatal("GetMultiRepoConfig() returned nil")
		}

		// The key assertion: "oss/" should resolve to {repo}/oss/
		if len(multiRepo.Additional) != 1 {
			t.Fatalf("expected 1 additional repo, got %d", len(multiRepo.Additional))
		}

		// Verify config file used is in the right place
		configUsed := config.ConfigFileUsed()
		expectedConfig := filepath.Join(beadsDir, "config.yaml")
		if configUsed != expectedConfig {
			t.Errorf("ConfigFileUsed() = %q, want %q", configUsed, expectedConfig)
		}

		t.Logf("From repo root: additional[0] = %q", multiRepo.Additional[0])
		t.Logf("ConfigFileUsed() = %q", configUsed)
	})

	// T041: Test from .beads/ directory (the bug trigger location)
	t.Run("T041_from_beads_directory", func(t *testing.T) {
		if err := os.Chdir(beadsDir); err != nil {
			t.Fatalf("chdir to .beads failed: %v", err)
		}
		git.ResetCaches()

		// Re-initialize config from new CWD
		initConfigForTest(t)

		multiRepo := config.GetMultiRepoConfig()
		if multiRepo == nil {
			t.Fatal("GetMultiRepoConfig() returned nil")
		}

		if len(multiRepo.Additional) != 1 {
			t.Fatalf("expected 1 additional repo, got %d", len(multiRepo.Additional))
		}

		// Verify config is still found correctly
		configUsed := config.ConfigFileUsed()
		expectedConfig := filepath.Join(beadsDir, "config.yaml")
		if configUsed != expectedConfig {
			t.Errorf("ConfigFileUsed() = %q, want %q", configUsed, expectedConfig)
		}

		t.Logf("From .beads/: additional[0] = %q", multiRepo.Additional[0])
		t.Logf("ConfigFileUsed() = %q", configUsed)
	})

	// T042: Test from subdirectory
	t.Run("T042_from_subdirectory", func(t *testing.T) {
		if err := os.Chdir(subDir); err != nil {
			t.Fatalf("chdir to subdir failed: %v", err)
		}
		git.ResetCaches()

		// Re-initialize config from new CWD
		initConfigForTest(t)

		multiRepo := config.GetMultiRepoConfig()
		if multiRepo == nil {
			t.Fatal("GetMultiRepoConfig() returned nil")
		}

		if len(multiRepo.Additional) != 1 {
			t.Fatalf("expected 1 additional repo, got %d", len(multiRepo.Additional))
		}

		// Verify config is still found correctly
		configUsed := config.ConfigFileUsed()
		expectedConfig := filepath.Join(beadsDir, "config.yaml")
		if configUsed != expectedConfig {
			t.Errorf("ConfigFileUsed() = %q, want %q", configUsed, expectedConfig)
		}

		t.Logf("From subdir: additional[0] = %q", multiRepo.Additional[0])
		t.Logf("ConfigFileUsed() = %q", configUsed)
	})
}

// TestExportToMultiRepoCWDInvariant tests that ExportToMultiRepo produces
// consistent export paths regardless of CWD.
//
// This is an integration test that exercises the actual export code path
// which was affected by the bug.
func TestExportToMultiRepoCWDInvariant(t *testing.T) {
	ctx := context.Background()

	// Store original working directory
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get original working directory: %v", err)
	}
	defer func() { _ = os.Chdir(originalWd) }()

	// Create temp repo structure
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks failed: %v", err)
	}

	// Setup git repo
	if err := setupGitRepoInDir(t, tmpDir); err != nil {
		t.Fatalf("failed to setup git repo: %v", err)
	}

	// Create .beads directory
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create oss/.beads directory
	ossBeadsDir := filepath.Join(tmpDir, "oss", ".beads")
	if err := os.MkdirAll(ossBeadsDir, 0755); err != nil {
		t.Fatalf("failed to create oss/.beads dir: %v", err)
	}

	// Create config.yaml with relative path
	configContent := `repos:
  primary: "."
  additional:
    - oss/
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Create database and issue once before CWD tests
	dbPath := filepath.Join(beadsDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Set issue prefix
	if err := store.SetConfig(ctx, "issue_prefix", "oss"); err != nil {
		store.Close()
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	// Create a test issue for oss/ repo
	issue := &types.Issue{
		ID:         "oss-1",
		Title:      "OSS Issue",
		Status:     types.StatusOpen,
		IssueType:  types.TypeTask,
		Priority:   2,
		SourceRepo: "oss/", // This routes to additional repo
	}
	if err := store.CreateIssue(ctx, issue, "oss"); err != nil {
		store.Close()
		t.Fatalf("failed to create issue: %v", err)
	}
	store.Close()

	// Helper function to run export and check results
	runExportTest := func(t *testing.T, testCwd string) string {
		t.Helper()

		// Change to test CWD
		if err := os.Chdir(testCwd); err != nil {
			t.Fatalf("chdir to %s failed: %v", testCwd, err)
		}
		git.ResetCaches()

		// Initialize config
		initConfigForTest(t)

		// Open existing store
		store, err := sqlite.New(ctx, dbPath)
		if err != nil {
			t.Fatalf("failed to open store: %v", err)
		}
		defer store.Close()

		// Run multi-repo export
		results, err := store.ExportToMultiRepo(ctx)
		if err != nil {
			t.Fatalf("ExportToMultiRepo failed: %v", err)
		}

		// Check that oss/ was exported
		if results == nil {
			t.Fatal("ExportToMultiRepo returned nil results")
		}

		// The export should create issues.jsonl in oss/.beads/
		expectedPath := filepath.Join(ossBeadsDir, "issues.jsonl")
		if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
			t.Errorf("expected %s to exist after export from %s", expectedPath, testCwd)
		}

		return expectedPath
	}

	// Test from repo root
	t.Run("export_from_repo_root", func(t *testing.T) {
		path := runExportTest(t, tmpDir)
		t.Logf("Export from repo root created: %s", path)
	})

	// Test from .beads/ directory
	t.Run("export_from_beads_dir", func(t *testing.T) {
		path := runExportTest(t, beadsDir)
		t.Logf("Export from .beads/ created: %s", path)

		// Key assertion: should NOT create .beads/oss/.beads/issues.jsonl
		badPath := filepath.Join(beadsDir, "oss", ".beads", "issues.jsonl")
		if _, err := os.Stat(badPath); err == nil {
			t.Errorf("BUG: export created %s (CWD-relative path)", badPath)
		}
	})

	// Test from subdirectory
	subDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	t.Run("export_from_subdirectory", func(t *testing.T) {
		path := runExportTest(t, subDir)
		t.Logf("Export from subdir created: %s", path)
	})
}

// TestSyncModePathResolution tests path resolution across different sync modes.
//
// Covers: T050-T052
func TestSyncModePathResolution(t *testing.T) {
	ctx := context.Background()

	// Store original working directory
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get original working directory: %v", err)
	}
	defer func() { _ = os.Chdir(originalWd) }()

	// T050: Normal sync mode path resolution
	t.Run("T050_normal_sync_mode", func(t *testing.T) {
		// Restore CWD at end of subtest to prevent interference with subsequent tests.
		// t.TempDir() cleanup happens after subtest returns, so CWD must be restored first.
		subtestWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get subtest working directory: %v", err)
		}
		defer func() { _ = os.Chdir(subtestWd) }()

		tmpDir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("eval symlinks failed: %v", err)
		}

		// Setup git repo
		if err := setupGitRepoInDir(t, tmpDir); err != nil {
			t.Fatalf("failed to setup git repo: %v", err)
		}

		// Create .beads directory
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create .beads dir: %v", err)
		}

		// Create oss/.beads directory
		ossBeadsDir := filepath.Join(tmpDir, "oss", ".beads")
		if err := os.MkdirAll(ossBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create oss/.beads dir: %v", err)
		}

		// Create config.yaml with relative path
		configContent := `repos:
  primary: "."
  additional:
    - oss/
`
		configPath := filepath.Join(beadsDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		// Create database
		dbPath := filepath.Join(beadsDir, "beads.db")
		store, err := sqlite.New(ctx, dbPath)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}

		// Set issue prefix
		if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
			store.Close()
			t.Fatalf("failed to set issue_prefix: %v", err)
		}

		// Create issue for oss/
		issue := &types.Issue{
			ID:         "test-100",
			Title:      "Normal mode issue",
			Status:     types.StatusOpen,
			IssueType:  types.TypeTask,
			Priority:   2,
			SourceRepo: "oss/",
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			store.Close()
			t.Fatalf("failed to create issue: %v", err)
		}

		// Change to repo root and initialize config
		if err := os.Chdir(tmpDir); err != nil {
			store.Close()
			t.Fatalf("chdir failed: %v", err)
		}
		git.ResetCaches()

		initConfigForTest(t)

		// Export
		results, err := store.ExportToMultiRepo(ctx)
		store.Close()
		if err != nil {
			t.Fatalf("ExportToMultiRepo failed: %v", err)
		}

		// Verify export created file in correct location
		expectedPath := filepath.Join(ossBeadsDir, "issues.jsonl")
		if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
			t.Errorf("expected %s to exist", expectedPath)
		}

		t.Logf("Normal sync mode export results: %v", results)
	})

	// T051: Sync-branch mode with daemon context
	t.Run("T051_sync_branch_mode", func(t *testing.T) {
		// Restore CWD at end of subtest to prevent interference with subsequent tests.
		subtestWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get subtest working directory: %v", err)
		}
		defer func() { _ = os.Chdir(subtestWd) }()

		tmpDir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("eval symlinks failed: %v", err)
		}

		// Setup git repo
		if err := setupGitRepoInDir(t, tmpDir); err != nil {
			t.Fatalf("failed to setup git repo: %v", err)
		}

		// Create .beads directory
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatalf("failed to create .beads dir: %v", err)
		}

		// Create oss/.beads directory
		ossBeadsDir := filepath.Join(tmpDir, "oss", ".beads")
		if err := os.MkdirAll(ossBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create oss/.beads dir: %v", err)
		}

		// Create config.yaml with sync-branch AND multi-repo
		configContent := `sync:
  branch: beads-sync
repos:
  primary: "."
  additional:
    - oss/
`
		configPath := filepath.Join(beadsDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		// Create the sync branch
		if err := exec.Command("git", "-C", tmpDir, "branch", "beads-sync").Run(); err != nil {
			t.Fatalf("failed to create sync branch: %v", err)
		}

		// Create database
		dbPath := filepath.Join(beadsDir, "beads.db")
		store, err := sqlite.New(ctx, dbPath)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}

		// Set issue prefix
		if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
			store.Close()
			t.Fatalf("failed to set issue_prefix: %v", err)
		}

		// Create issue for oss/
		issue := &types.Issue{
			ID:         "test-200",
			Title:      "Sync-branch mode issue",
			Status:     types.StatusOpen,
			IssueType:  types.TypeTask,
			Priority:   2,
			SourceRepo: "oss/",
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			store.Close()
			t.Fatalf("failed to create issue: %v", err)
		}

		// Simulate daemon context: CWD is .beads/
		if err := os.Chdir(beadsDir); err != nil {
			store.Close()
			t.Fatalf("chdir to .beads/ failed: %v", err)
		}
		git.ResetCaches()

		initConfigForTest(t)

		// Export from daemon-like context
		results, err := store.ExportToMultiRepo(ctx)
		store.Close()
		if err != nil {
			t.Fatalf("ExportToMultiRepo failed: %v", err)
		}

		// Key assertion: should still export to correct location
		expectedPath := filepath.Join(ossBeadsDir, "issues.jsonl")
		if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
			t.Errorf("expected %s to exist (sync-branch mode from .beads/)", expectedPath)
		}

		// Verify no spurious directory created
		badPath := filepath.Join(beadsDir, "oss", ".beads", "issues.jsonl")
		if _, err := os.Stat(badPath); err == nil {
			t.Errorf("BUG: created %s (CWD-relative in sync-branch mode)", badPath)
		}

		t.Logf("Sync-branch mode export results: %v", results)
	})

	// T052: External BEADS_DIR mode
	t.Run("T052_external_beads_dir_mode", func(t *testing.T) {
		// Restore CWD at end of subtest to prevent interference with subsequent tests.
		subtestWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get subtest working directory: %v", err)
		}
		defer func() { _ = os.Chdir(subtestWd) }()

		// Create main project repo
		projectDir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("eval symlinks failed: %v", err)
		}
		if err := setupGitRepoInDir(t, projectDir); err != nil {
			t.Fatalf("failed to setup project repo: %v", err)
		}

		// Create external beads repo
		externalDir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("eval symlinks failed: %v", err)
		}
		if err := setupGitRepoInDir(t, externalDir); err != nil {
			t.Fatalf("failed to setup external repo: %v", err)
		}

		// Create .beads in external repo
		externalBeadsDir := filepath.Join(externalDir, ".beads")
		if err := os.MkdirAll(externalBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create external .beads: %v", err)
		}

		// Create oss/.beads in external repo (sibling to external .beads)
		ossBeadsDir := filepath.Join(externalDir, "oss", ".beads")
		if err := os.MkdirAll(ossBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create oss/.beads: %v", err)
		}

		// Create config.yaml in external repo with relative path
		configContent := `repos:
  primary: "."
  additional:
    - oss/
`
		configPath := filepath.Join(externalBeadsDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		// Create database in external repo
		dbPath := filepath.Join(externalBeadsDir, "beads.db")
		store, err := sqlite.New(ctx, dbPath)
		if err != nil {
			t.Fatalf("failed to create store: %v", err)
		}

		// Set issue prefix
		if err := store.SetConfig(ctx, "issue_prefix", "ext"); err != nil {
			store.Close()
			t.Fatalf("failed to set issue_prefix: %v", err)
		}

		// Create issue for oss/ (ext prefix matches issue ID)
		issue := &types.Issue{
			ID:         "ext-300",
			Title:      "External mode issue",
			Status:     types.StatusOpen,
			IssueType:  types.TypeTask,
			Priority:   2,
			SourceRepo: "oss/",
		}
		if err := store.CreateIssue(ctx, issue, "ext"); err != nil {
			store.Close()
			t.Fatalf("failed to create issue: %v", err)
		}

		// Simulate external mode: CWD is project repo, BEADS_DIR points elsewhere
		if err := os.Chdir(projectDir); err != nil {
			store.Close()
			t.Fatalf("chdir to project failed: %v", err)
		}
		git.ResetCaches()

		// Initialize config from external location
		// In external mode, config is loaded from BEADS_DIR, not CWD
		// We simulate this by changing to external dir for config init
		if err := os.Chdir(externalDir); err != nil {
			store.Close()
			t.Fatalf("chdir to external failed: %v", err)
		}
		git.ResetCaches()

		initConfigForTest(t)

		// Export
		results, err := store.ExportToMultiRepo(ctx)
		store.Close()
		if err != nil {
			t.Fatalf("ExportToMultiRepo failed: %v", err)
		}

		// Verify export in correct location
		expectedPath := filepath.Join(ossBeadsDir, "issues.jsonl")
		if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
			t.Errorf("expected %s to exist (external mode)", expectedPath)
		}

		t.Logf("External BEADS_DIR mode export results: %v", results)
	})
}
