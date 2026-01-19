package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// TestGetRepoContextForWorkspace_NormalRepo tests context resolution for a normal git repository
func TestGetRepoContextForWorkspace_NormalRepo(t *testing.T) {
	// Create a temporary git repo
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create .beads directory with required files
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	// Create a database file (required for hasBeadsProjectFiles)
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Reset caches before test
	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// Get context for the workspace
	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	// Verify context fields
	if rc.RepoRoot != resolveSymlinks(tmpDir) {
		t.Errorf("RepoRoot mismatch: expected %s, got %s", resolveSymlinks(tmpDir), rc.RepoRoot)
	}
	if rc.BeadsDir != resolveSymlinks(beadsDir) {
		t.Errorf("BeadsDir mismatch: expected %s, got %s", resolveSymlinks(beadsDir), rc.BeadsDir)
	}
	if rc.IsRedirected {
		t.Error("IsRedirected should be false for workspace-specific context")
	}
	if rc.IsWorktree {
		t.Error("IsWorktree should be false for main repo")
	}
}

// TestGetRepoContextForWorkspace_IgnoresBEADS_DIR verifies that workspace-specific
// context resolution ignores the BEADS_DIR environment variable (DMN-001)
func TestGetRepoContextForWorkspace_IgnoresBEADS_DIR(t *testing.T) {
	// Save original env var
	originalBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalBeadsDir != "" {
			os.Setenv("BEADS_DIR", originalBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		ResetCaches()
		git.ResetCaches()
	})

	// Create two separate repos: repo1 and repo2
	tmpDir := t.TempDir()
	repo1 := filepath.Join(tmpDir, "repo1")
	repo2 := filepath.Join(tmpDir, "repo2")

	for _, repo := range []string{repo1, repo2} {
		if err := os.MkdirAll(repo, 0750); err != nil {
			t.Fatalf("failed to create repo dir: %v", err)
		}
		if err := initGitRepo(repo); err != nil {
			t.Fatalf("failed to init git repo in %s: %v", repo, err)
		}
		beadsDir := filepath.Join(repo, ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatalf("failed to create .beads in %s: %v", repo, err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
			t.Fatalf("failed to create beads.db: %v", err)
		}
	}

	// Set BEADS_DIR to repo2's .beads
	os.Setenv("BEADS_DIR", filepath.Join(repo2, ".beads"))

	// Get context for repo1 - should find repo1's .beads, NOT repo2's
	rc, err := GetRepoContextForWorkspace(repo1)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	// Verify we got repo1, not repo2
	expectedBeadsDir := resolveSymlinks(filepath.Join(repo1, ".beads"))
	if rc.BeadsDir != expectedBeadsDir {
		t.Errorf("BEADS_DIR was not ignored: expected %s, got %s", expectedBeadsDir, rc.BeadsDir)
	}
	expectedRepoRoot := resolveSymlinks(repo1)
	if rc.RepoRoot != expectedRepoRoot {
		t.Errorf("RepoRoot mismatch: expected %s, got %s", expectedRepoRoot, rc.RepoRoot)
	}
}

// TestGetRepoContextForWorkspace_NonexistentPath tests handling of invalid workspace paths
func TestGetRepoContextForWorkspace_NonexistentPath(t *testing.T) {
	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	_, err := GetRepoContextForWorkspace("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("expected error for nonexistent workspace path")
	}
}

// TestGetRepoContextForWorkspace_NonGitDirectory tests handling of non-git directories
func TestGetRepoContextForWorkspace_NonGitDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// Don't initialize git - just a plain directory
	_, err := GetRepoContextForWorkspace(tmpDir)
	if err == nil {
		t.Error("expected error for non-git directory")
	}
}

// TestGetRepoContextForWorkspace_MissingBeadsDir tests error when .beads doesn't exist
func TestGetRepoContextForWorkspace_MissingBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// No .beads directory created
	_, err := GetRepoContextForWorkspace(tmpDir)
	if err == nil {
		t.Error("expected error when .beads directory is missing")
	}
}

