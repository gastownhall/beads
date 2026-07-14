package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun runs a git command in dir and fails the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// gitPorcelain returns `git status --porcelain` output for dir.
func gitPorcelain(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status failed: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestCommitBeadsWorkspaceFiles verifies that the bootstrap workspace-commit
// helper leaves a clean working tree after .beads/ files are written/updated,
// and that it does not sweep up unrelated staged changes (GH#4644).
func TestCommitBeadsWorkspaceFiles(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init")
	gitRun(t, repo, "config", "user.email", "test@example.com")
	gitRun(t, repo, "config", "user.name", "Test")

	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	gitignore := filepath.Join(beadsDir, ".gitignore")

	// Simulate an already-adopted repo: a committed but stale .gitignore.
	if err := os.WriteFile(gitignore, []byte("dolt/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".beads/.gitignore")
	gitRun(t, repo, "commit", "-m", "initial beads gitignore")
	if s := gitPorcelain(t, repo); s != "" {
		t.Fatalf("expected clean tree after initial commit, got:\n%s", s)
	}

	// A concurrent, unrelated staged change the user is preparing.
	unrelated := filepath.Join(repo, "unrelated.txt")
	if err := os.WriteFile(unrelated, []byte("wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "unrelated.txt")

	// Bootstrap appends a missing pattern (as EnsureGitignoreForBeadsDir does)
	// and writes fresh workspace files, leaving them dirty/untracked.
	if err := os.WriteFile(gitignore, []byte("dolt/\nembeddeddolt/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("backend: dolt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	commitBeadsWorkspaceFiles(beadsDir)

	// The .beads/ files must now be committed (not left dirty).
	status := gitPorcelain(t, repo)
	for _, line := range strings.Split(status, "\n") {
		if strings.Contains(line, ".beads/") {
			t.Errorf("expected .beads/ files committed, still dirty: %q\n(full status:\n%s)", line, status)
		}
	}

	// The unrelated staged change must be left untouched (still staged).
	if !strings.Contains(status, "unrelated.txt") {
		t.Errorf("expected unrelated.txt to remain staged, status:\n%s", status)
	}

	// The commit message identifies the bootstrap workspace sync.
	logCmd := exec.Command("git", "log", "-1", "--pretty=%s")
	logCmd.Dir = repo
	logOut, err := logCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logOut), "bd bootstrap") {
		t.Errorf("expected bootstrap commit message, got: %s", logOut)
	}
}

// TestCommitBeadsWorkspaceFiles_NoGitRepo verifies the helper is a safe no-op
// outside a git repository.
func TestCommitBeadsWorkspaceFiles_NoGitRepo(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, ".gitignore"), []byte("dolt/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Must not panic or error; nothing to assert beyond "does not blow up".
	commitBeadsWorkspaceFiles(beadsDir)
}
