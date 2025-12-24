package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCanonicalizeGitURL tests URL normalization for various git URL formats
func TestCanonicalizeGitURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// HTTPS URLs
		{
			name:     "https basic",
			input:    "https://github.com/user/repo",
			expected: "github.com/user/repo",
		},
		{
			name:     "https with .git suffix",
			input:    "https://github.com/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "https with trailing slash",
			input:    "https://github.com/user/repo/",
			expected: "github.com/user/repo",
		},
		{
			name:     "https uppercase host",
			input:    "https://GitHub.COM/User/Repo.git",
			expected: "github.com/User/Repo",
		},
		{
			name:     "https with port 443",
			input:    "https://github.com:443/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "https with custom port",
			input:    "https://gitlab.company.com:8443/user/repo.git",
			expected: "gitlab.company.com:8443/user/repo",
		},

		// SSH URLs (protocol style)
		{
			name:     "ssh protocol basic",
			input:    "ssh://git@github.com/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "ssh with port 22",
			input:    "ssh://git@github.com:22/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "ssh with custom port",
			input:    "ssh://git@gitlab.company.com:2222/user/repo.git",
			expected: "gitlab.company.com:2222/user/repo",
		},

		// SCP-style URLs (git@host:path)
		{
			name:     "scp style basic",
			input:    "git@github.com:user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "scp style without .git",
			input:    "git@github.com:user/repo",
			expected: "github.com/user/repo",
		},
		{
			name:     "scp style uppercase host",
			input:    "git@GITHUB.COM:User/Repo.git",
			expected: "github.com/User/Repo",
		},
		{
			name:     "scp style with trailing slash",
			input:    "git@github.com:user/repo/",
			expected: "github.com/user/repo",
		},
		{
			name:     "scp style deep path",
			input:    "git@gitlab.com:org/team/project/repo.git",
			expected: "gitlab.com/org/team/project/repo",
		},

		// HTTP URLs (less common but valid)
		{
			name:     "http basic",
			input:    "http://github.com/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "http with port 80",
			input:    "http://github.com:80/user/repo.git",
			expected: "github.com/user/repo",
		},

		// Git protocol
		{
			name:     "git protocol",
			input:    "git://github.com/user/repo.git",
			expected: "github.com/user/repo",
		},

		// Whitespace handling
		{
			name:     "with leading whitespace",
			input:    "  https://github.com/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "with trailing whitespace",
			input:    "https://github.com/user/repo.git  ",
			expected: "github.com/user/repo",
		},
		{
			name:     "with newline",
			input:    "https://github.com/user/repo.git\n",
			expected: "github.com/user/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := canonicalizeGitURL(tt.input)
			if err != nil {
				t.Fatalf("canonicalizeGitURL(%q) error = %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("canonicalizeGitURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestCanonicalizeGitURL_LocalPath tests that local paths are handled
func TestCanonicalizeGitURL_LocalPath(t *testing.T) {
	// Create a temp directory to use as a "local path"
	tmpDir := t.TempDir()

	// Local absolute path
	result, err := canonicalizeGitURL(tmpDir)
	if err != nil {
		t.Fatalf("canonicalizeGitURL(%q) error = %v", tmpDir, err)
	}

	// Should return a forward-slash path
	if strings.Contains(result, "\\") {
		t.Errorf("canonicalizeGitURL(%q) = %q, should use forward slashes", tmpDir, result)
	}
}

// TestCanonicalizeGitURL_WindowsPath tests Windows path detection
func TestCanonicalizeGitURL_WindowsPath(t *testing.T) {
	// This tests the Windows path detection logic (C:/)
	// The function should NOT treat "C:/foo/bar" as an scp-style URL
	tests := []struct {
		input    string
		expected string
	}{
		// These are NOT scp-style URLs - they're Windows paths
		{"C:/Users/test/repo", "C:/Users/test/repo"},
		{"D:/projects/myrepo", "D:/projects/myrepo"},
	}

	for _, tt := range tests {
		result, err := canonicalizeGitURL(tt.input)
		if err != nil {
			t.Fatalf("canonicalizeGitURL(%q) error = %v", tt.input, err)
		}
		// Should preserve the Windows path structure (forward slashes)
		if !strings.Contains(result, "/") {
			t.Errorf("canonicalizeGitURL(%q) = %q, expected path with slashes", tt.input, result)
		}
	}
}

// TestComputeRepoID_WithRemote tests ComputeRepoID when remote.origin.url exists
func TestComputeRepoID_WithRemote(t *testing.T) {
	// Create temporary directory for test repo
	tmpDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Configure git user
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Set remote.origin.url
	cmd = exec.Command("git", "remote", "add", "origin", "https://github.com/user/test-repo.git")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add failed: %v", err)
	}

	// Change to repo dir
	t.Chdir(tmpDir)

	// ComputeRepoID should return a consistent hash
	result1, err := ComputeRepoID()
	if err != nil {
		t.Fatalf("ComputeRepoID() error = %v", err)
	}

	// Should be a 32-character hex string (16 bytes)
	if len(result1) != 32 {
		t.Errorf("ComputeRepoID() = %q, expected 32 character hex string", result1)
	}

	// Should be consistent across calls
	result2, err := ComputeRepoID()
	if err != nil {
		t.Fatalf("ComputeRepoID() second call error = %v", err)
	}
	if result1 != result2 {
		t.Errorf("ComputeRepoID() not consistent: %q vs %q", result1, result2)
	}
}

// TestComputeRepoID_NoRemote tests ComputeRepoID when no remote exists
func TestComputeRepoID_NoRemote(t *testing.T) {
	// Create temporary directory for test repo
	tmpDir := t.TempDir()

	// Initialize git repo (no remote)
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Change to repo dir
	t.Chdir(tmpDir)

	// ComputeRepoID should fall back to using the local path
	result, err := ComputeRepoID()
	if err != nil {
		t.Fatalf("ComputeRepoID() error = %v", err)
	}

	// Should still return a 32-character hex string
	if len(result) != 32 {
		t.Errorf("ComputeRepoID() = %q, expected 32 character hex string", result)
	}
}

// TestComputeRepoID_NotGitRepo tests ComputeRepoID when not in a git repo
func TestComputeRepoID_NotGitRepo(t *testing.T) {
	// Create temporary directory that is NOT a git repo
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)

	// ComputeRepoID should return an error
	_, err := ComputeRepoID()
	if err == nil {
		t.Error("ComputeRepoID() expected error for non-git directory, got nil")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("ComputeRepoID() error = %q, expected 'not a git repository'", err.Error())
	}
}

// TestComputeRepoID_DifferentRemotesSameCanonical tests that different URL formats
// for the same repo produce the same ID
func TestComputeRepoID_DifferentRemotesSameCanonical(t *testing.T) {
	remotes := []string{
		"https://github.com/user/repo.git",
		"git@github.com:user/repo.git",
		"ssh://git@github.com/user/repo.git",
	}

	var ids []string

	for _, remote := range remotes {
		tmpDir := t.TempDir()

		// Initialize git repo
		cmd := exec.Command("git", "init")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}

		// Set remote
		cmd = exec.Command("git", "remote", "add", "origin", remote)
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git remote add failed for %q: %v", remote, err)
		}

		t.Chdir(tmpDir)

		id, err := ComputeRepoID()
		if err != nil {
			t.Fatalf("ComputeRepoID() for remote %q error = %v", remote, err)
		}
		ids = append(ids, id)
	}

	// All IDs should be the same since they point to the same canonical repo
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[0] {
			t.Errorf("ComputeRepoID() produced different IDs for same repo:\n  remote[0]=%q id=%s\n  remote[%d]=%q id=%s",
				remotes[0], ids[0], i, remotes[i], ids[i])
		}
	}
}