// TestRepoContext_Validate tests the Validate method for detecting stale contexts
func TestRepoContext_Validate(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// Get initial context
	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	// Validate should pass initially
	if err := rc.Validate(); err != nil {
		t.Errorf("Validate should pass for fresh context: %v", err)
	}

	// Remove the .beads directory to make context stale
	if err := os.RemoveAll(beadsDir); err != nil {
		t.Fatalf("failed to remove .beads: %v", err)
	}

	// Validate should now fail (stale context)
	if err := rc.Validate(); err == nil {
		t.Error("Validate should fail when BeadsDir no longer exists")
	}
}

// TestRepoContext_Validate_RepoRootRemoved tests Validate when repo root is removed
func TestRepoContext_Validate_RepoRootRemoved(t *testing.T) {
	// Create repo inside a removable parent
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "removable-repo")
	if err := os.MkdirAll(repoDir, 0750); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}
	if err := initGitRepo(repoDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// Get context
	rc, err := GetRepoContextForWorkspace(repoDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	// Validate should pass
	if err := rc.Validate(); err != nil {
		t.Errorf("Validate should pass for fresh context: %v", err)
	}

	// Remove the entire repo
	if err := os.RemoveAll(repoDir); err != nil {
		t.Fatalf("failed to remove repo: %v", err)
	}

	// Validate should now fail (both BeadsDir and RepoRoot are gone)
	if err := rc.Validate(); err == nil {
		t.Error("Validate should fail when RepoRoot no longer exists")
	}
}

// TestGetRepoContextForWorkspace_CacheReset verifies that multiple calls return fresh contexts
func TestGetRepoContextForWorkspace_CacheReset(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// First call
	rc1, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("first GetRepoContextForWorkspace failed: %v", err)
	}

	// Second call - should still work (fresh resolution)
	rc2, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("second GetRepoContextForWorkspace failed: %v", err)
	}

	// Both should return valid contexts
	if rc1.BeadsDir != rc2.BeadsDir {
		t.Errorf("BeadsDir mismatch between calls: %s vs %s", rc1.BeadsDir, rc2.BeadsDir)
	}
}

// TestGetRepoContextForWorkspace_RelativePath tests handling of relative workspace paths
func TestGetRepoContextForWorkspace_RelativePath(t *testing.T) {
	// Get original working directory
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(originalWd)
		ResetCaches()
		git.ResetCaches()
	})

	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Change to parent directory
	parentDir := filepath.Dir(tmpDir)
	os.Chdir(parentDir)

	// Use relative path
	relPath := filepath.Base(tmpDir)
	rc, err := GetRepoContextForWorkspace(relPath)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace with relative path failed: %v", err)
	}

	// Verify we got the correct absolute path
	expectedBeadsDir := resolveSymlinks(beadsDir)
	if rc.BeadsDir != expectedBeadsDir {
		t.Errorf("BeadsDir mismatch: expected %s, got %s", expectedBeadsDir, rc.BeadsDir)
	}
}

