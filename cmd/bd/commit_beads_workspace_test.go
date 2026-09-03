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

// gitOut runs a git command in dir and returns its trimmed stdout.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s failed: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// newBeadsRepo creates a git repo with a committed .beads/.gitignore, the
// starting point of an already-adopted workspace.
func newBeadsRepo(t *testing.T) (repo, beadsDir string) {
	t.Helper()
	repo = t.TempDir()
	gitRun(t, repo, "init")
	gitRun(t, repo, "config", "user.email", "test@example.com")
	gitRun(t, repo, "config", "user.name", "Test")

	beadsDir = filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, ".gitignore"), []byte("dolt/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".beads/.gitignore")
	gitRun(t, repo, "commit", "-m", "initial beads gitignore")
	return repo, beadsDir
}

// writeBootstrapWorkspaceFiles simulates what the bootstrap sync path leaves
// on disk: an appended .gitignore pattern plus fresh config/metadata.
func writeBootstrapWorkspaceFiles(t *testing.T, beadsDir string) {
	t.Helper()
	files := map[string]string{
		".gitignore":    "dolt/\nembeddeddolt/\n",
		"config.yaml":   "backend: dolt\n",
		"metadata.json": "{}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCommitBeadsWorkspaceFiles verifies that the bootstrap workspace-commit
// helper leaves a clean working tree after .beads/ files are written/updated,
// and that it does not sweep up unrelated staged changes (GH#4644).
func TestCommitBeadsWorkspaceFiles(t *testing.T) {
	repo, beadsDir := newBeadsRepo(t)
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
	writeBootstrapWorkspaceFiles(t, beadsDir)

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

// TestCommitBeadsWorkspaceFiles_LeavesCollateralBeadsContentAlone verifies the
// helper commits only the three workspace files bootstrap owns. Legitimate
// tracked content lives under .beads/ too (formulas, a git-tracked
// issues.jsonl), and a `.beads/` directory pathspec would silently sweep a
// user's pending edit to any of it into bootstrap's commit.
func TestCommitBeadsWorkspaceFiles_LeavesCollateralBeadsContentAlone(t *testing.T) {
	repo, beadsDir := newBeadsRepo(t)

	// Tracked, committed content inside .beads/ that bootstrap does not own.
	issues := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issues, []byte("{\"id\":\"bd-1\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	formulaDir := filepath.Join(beadsDir, "formulas")
	if err := os.MkdirAll(formulaDir, 0o750); err != nil {
		t.Fatal(err)
	}
	formula := filepath.Join(formulaDir, "mol-x.formula.toml")
	if err := os.WriteFile(formula, []byte("formula = \"mol-x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".beads/issues.jsonl", ".beads/formulas/mol-x.formula.toml")
	gitRun(t, repo, "commit", "-m", "beads content")

	// The user has both mid-edit when bootstrap runs.
	if err := os.WriteFile(issues, []byte("{\"id\":\"bd-1\"}\n{\"id\":\"bd-2\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(formula, []byte("formula = \"mol-x\"\nversion = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeBootstrapWorkspaceFiles(t, beadsDir)

	commitBeadsWorkspaceFiles(beadsDir)

	status := gitPorcelain(t, repo)
	for _, want := range []string{"issues.jsonl", "formulas/mol-x.formula.toml"} {
		if !strings.Contains(status, want) {
			t.Errorf("expected %s to stay uncommitted (bootstrap does not own it), status:\n%s", want, status)
		}
	}
	for _, owned := range beadsWorkspaceFiles {
		if strings.Contains(status, ".beads/"+owned) {
			t.Errorf("expected .beads/%s to be committed, status:\n%s", owned, status)
		}
	}
}

// TestCommitBeadsWorkspaceFiles_LinkedWorktreeDoesNotTouchMainCheckout
// verifies the commit lands in the checkout that physically contains the
// .beads directory, never the main checkout.
//
// RepoContext deliberately resolves RepoRoot to the MAIN checkout for a linked
// worktree (buildRepoContext -> git.GetMainRepoRoot), so routing this helper's
// git operations through it would advance a HEAD the user never asked
// bootstrap to touch.
func TestCommitBeadsWorkspaceFiles_LinkedWorktreeDoesNotTouchMainCheckout(t *testing.T) {
	main, _ := newBeadsRepo(t)

	worktree := filepath.Join(t.TempDir(), "wt")
	gitRun(t, main, "worktree", "add", "-b", "feature", worktree)

	mainHeadBefore := gitOut(t, main, "rev-parse", "HEAD")
	mainBranchBefore := gitOut(t, main, "rev-parse", "--abbrev-ref", "HEAD")
	worktreeHeadBefore := gitOut(t, worktree, "rev-parse", "HEAD")

	worktreeBeads := filepath.Join(worktree, ".beads")
	if err := os.MkdirAll(worktreeBeads, 0o750); err != nil {
		t.Fatal(err)
	}
	writeBootstrapWorkspaceFiles(t, worktreeBeads)

	commitBeadsWorkspaceFiles(worktreeBeads)

	if got := gitOut(t, main, "rev-parse", "HEAD"); got != mainHeadBefore {
		t.Errorf("main checkout HEAD moved: %s -> %s (bootstrap must not commit on another checkout's branch)", mainHeadBefore, got)
	}
	if got := gitOut(t, main, "rev-parse", "--abbrev-ref", "HEAD"); got != mainBranchBefore {
		t.Errorf("main checkout branch changed: %s -> %s", mainBranchBefore, got)
	}
	if got := gitOut(t, worktree, "rev-parse", "HEAD"); got == worktreeHeadBefore {
		t.Errorf("worktree HEAD did not advance; workspace files were not committed where they live")
	}
	if s := gitPorcelain(t, worktree); strings.Contains(s, ".beads/") {
		t.Errorf("expected worktree .beads/ workspace files committed, still dirty:\n%s", s)
	}
}

// TestCommitBeadsWorkspaceFiles_BypassesHooks verifies the commit runs with
// hooks disabled. --no-verify alone does NOT skip prepare-commit-msg, and a
// hook that calls back into bd would deadlock against the embedded Dolt lock
// bootstrap may still hold. Mirrors bd init's `-c core.hooksPath=` invocation
// and its auto_commit_bypasses_hooks regression.
func TestCommitBeadsWorkspaceFiles_BypassesHooks(t *testing.T) {
	repo, beadsDir := newBeadsRepo(t)

	hookPath := filepath.Join(repo, ".git", "hooks", "prepare-commit-msg")
	hook := "#!/bin/sh\necho hook-fired >> .hook-ran\nexit 1\n"
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil { //nolint:gosec // G306: a git hook must be executable
		t.Fatal(err)
	}

	writeBootstrapWorkspaceFiles(t, beadsDir)
	commitBeadsWorkspaceFiles(beadsDir)

	if _, err := os.Stat(filepath.Join(repo, ".hook-ran")); err == nil {
		t.Error("expected the bootstrap commit to bypass git hooks, but prepare-commit-msg ran")
	}
	if s := gitPorcelain(t, repo); strings.Contains(s, ".beads/") {
		t.Errorf("expected workspace files committed despite the failing hook, still dirty:\n%s", s)
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
