package beads

import (
	"os"
	"os/exec"
	"testing"
)

func initFingerprintGitRepo(t *testing.T, dir string, remote string) {
	t.Helper()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	if remote != "" {
		cmds = append(cmds, []string{"git", "remote", "add", "origin", remote})
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("command %v failed: %v", args, err)
		}
	}
}

func TestCanonicalizeGitURL_HTTPS(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "basic https",
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
			name:     "https with .git and trailing slash",
			input:    "https://github.com/user/repo.git/",
			expected: "github.com/user/repo",
		},
		{
			name:     "http scheme",
			input:    "http://github.com/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "mixed case host",
			input:    "https://GitHub.COM/User/Repo.git",
			expected: "github.com/User/Repo",
		},
		{
			name:     "with standard port 443",
			input:    "https://github.com:443/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "with non-standard port",
			input:    "https://gitlab.example.com:8443/user/repo.git",
			expected: "gitlab.example.com:8443/user/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := canonicalizeGitURL(tt.input)
			if err != nil {
				t.Fatalf("canonicalizeGitURL(%q) returned error: %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("canonicalizeGitURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestCanonicalizeGitURL_SSH(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "ssh scheme",
			input:    "ssh://git@github.com/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "ssh with port 22",
			input:    "ssh://git@github.com:22/user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "ssh with non-standard port",
			input:    "ssh://git@gitlab.example.com:2222/user/repo.git",
			expected: "gitlab.example.com:2222/user/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := canonicalizeGitURL(tt.input)
			if err != nil {
				t.Fatalf("canonicalizeGitURL(%q) returned error: %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("canonicalizeGitURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestCanonicalizeGitURL_SCP(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "basic scp-style",
			input:    "git@github.com:user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "scp-style without .git",
			input:    "git@github.com:user/repo",
			expected: "github.com/user/repo",
		},
		{
			name:     "scp-style with trailing slash",
			input:    "git@github.com:user/repo/",
			expected: "github.com/user/repo",
		},
		{
			name:     "scp-style mixed case host",
			input:    "git@GitHub.COM:user/repo.git",
			expected: "github.com/user/repo",
		},
		{
			name:     "scp-style with custom user",
			input:    "deploy@gitlab.example.com:group/project.git",
			expected: "gitlab.example.com/group/project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := canonicalizeGitURL(tt.input)
			if err != nil {
				t.Fatalf("canonicalizeGitURL(%q) returned error: %v", tt.input, err)
			}
			if result != tt.expected {
				t.Errorf("canonicalizeGitURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestCanonicalizeGitURL_Whitespace(t *testing.T) {
	result, err := canonicalizeGitURL("  https://github.com/user/repo.git  \n")
	if err != nil {
		t.Fatalf("canonicalizeGitURL with whitespace returned error: %v", err)
	}
	if result != "github.com/user/repo" {
		t.Errorf("canonicalizeGitURL with whitespace = %q, want %q", result, "github.com/user/repo")
	}
}

func TestComputeRepoID_WithRemote(t *testing.T) {
	tmpDir := t.TempDir()

	initFingerprintGitRepo(t, tmpDir, "https://github.com/testuser/testrepo.git")

	t.Chdir(tmpDir)

	id, err := ComputeRepoID()
	if err != nil {
		t.Fatalf("ComputeRepoID() returned error: %v", err)
	}
	if id == "" {
		t.Error("ComputeRepoID() returned empty string")
	}
	if len(id) != 32 { // hex encoded 16 bytes
		t.Errorf("ComputeRepoID() returned ID of length %d, want 32", len(id))
	}

	// Same remote should produce same ID
	id2, err := ComputeRepoID()
	if err != nil {
		t.Fatalf("second ComputeRepoID() returned error: %v", err)
	}
	if id != id2 {
		t.Errorf("ComputeRepoID() not deterministic: %q != %q", id, id2)
	}
}

func TestComputeRepoID_WithoutRemote(t *testing.T) {
	tmpDir := t.TempDir()

	initFingerprintGitRepo(t, tmpDir, "")

	t.Chdir(tmpDir)

	// Should fall back to path-based ID
	id, err := ComputeRepoID()
	if err != nil {
		t.Fatalf("ComputeRepoID() without remote returned error: %v", err)
	}
	if id == "" {
		t.Error("ComputeRepoID() without remote returned empty string")
	}
	if len(id) != 32 {
		t.Errorf("ComputeRepoID() returned ID of length %d, want 32", len(id))
	}
}

func TestComputeRepoID_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	_, err := ComputeRepoID()
	if err == nil {
		t.Error("ComputeRepoID() in non-git dir should return error")
	}
}

func TestGetCloneID(t *testing.T) {
	tmpDir := t.TempDir()

	initFingerprintGitRepo(t, tmpDir, "")

	t.Chdir(tmpDir)

	id, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() returned error: %v", err)
	}
	if id == "" {
		t.Error("GetCloneID() returned empty string")
	}
	if len(id) != 16 { // hex encoded 8 bytes
		t.Errorf("GetCloneID() returned ID of length %d, want 16", len(id))
	}

	// Deterministic
	id2, err := GetCloneID()
	if err != nil {
		t.Fatalf("second GetCloneID() returned error: %v", err)
	}
	if id != id2 {
		t.Errorf("GetCloneID() not deterministic: %q != %q", id, id2)
	}
}

func TestGetCloneID_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	_, err := GetCloneID()
	if err == nil {
		t.Error("GetCloneID() in non-git dir should return error")
	}
}

func TestGetCloneID_DifferentClonesDifferentIDs(t *testing.T) {
	// Two different git repos should produce different clone IDs
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	for _, dir := range []string{tmpDir1, tmpDir2} {
		initFingerprintGitRepo(t, dir, "")
	}

	// Get ID from first repo
	t.Chdir(tmpDir1)
	id1, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() for repo1 returned error: %v", err)
	}

	// Get ID from second repo
	if err := os.Chdir(tmpDir2); err != nil {
		t.Fatal(err)
	}
	id2, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() for repo2 returned error: %v", err)
	}

	if id1 == id2 {
		t.Errorf("different clones should have different IDs, both got %q", id1)
	}
}

func TestComputeRepoIDForPath_UsesTargetRepoOutsideCWD(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()

	initFingerprintGitRepo(t, repoA, "https://github.com/testuser/repo-a.git")
	initFingerprintGitRepo(t, repoB, "https://github.com/testuser/repo-b.git")

	t.Chdir(repoB)
	want, err := ComputeRepoID()
	if err != nil {
		t.Fatalf("ComputeRepoID() for target repo returned error: %v", err)
	}

	t.Chdir(repoA)
	got, err := ComputeRepoIDForPath(repoB)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath() returned error: %v", err)
	}

	if got != want {
		t.Errorf("ComputeRepoIDForPath(%q) = %q, want %q", repoB, got, want)
	}
}

// TestComputeRepoID_WorktreeMatchesMainRepo verifies that ComputeRepoIDForPath
// returns the same fingerprint from a worktree as from the main repo, even for
// local repos without a remote (path-based fallback). GH#2867.
func TestComputeRepoID_WorktreeMatchesMainRepo(t *testing.T) {
	mainRepo := t.TempDir()

	// Create main repo (no remote — forces path-based fingerprint)
	initFingerprintGitRepo(t, mainRepo, "")
	// Need at least one commit for worktree creation
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "init")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// Compute fingerprint from main repo
	mainID, err := ComputeRepoIDForPath(mainRepo)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(mainRepo) error: %v", err)
	}

	// Create a worktree
	worktreeDir := t.TempDir()
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "test-wt")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepo
		_ = cmd.Run()
	})

	// Compute fingerprint from worktree — must match main repo
	wtID, err := ComputeRepoIDForPath(worktreeDir)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(worktree) error: %v", err)
	}

	if mainID != wtID {
		t.Errorf("fingerprint mismatch: main=%s worktree=%s (GH#2867)", mainID, wtID)
	}
}