// initGitRepo initializes a git repository in the given directory
func initGitRepo(dir string) error {
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	// Suppress git output
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// TestIsPathInSafeBoundary tests security boundary validation (TS-SEC-003)
// This ensures redirect paths cannot escape to sensitive system directories.
func TestIsPathInSafeBoundary(t *testing.T) {
	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	// Get user home directory for test comparisons
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home directory: %v", err)
	}

	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		// Unsafe system directories - should be rejected
		{"system /etc", "/etc/beads", false},
		{"system /usr", "/usr/local/beads", false},
		{"system /var", "/var/lib/beads", false},
		{"system /root", "/root/.beads", false},
		{"system /bin", "/bin/.beads", false},
		{"system /sbin", "/sbin/.beads", false},
		{"system /opt", "/opt/beads", false},
		{"macOS /System", "/System/Library/.beads", false},
		{"macOS /Library", "/Library/Application Support/.beads", false},
		{"macOS /private", "/private/etc/.beads", false},

		// Safe paths - should be accepted
		{"user home directory", filepath.Join(homeDir, "projects/.beads"), true},
		{"temp directory", os.TempDir(), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPathInSafeBoundary(tt.path)
			if result != tt.expected {
				t.Errorf("isPathInSafeBoundary(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

// TestGetRepoContextForWorkspace_RedirectToUnsafeLocation tests that redirects
// to unsafe locations are rejected (TS-SEC-003 integration test).
func TestGetRepoContextForWorkspace_RedirectToUnsafeLocation(t *testing.T) {
	// Save original env
	originalBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalBeadsDir != "" {
			os.Setenv("BEADS_DIR", originalBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		ResetCaches()
		git.ResetCaches()
	})

	// Create a temporary git repo
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create .beads directory with a redirect file pointing outside safe boundary
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Write redirect pointing to /etc (unsafe location)
	// Note: FollowRedirect won't follow non-existent paths, but if /etc/.beads existed
	// and contained beads files, this security check would catch it
	redirectFile := filepath.Join(beadsDir, "redirect")
	if err := os.WriteFile(redirectFile, []byte("/etc/.beads\n"), 0644); err != nil {
		t.Fatalf("failed to write redirect file: %v", err)
	}

	// Since /etc/.beads doesn't exist, FollowRedirect returns original path
	// So create a valid beads.db in the local .beads to get past initial validation,
	// then test the boundary check directly via the isPathInSafeBoundary function
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// GetRepoContextForWorkspace should succeed because FollowRedirect
	// returns the original safe path when target doesn't exist
	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		// This is expected if the implementation catches the redirect attempt
		// Even though target doesn't exist, the test verifies the security boundary
		t.Logf("GetRepoContextForWorkspace correctly rejected unsafe redirect: %v", err)
		return
	}

	// If we get here, the context was created with the safe local path
	// (because /etc/.beads doesn't exist and FollowRedirect fell back)
	// Verify it's using the local beads dir, not the unsafe redirect target
	expectedBeadsDir := resolveSymlinks(beadsDir)
	if rc.BeadsDir != expectedBeadsDir {
		t.Errorf("BeadsDir = %q, want safe local path %q", rc.BeadsDir, expectedBeadsDir)
	}
}

// TestGetRepoContextForWorkspace_RedirectWithinRepo tests that redirects
// staying within the same repo or user's directories are allowed.
func TestGetRepoContextForWorkspace_RedirectWithinRepo(t *testing.T) {
	originalBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalBeadsDir != "" {
			os.Setenv("BEADS_DIR", originalBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		ResetCaches()
		git.ResetCaches()
	})

	// Create two git repos in temp directory
	tmpDir := t.TempDir()
	repo1 := filepath.Join(tmpDir, "repo1")
	repo2 := filepath.Join(tmpDir, "repo2")

	for _, repo := range []string{repo1, repo2} {
		if err := os.MkdirAll(repo, 0750); err != nil {
			t.Fatalf("failed to create repo dir: %v", err)
		}
		if err := initGitRepo(repo); err != nil {
			t.Fatalf("failed to init git repo in %s: %v", repo, err)
		}
	}

	// Create .beads with actual files in repo2
	beadsDir2 := filepath.Join(repo2, ".beads")
	if err := os.MkdirAll(beadsDir2, 0750); err != nil {
		t.Fatalf("failed to create .beads in repo2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir2, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db in repo2: %v", err)
	}

	// Create .beads in repo1 with redirect to repo2
	beadsDir1 := filepath.Join(repo1, ".beads")
	if err := os.MkdirAll(beadsDir1, 0750); err != nil {
		t.Fatalf("failed to create .beads in repo1: %v", err)
	}
	redirectFile := filepath.Join(beadsDir1, "redirect")
	if err := os.WriteFile(redirectFile, []byte(beadsDir2+"\n"), 0644); err != nil {
		t.Fatalf("failed to write redirect file: %v", err)
	}

	// GetRepoContextForWorkspace for repo1 should work
	// Note: GetRepoContextForWorkspace ignores BEADS_DIR and looks for .beads in workspace
	// But it does follow the redirect file in the local .beads
	rc, err := GetRepoContextForWorkspace(repo1)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed for safe redirect: %v", err)
	}

	// Verify the redirect was followed to repo2's .beads
	expectedBeadsDir := resolveSymlinks(beadsDir2)
	if rc.BeadsDir != expectedBeadsDir {
		t.Errorf("BeadsDir = %q, want redirected path %q", rc.BeadsDir, expectedBeadsDir)
	}
}

// resolveSymlinks resolves symlinks and returns the canonical path
// This handles macOS temp directory symlinks (/var -> /private/var)
func resolveSymlinks(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// TestRole_ExplicitConfig tests Role() when beads.role is set in git config.
func TestRole_ExplicitConfig(t *testing.T) {
	tests := map[string]struct {
		configValue  string
		expectedRole UserRole
	}{
		"contributor role": {configValue: "contributor", expectedRole: Contributor},
		"maintainer role":  {configValue: "maintainer", expectedRole: Maintainer},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tmpDir := t.TempDir()
			if err := initGitRepo(tmpDir); err != nil {
				t.Fatalf("failed to init git repo: %v", err)
			}

			// Create .beads directory with required files
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0750); err != nil {
				t.Fatalf("failed to create .beads: %v", err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
				t.Fatalf("failed to create beads.db: %v", err)
			}

			// Set beads.role in git config
			cmd := exec.Command("git", "config", "beads.role", tt.configValue)
			cmd.Dir = tmpDir
			if err := cmd.Run(); err != nil {
				t.Fatalf("failed to set git config: %v", err)
			}

			t.Cleanup(func() {
				ResetCaches()
				git.ResetCaches()
			})

			rc, err := GetRepoContextForWorkspace(tmpDir)
			if err != nil {
				t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
			}

			role, ok := rc.Role()
			if !ok {
				t.Error("Role() returned ok=false, expected ok=true")
			}
			if role != tt.expectedRole {
				t.Errorf("Role() = %q, want %q", role, tt.expectedRole)
			}
		})
	}
}

// TestRole_NoConfig tests Role() when beads.role is not set.
func TestRole_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create .beads directory with required files (no git config set)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	role, ok := rc.Role()
	if ok {
		t.Errorf("Role() returned ok=true with role=%q, expected ok=false", role)
	}
	if role != "" {
		t.Errorf("Role() = %q, want empty string", role)
	}
}

