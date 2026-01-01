package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupGitRepoForForkTest creates a temporary git repository for testing
func setupGitRepoForForkTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create .beads directory
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	// Initialize git repo
	cmd := exec.Command("git", "init", "--initial-branch=main")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Configure git user
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	_ = cmd.Run()

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	_ = cmd.Run()

	return dir
}

// addRemote adds a git remote to the test repo
func addRemote(t *testing.T, dir, name, url string) {
	t.Helper()
	cmd := exec.Command("git", "remote", "add", name, url)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to add remote %s: %v", name, err)
	}
}

// ============================================================================
// Test isUpstreamRepo (existing tests, updated)
// ============================================================================

func TestIsUpstreamRepo(t *testing.T) {
	tests := []struct {
		name     string
		remote   string
		expected bool
	}{
		{"ssh upstream", "git@github.com:steveyegge/beads.git", true},
		{"https upstream", "https://github.com/steveyegge/beads.git", true},
		{"https upstream no .git", "https://github.com/steveyegge/beads", true},
		{"fork ssh", "git@github.com:contributor/beads.git", false},
		{"fork https", "https://github.com/contributor/beads.git", false},
		{"different repo", "git@github.com:someone/other-project.git", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify the pattern matching logic matches what isUpstreamRepo uses
			upstreamPatterns := []string{
				"steveyegge/beads",
				"git@github.com:steveyegge/beads",
				"https://github.com/steveyegge/beads",
			}

			matches := false
			for _, pattern := range upstreamPatterns {
				if strings.Contains(tt.remote, pattern) {
					matches = true
					break
				}
			}

			if matches != tt.expected {
				t.Errorf("remote %q: expected upstream=%v, got %v", tt.remote, tt.expected, matches)
			}
		})
	}
}

// Test 1: Upstream maintainer (origin = steveyegge/beads)
func TestIsUpstreamRepo_Maintainer(t *testing.T) {
	dir := setupGitRepoForForkTest(t)
	addRemote(t, dir, "origin", "https://github.com/steveyegge/beads.git")

	if !isUpstreamRepo(dir) {
		t.Error("expected isUpstreamRepo to return true for steveyegge/beads")
	}
}

// Test 1b: Upstream maintainer with SSH URL
func TestIsUpstreamRepo_MaintainerSSH(t *testing.T) {
	dir := setupGitRepoForForkTest(t)
	addRemote(t, dir, "origin", "git@github.com:steveyegge/beads.git")

	if !isUpstreamRepo(dir) {
		t.Error("expected isUpstreamRepo to return true for SSH steveyegge/beads")
	}
}

// Test isUpstreamRepo with non-beads origin
func TestIsUpstreamRepo_NotUpstream(t *testing.T) {
	dir := setupGitRepoForForkTest(t)
	addRemote(t, dir, "origin", "https://github.com/peterkc/beads.git")

	if isUpstreamRepo(dir) {
		t.Error("expected isUpstreamRepo to return false for fork origin")
	}
}

// Test isUpstreamRepo with no origin
func TestIsUpstreamRepo_NoOrigin(t *testing.T) {
	dir := setupGitRepoForForkTest(t)
	// Don't add origin remote

	if isUpstreamRepo(dir) {
		t.Error("expected isUpstreamRepo to return false when no origin exists")
	}
}

// ============================================================================
// Test isForkOfBeads (new tests for GH#823)
// ============================================================================

// Test 2: Fork (standard) - origin=fork, upstream=beads
func TestIsForkOfBeads_StandardFork(t *testing.T) {
	dir := setupGitRepoForForkTest(t)
	addRemote(t, dir, "origin", "https://github.com/peterkc/beads.git")
	addRemote(t, dir, "upstream", "https://github.com/steveyegge/beads.git")

	if !isForkOfBeads(dir) {
		t.Error("expected isForkOfBeads to return true for standard fork setup")
	}
}

// Test 3: Fork (custom naming) - origin=fork, github=beads
func TestIsForkOfBeads_CustomNaming(t *testing.T) {
	dir := setupGitRepoForForkTest(t)
	addRemote(t, dir, "origin", "https://github.com/peterkc/beads.git")
	addRemote(t, dir, "github", "https://github.com/steveyegge/beads.git")

	if !isForkOfBeads(dir) {
		t.Error("expected isForkOfBeads to return true for custom remote naming")
	}
}

// Test 4: User's own project (no beads remote) - THE BUG CASE
func TestIsForkOfBeads_UserProject(t *testing.T) {
	dir := setupGitRepoForForkTest(t)
	addRemote(t, dir, "origin", "https://github.com/mycompany/myapp.git")

	if isForkOfBeads(dir) {
		t.Error("expected isForkOfBeads to return false for user's own project")
	}
}