// TestComputeRepoID_WorktreeWithRemoteMatchesMainRepo verifies fingerprint
// stability across worktrees when a remote is configured.
func TestComputeRepoID_WorktreeWithRemoteMatchesMainRepo(t *testing.T) {
	mainRepo := t.TempDir()

	initFingerprintGitRepo(t, mainRepo, "https://github.com/test/repo.git")
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "init")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	mainID, err := ComputeRepoIDForPath(mainRepo)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(mainRepo) error: %v", err)
	}

	worktreeDir := t.TempDir()
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "test-wt-remote")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepo
		_ = cmd.Run()
	})

	wtID, err := ComputeRepoIDForPath(worktreeDir)
	if err != nil {
		t.Fatalf("ComputeRepoIDForPath(worktree) error: %v", err)
	}

	if mainID != wtID {
		t.Errorf("fingerprint mismatch with remote: main=%s worktree=%s", mainID, wtID)
	}
}

// TestGetCloneID_WorktreeMatchesMainRepo verifies clone ID stability across worktrees.
func TestGetCloneID_WorktreeMatchesMainRepo(t *testing.T) {
	mainRepo := t.TempDir()

	initFingerprintGitRepo(t, mainRepo, "")
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "init")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	mainID, err := GetCloneIDForPath(mainRepo)
	if err != nil {
		t.Fatalf("GetCloneIDForPath(mainRepo) error: %v", err)
	}

	worktreeDir := t.TempDir()
	cmd = exec.Command("git", "worktree", "add", worktreeDir, "-b", "test-wt-clone")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git worktree add failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepo
		_ = cmd.Run()
	})

	wtID, err := GetCloneIDForPath(worktreeDir)
	if err != nil {
		t.Fatalf("GetCloneIDForPath(worktree) error: %v", err)
	}

	if mainID != wtID {
		t.Errorf("clone ID mismatch: main=%s worktree=%s (GH#2867)", mainID, wtID)
	}
}

func TestGetCloneIDForPath_UsesTargetRepoOutsideCWD(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()

	initFingerprintGitRepo(t, repoA, "")
	initFingerprintGitRepo(t, repoB, "")

	t.Chdir(repoB)
	want, err := GetCloneID()
	if err != nil {
		t.Fatalf("GetCloneID() for target repo returned error: %v", err)
	}

	t.Chdir(repoA)
	got, err := GetCloneIDForPath(repoB)
	if err != nil {
		t.Fatalf("GetCloneIDForPath() returned error: %v", err)
	}

	if got != want {
		t.Errorf("GetCloneIDForPath(%q) = %q, want %q", repoB, got, want)
	}
}