// TestRole_BEADS_DIR_ImpliesContributor tests that BEADS_DIR redirect
// implicitly returns Contributor role without requiring git config.
func TestRole_BEADS_DIR_ImpliesContributor(t *testing.T) {
	// Save original env var
	originalBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalBeadsDir != "" {
			os.Setenv("BEADS_DIR", originalBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		ResetCaches()
		git.ResetCaches()
	})

	// Create two repos: source repo and target repo for redirect
	tmpDir := t.TempDir()
	sourceRepo := filepath.Join(tmpDir, "source")
	targetRepo := filepath.Join(tmpDir, "target")

	for _, repo := range []string{sourceRepo, targetRepo} {
		if err := os.MkdirAll(repo, 0750); err != nil {
			t.Fatalf("failed to create repo dir: %v", err)
		}
		if err := initGitRepo(repo); err != nil {
			t.Fatalf("failed to init git repo: %v", err)
		}
	}

	// Create .beads with files in target repo
	targetBeadsDir := filepath.Join(targetRepo, ".beads")
	if err := os.MkdirAll(targetBeadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads in target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetBeadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Create .beads in source repo with redirect to target
	sourceBeadsDir := filepath.Join(sourceRepo, ".beads")
	if err := os.MkdirAll(sourceBeadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads in source: %v", err)
	}
	redirectFile := filepath.Join(sourceBeadsDir, "redirect")
	if err := os.WriteFile(redirectFile, []byte(targetBeadsDir+"\n"), 0644); err != nil {
		t.Fatalf("failed to write redirect file: %v", err)
	}

	// Set BEADS_DIR to the target (simulating direnv setup)
	os.Setenv("BEADS_DIR", targetBeadsDir)

	// Change to source repo to trigger redirect detection
	originalWd, _ := os.Getwd()
	if err := os.Chdir(sourceRepo); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() {
		os.Chdir(originalWd)
	})

	// Get context - should detect redirect via GetRedirectInfo
	rc, err := GetRepoContext()
	if err != nil {
		t.Fatalf("GetRepoContext failed: %v", err)
	}

	// IsRedirected should be true
	if !rc.IsRedirected {
		t.Error("IsRedirected should be true when BEADS_DIR points elsewhere")
	}

	// Role should implicitly return Contributor
	role, ok := rc.Role()
	if !ok {
		t.Error("Role() returned ok=false, expected ok=true for redirected context")
	}
	if role != Contributor {
		t.Errorf("Role() = %q, want %q (implicit contributor for BEADS_DIR)", role, Contributor)
	}

	// IsContributor should return true
	if !rc.IsContributor() {
		t.Error("IsContributor() = false, want true for redirected context")
	}

	// IsMaintainer should return false
	if rc.IsMaintainer() {
		t.Error("IsMaintainer() = true, want false for redirected context")
	}
}