// Test 5: User's project with unrelated upstream - THE BUG CASE
func TestIsForkOfBeads_UserProjectWithUpstream(t *testing.T) {
	dir := setupGitRepoForForkTest(t)
	addRemote(t, dir, "origin", "https://github.com/mycompany/myapp.git")
	addRemote(t, dir, "upstream", "https://github.com/other/repo.git")

	if isForkOfBeads(dir) {
		t.Error("expected isForkOfBeads to return false for user's project with unrelated upstream")
	}
}

// Test 6: No remotes
func TestIsForkOfBeads_NoRemotes(t *testing.T) {
	dir := setupGitRepoForForkTest(t)
	// Don't add any remotes

	if isForkOfBeads(dir) {
		t.Error("expected isForkOfBeads to return false when no remotes exist")
	}
}

// Test SSH URL detection
func TestIsForkOfBeads_SSHRemote(t *testing.T) {
	dir := setupGitRepoForForkTest(t)
	addRemote(t, dir, "origin", "git@github.com:peterkc/beads.git")
	addRemote(t, dir, "upstream", "git@github.com:steveyegge/beads.git")

	if !isForkOfBeads(dir) {
		t.Error("expected isForkOfBeads to return true for SSH upstream")
	}
}

// ============================================================================
// Test isAlreadyExcluded (existing tests)
// ============================================================================

func TestIsAlreadyExcluded(t *testing.T) {
	// Create temp file with exclusion
	tmpDir := t.TempDir()
	excludePath := filepath.Join(tmpDir, "exclude")

	// Test non-existent file
	if isAlreadyExcluded(excludePath) {
		t.Error("expected non-existent file to return false")
	}

	// Test file without exclusion
	if err := os.WriteFile(excludePath, []byte("*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if isAlreadyExcluded(excludePath) {
		t.Error("expected file without exclusion to return false")
	}

	// Test file with exclusion
	if err := os.WriteFile(excludePath, []byte("*.log\n.beads/issues.jsonl\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !isAlreadyExcluded(excludePath) {
		t.Error("expected file with exclusion to return true")
	}
}

// ============================================================================
// Test addToExclude (existing tests)
// ============================================================================

func TestAddToExclude(t *testing.T) {
	tmpDir := t.TempDir()
	infoDir := filepath.Join(tmpDir, ".git", "info")
	excludePath := filepath.Join(infoDir, "exclude")

	// Test creating new file
	if err := addToExclude(excludePath); err != nil {
		t.Fatalf("addToExclude failed: %v", err)
	}

	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("failed to read exclude file: %v", err)
	}

	if !strings.Contains(string(content), ".beads/issues.jsonl") {
		t.Errorf("exclude file missing .beads/issues.jsonl: %s", content)
	}

	// Test appending to existing file
	if err := os.WriteFile(excludePath, []byte("*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := addToExclude(excludePath); err != nil {
		t.Fatalf("addToExclude append failed: %v", err)
	}

	content, err = os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("failed to read exclude file: %v", err)
	}

	if !strings.Contains(string(content), "*.log") {
		t.Errorf("exclude file missing original content: %s", content)
	}
	if !strings.Contains(string(content), ".beads/issues.jsonl") {
		t.Errorf("exclude file missing .beads/issues.jsonl: %s", content)
	}
}

// ============================================================================
// Test isForkProtectionDisabled (git config opt-out)
// ============================================================================

// Test isForkProtectionDisabled with various git config values
func TestIsForkProtectionDisabled(t *testing.T) {
	tests := []struct {
		name     string
		config   string // value to set, empty = don't set
		expected bool
	}{
		{"not set", "", false},
		{"set to false", "false", true},
		{"set to true", "true", false},
		{"set to other", "disabled", false}, // only "false" disables
		{"set to FALSE", "FALSE", false},    // case-sensitive
		{"set to 0", "0", false},            // only "false" disables
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupGitRepoForForkTest(t)

			if tt.config != "" {
				cmd := exec.Command("git", "-C", dir, "config", "beads.fork-protection", tt.config)
				if err := cmd.Run(); err != nil {
					t.Fatalf("failed to set git config: %v", err)
				}
			}

			result := isForkProtectionDisabled(dir)
			if result != tt.expected {
				t.Errorf("isForkProtectionDisabled() = %v, want %v (config=%q)", result, tt.expected, tt.config)
			}
		})
	}
}

// Test 8: Config opt-out via git config (replaces YAML config)
func TestConfigOptOut_GitConfig(t *testing.T) {
	dir := setupGitRepoForForkTest(t)
	addRemote(t, dir, "origin", "https://github.com/peterkc/beads.git")
	addRemote(t, dir, "upstream", "https://github.com/steveyegge/beads.git")

	// Verify this IS a fork of beads
	if !isForkOfBeads(dir) {
		t.Fatal("expected isForkOfBeads to return true for test setup")
	}

	// Set git config opt-out
	cmd := exec.Command("git", "-C", dir, "config", "beads.fork-protection", "false")
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	// Verify opt-out is detected
	if !isForkProtectionDisabled(dir) {
		t.Error("expected isForkProtectionDisabled to return true after setting git config")
	}
}