// TestGetCloneID_Basic tests GetCloneID returns a consistent ID
func TestGetCloneID_Basic(t *testing.T) {
	// Create temporary directory for test repo
	tmpDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	t.Chdir(tmpDir)

	// GetCloneID should return a consistent hash
	result1, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() error = %v", err)
	}

	// Should be a 16-character hex string (8 bytes)
	if len(result1) != 16 {
		t.Errorf("GetCloneID() = %q, expected 16 character hex string", result1)
	}

	// Should be consistent across calls
	result2, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() second call error = %v", err)
	}
	if result1 != result2 {
		t.Errorf("GetCloneID() not consistent: %q vs %q", result1, result2)
	}
}

// TestGetCloneID_DifferentDirs tests GetCloneID produces different IDs for different clones
func TestGetCloneID_DifferentDirs(t *testing.T) {
	ids := make(map[string]string)

	for i := 0; i < 3; i++ {
		tmpDir := t.TempDir()

		// Initialize git repo
		cmd := exec.Command("git", "init")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}

		t.Chdir(tmpDir)

		id, err := GetCloneID()
		if err != nil {
			t.Fatalf("GetCloneID() error = %v", err)
		}

		// Each clone should have a unique ID
		if prev, exists := ids[id]; exists {
			t.Errorf("GetCloneID() produced duplicate ID %q for dirs %q and %q", id, prev, tmpDir)
		}
		ids[id] = tmpDir
	}
}

// TestGetCloneID_NotGitRepo tests GetCloneID when not in a git repo
func TestGetCloneID_NotGitRepo(t *testing.T) {
	// Create temporary directory that is NOT a git repo
	tmpDir := t.TempDir()

	t.Chdir(tmpDir)

	// GetCloneID should return an error
	_, err := GetCloneID()
	if err == nil {
		t.Error("GetCloneID() expected error for non-git directory, got nil")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("GetCloneID() error = %q, expected 'not a git repository'", err.Error())
	}
}

// TestGetCloneID_IncludesHostname tests that GetCloneID includes hostname
// to differentiate the same path on different machines
func TestGetCloneID_IncludesHostname(t *testing.T) {
	// This test verifies the concept - we can't actually test different hostnames
	// but we can verify that the same path produces the same ID on this machine
	tmpDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	t.Chdir(tmpDir)

	hostname, _ := os.Hostname()
	id, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() error = %v", err)
	}

	// Just verify we got a valid ID - we can't test different hostnames
	// but the implementation includes hostname in the hash
	if len(id) != 16 {
		t.Errorf("GetCloneID() = %q, expected 16 character hex string (hostname=%s)", id, hostname)
	}
}

// TestGetCloneID_Worktree tests GetCloneID in a worktree
func TestGetCloneID_Worktree(t *testing.T) {
	// Create temporary directory for test
	tmpDir := t.TempDir()

	// Initialize main git repo
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

	// Create initial commit (required for worktree)
	dummyFile := filepath.Join(mainRepoDir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "README.md")
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

	// Get IDs from both locations
	t.Chdir(mainRepoDir)
	mainID, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() in main repo error = %v", err)
	}

	t.Chdir(worktreeDir)
	worktreeID, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() in worktree error = %v", err)
	}

	// Worktree should have a DIFFERENT ID than main repo
	// because they're different paths (different clones conceptually)
	if mainID == worktreeID {
		t.Errorf("GetCloneID() returned same ID for main repo and worktree - should be different")
	}
}
