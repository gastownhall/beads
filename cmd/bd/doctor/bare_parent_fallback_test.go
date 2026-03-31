package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/utils"
)

func TestResolveBeadsDirForRepo_BareParentWorktreeFallback(t *testing.T) {
	clearResolveBeadsDirCache()
	t.Cleanup(clearResolveBeadsDirCache)

	bareDir, featureWorktreeDir := setupBareParentWorktreeForDoctorTest(t)
	bareBeadsDir := filepath.Join(bareDir, ".beads")
	if err := os.MkdirAll(bareBeadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	resolved := ResolveBeadsDirForRepo(featureWorktreeDir)
	if resolved != utils.CanonicalizePath(bareBeadsDir) {
		t.Fatalf("ResolveBeadsDirForRepo() = %q, want %q", resolved, utils.CanonicalizePath(bareBeadsDir))
	}
}

func TestResolveBeadsDirForRepo_CachesFallbackResult(t *testing.T) {
	clearResolveBeadsDirCache()
	t.Cleanup(clearResolveBeadsDirCache)

	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "feature")
	bareDir := filepath.Join(tmpDir, "repo.git")
	bareBeadsDir := filepath.Join(bareDir, ".beads")
	gitBinDir := filepath.Join(tmpDir, "bin")
	gitLogPath := filepath.Join(tmpDir, "git.log")
	gitScriptPath := filepath.Join(gitBinDir, "git")

	for _, dir := range []string{repoPath, bareBeadsDir, gitBinDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	gitScript := strings.Join([]string{
		"#!/bin/sh",
		"printf 'called\n' >> \"$FAKE_GIT_LOG\"",
		"printf '%s\\n%s\\n' \"$FAKE_GIT_DIR\" \"$FAKE_GIT_COMMON_DIR\"",
		"",
	}, "\n")
	if err := os.WriteFile(gitScriptPath, []byte(gitScript), 0o750); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", gitBinDir)
	t.Setenv("FAKE_GIT_LOG", gitLogPath)
	t.Setenv("FAKE_GIT_DIR", filepath.Join(bareDir, "worktrees", "feature"))
	t.Setenv("FAKE_GIT_COMMON_DIR", bareDir)

	first := ResolveBeadsDirForRepo(repoPath)
	if first != utils.CanonicalizePath(bareBeadsDir) {
		t.Fatalf("first ResolveBeadsDirForRepo() = %q, want %q", first, utils.CanonicalizePath(bareBeadsDir))
	}

	if err := os.Remove(gitScriptPath); err != nil {
		t.Fatal(err)
	}

	second := ResolveBeadsDirForRepo(repoPath)
	if second != first {
		t.Fatalf("second ResolveBeadsDirForRepo() = %q, want cached %q", second, first)
	}

	logData, err := os.ReadFile(gitLogPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(logData), "called\n"); calls != 1 {
		t.Fatalf("git fallback call count = %d, want 1", calls)
	}
}

func TestCheckMetadataVersionTracking_BareParentWorktreeFallback(t *testing.T) {
	clearResolveBeadsDirCache()
	t.Cleanup(clearResolveBeadsDirCache)

	bareDir, featureWorktreeDir := setupBareParentWorktreeForDoctorTest(t)
	bareBeadsDir := filepath.Join(bareDir, ".beads")
	if err := os.MkdirAll(bareBeadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bareBeadsDir, ".local_version"), []byte("0.60.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckMetadataVersionTracking(featureWorktreeDir, "0.60.0")
	if check.Status != StatusOK {
		t.Fatalf("expected ok, got %s: %s", check.Status, check.Message)
	}
}

func TestCheckDoltLocks_BareParentWorktreeFallback(t *testing.T) {
	clearResolveBeadsDirCache()
	t.Cleanup(clearResolveBeadsDirCache)

	bareDir, featureWorktreeDir := setupBareParentWorktreeForDoctorTest(t)
	bareBeadsDir := filepath.Join(bareDir, ".beads")
	if err := os.MkdirAll(bareBeadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bareBeadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BEADS_DOLT_SERVER_PORT", "59999")
	check := CheckDoltLocks(featureWorktreeDir)
	if check.Message == "N/A (not Dolt backend)" {
		t.Fatalf("expected fallback to parent beads dir, got %s", check.Message)
	}
}

func setupBareParentWorktreeForDoctorTest(t *testing.T) (string, string) {
	t.Helper()

	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "repo.git")
	mainWorktreeDir := filepath.Join(tmpDir, "main")
	featureWorktreeDir := filepath.Join(tmpDir, "feature")

	runGitInDirForDoctorTest(t, tmpDir, "init", "--bare", bareDir)
	runGitInDirForDoctorTest(t, tmpDir, "--git-dir", bareDir, "symbolic-ref", "HEAD", "refs/heads/main")
	runGitInDirForDoctorTest(t, tmpDir, "--git-dir", bareDir, "config", "user.email", "test@example.com")
	runGitInDirForDoctorTest(t, tmpDir, "--git-dir", bareDir, "config", "user.name", "Test User")
	emptyTree := runGitInDirForDoctorTest(t, tmpDir, "--git-dir", bareDir, "hash-object", "-t", "tree", "/dev/null")
	initCommit := runGitInDirForDoctorTest(t, tmpDir, "--git-dir", bareDir, "commit-tree", "-m", "Initial commit", emptyTree)
	runGitInDirForDoctorTest(t, tmpDir, "--git-dir", bareDir, "update-ref", "HEAD", initCommit)
	runGitInDirForDoctorTest(t, tmpDir, "--git-dir", bareDir, "worktree", "add", mainWorktreeDir, "main")
	runGitInDirForDoctorTest(t, mainWorktreeDir, "branch", "feature")
	runGitInDirForDoctorTest(t, tmpDir, "--git-dir", bareDir, "worktree", "add", featureWorktreeDir, "feature")

	return bareDir, featureWorktreeDir
}

func runGitInDirForDoctorTest(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed in %s: %v\n%s", args, dir, err, output)
	}

	return strings.TrimSpace(string(output))
}