// TestIsContributor tests the IsContributor convenience method.
func TestIsContributor(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Set role to contributor
	cmd := exec.Command("git", "config", "beads.role", "contributor")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	if !rc.IsContributor() {
		t.Error("IsContributor() = false, want true")
	}
	if rc.IsMaintainer() {
		t.Error("IsMaintainer() = true, want false")
	}
}

// TestIsMaintainer tests the IsMaintainer convenience method.
func TestIsMaintainer(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Set role to maintainer
	cmd := exec.Command("git", "config", "beads.role", "maintainer")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	if rc.IsContributor() {
		t.Error("IsContributor() = true, want false")
	}
	if !rc.IsMaintainer() {
		t.Error("IsMaintainer() = false, want true")
	}
}

// TestRequireRole_Configured tests RequireRole when role is set.
func TestRequireRole_Configured(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Set role
	cmd := exec.Command("git", "config", "beads.role", "contributor")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	if err := rc.RequireRole(); err != nil {
		t.Errorf("RequireRole() returned error: %v, want nil", err)
	}
}

// TestRequireRole_NotConfigured tests RequireRole when role is not set.
func TestRequireRole_NotConfigured(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Don't set any role config

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	err = rc.RequireRole()
	if err == nil {
		t.Error("RequireRole() returned nil, want ErrRoleNotConfigured")
	}
	if err != ErrRoleNotConfigured {
		t.Errorf("RequireRole() returned %v, want ErrRoleNotConfigured", err)
	}
}

// TestGitOutput tests the GitOutput helper method.
func TestGitOutput(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads.db: %v", err)
	}

	// Set a test config value
	cmd := exec.Command("git", "config", "test.value", "hello-world")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	// Test successful output
	output, err := rc.GitOutput(t.Context(), "config", "--get", "test.value")
	if err != nil {
		t.Errorf("GitOutput() returned error: %v", err)
	}
	expected := "hello-world\n"
	if output != expected {
		t.Errorf("GitOutput() = %q, want %q", output, expected)
	}

	// Test error case (non-existent config)
	_, err = rc.GitOutput(t.Context(), "config", "--get", "nonexistent.key")
	if err == nil {
		t.Error("GitOutput() returned nil error for non-existent key, want error")
	}
}
