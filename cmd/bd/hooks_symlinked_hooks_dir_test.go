package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// gitConfigGetForTest reads one git config value from dir, failing the test
// on a git error. An unset key reads as the empty string.
func gitConfigGetForTest(t *testing.T, dir, key string) string {
	t.Helper()
	cmd := gitCommand("config", "--get", key)
	cmd.Dir = dir
	cmd.Env = scrubGitHookEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return "" // unset key: git exits 1 with no output
		}
		t.Fatalf("git config --get %s: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}

// symlinkHooksDirForTest replaces dir (which must not exist yet) with a
// symlink to target and returns the sentinel hook planted inside target.
func symlinkHooksDirForTest(t *testing.T, dir, target string) string {
	t.Helper()
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "pre-commit")
	writeFileForWorktreeTest(t, sentinel, []byte("#!/bin/sh\necho keep\n"))
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	return sentinel
}

// TestInstallHooksRefusesSymlinkedHooksDirBeforePreservation covers AC-1 for
// the .beads/hooks mode: the refusal must fire before MkdirAll, hook
// preservation, hook writes and the core.hooksPath change, so a fixture with
// a preservation source in .git/hooks catches a directory check placed too
// late — preservation would copy post-rewrite through the link before the
// per-hook guard loop ever ran.
func TestInstallHooksRefusesSymlinkedHooksDirBeforePreservation(t *testing.T) {
	isolateBeadsDirForTest(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	beadsDir := filepath.Join(repo, ".beads")
	writeFileForWorktreeTest(t, filepath.Join(beadsDir, "metadata.json"), []byte("{}\n"))
	runGitForWorktreeTest(t, repo, "init", "-q")
	// Preservation source: a foreign post-rewrite hook in the default
	// .git/hooks directory, which beads would copy into the new hooks dir.
	writeFileForWorktreeTest(t, filepath.Join(repo, ".git", "hooks", "post-rewrite"), []byte("#!/bin/sh\necho preserve\n"))
	t.Chdir(repo)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
	t.Setenv("BEADS_DIR", beadsDir)

	sentinel := symlinkHooksDirForTest(t, filepath.Join(beadsDir, "hooks"), filepath.Join(root, "outside-hooks"))

	err := installHooksWithOptions([]string{"pre-commit"}, false, false, false, true)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected install to refuse symlinked .beads/hooks, got %v", err)
	}
	if got := readFileForWorktreeTest(t, sentinel); got != "#!/bin/sh\necho keep\n" {
		t.Errorf("pre-commit outside the repo modified through symlinked .beads/hooks: %q", got)
	}
	preserved := filepath.Join(root, "outside-hooks", "post-rewrite")
	if _, statErr := os.Lstat(preserved); statErr == nil {
		t.Errorf("preservation copied post-rewrite through the symlinked hooks dir before the refusal")
	}
	if got := gitConfigGetForTest(t, repo, "core.hooksPath"); got != "" {
		t.Errorf("core.hooksPath changed despite refusal: %q", got)
	}
}

// TestInstallHooksRefusesSymlinkedGitHooksDir covers AC-1 for the default
// mode: a .git/hooks directory that is itself a symlink is refused before
// any hook write.
func TestInstallHooksRefusesSymlinkedGitHooksDir(t *testing.T) {
	isolateBeadsDirForTest(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForWorktreeTest(t, repo, "init", "-q")
	if err := os.RemoveAll(filepath.Join(repo, ".git", "hooks")); err != nil {
		t.Fatal(err)
	}
	sentinel := symlinkHooksDirForTest(t, filepath.Join(repo, ".git", "hooks"), filepath.Join(root, "outside-hooks"))
	t.Chdir(repo)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)

	err := installHooksWithOptions([]string{"pre-commit"}, false, false, false, false)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected install to refuse symlinked .git/hooks, got %v", err)
	}
	if got := readFileForWorktreeTest(t, sentinel); got != "#!/bin/sh\necho keep\n" {
		t.Errorf("hook outside the repo modified through symlinked .git/hooks: %q", got)
	}
}

// TestInstallHooksRefusesSymlinkedSharedHooksDir covers AC-1 for the shared
// mode: a .beads-hooks directory that is itself a symlink is refused before
// preservation, writes, or the core.hooksPath change.
func TestInstallHooksRefusesSymlinkedSharedHooksDir(t *testing.T) {
	isolateBeadsDirForTest(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForWorktreeTest(t, repo, "init", "-q")
	sentinel := symlinkHooksDirForTest(t, filepath.Join(repo, ".beads-hooks"), filepath.Join(root, "outside-hooks"))
	t.Chdir(repo)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)

	err := installHooksWithOptions([]string{"pre-commit"}, false, true, false, false)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected install to refuse symlinked .beads-hooks, got %v", err)
	}
	if got := readFileForWorktreeTest(t, sentinel); got != "#!/bin/sh\necho keep\n" {
		t.Errorf("hook outside the repo modified through symlinked .beads-hooks: %q", got)
	}
	if got := gitConfigGetForTest(t, repo, "core.hooksPath"); got != "" {
		t.Errorf("core.hooksPath changed despite refusal: %q", got)
	}
}

// TestInstallHooksRefusesDanglingSymlinkedHooksDir pins the dangling-link
// case: Lstat sees the link itself even when its target does not exist, so
// the refusal fires instead of MkdirAll following the link and creating the
// target.
func TestInstallHooksRefusesDanglingSymlinkedHooksDir(t *testing.T) {
	isolateBeadsDirForTest(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	beadsDir := filepath.Join(repo, ".beads")
	writeFileForWorktreeTest(t, filepath.Join(beadsDir, "metadata.json"), []byte("{}\n"))
	runGitForWorktreeTest(t, repo, "init", "-q")
	t.Chdir(repo)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
	t.Setenv("BEADS_DIR", beadsDir)

	if err := os.Symlink(filepath.Join(root, "missing-hooks"), filepath.Join(beadsDir, "hooks")); err != nil {
		t.Fatal(err)
	}

	err := installHooksWithOptions([]string{"pre-commit"}, false, false, false, true)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected install to refuse dangling symlinked .beads/hooks, got %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "missing-hooks")); statErr == nil {
		t.Errorf("install created the dangling link's target through the symlink")
	}
}

// TestInstallHooksAllowsSymlinkedBeadsWorkspaceAlias pins AC-7's admission
// half: a symlinked .beads workspace (a supported alias — #7889 refused it
// wholesale, which this delta deliberately inverts) still installs into
// .beads/hooks, because only the hooks directory entry itself is checked,
// not its ancestors.
func TestInstallHooksAllowsSymlinkedBeadsWorkspaceAlias(t *testing.T) {
	isolateBeadsDirForTest(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, "real-store")
	writeFileForWorktreeTest(t, filepath.Join(store, "metadata.json"), []byte("{}\n"))
	runGitForWorktreeTest(t, repo, "init", "-q")
	if err := os.Symlink(store, filepath.Join(repo, ".beads")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
	t.Setenv("BEADS_DIR", filepath.Join(repo, ".beads"))

	if err := installHooksWithOptions([]string{"pre-commit"}, false, false, false, true); err != nil {
		t.Fatalf("install into a symlinked .beads workspace alias failed: %v", err)
	}
	body := readFileForWorktreeTest(t, filepath.Join(store, "hooks", "pre-commit"))
	if !strings.Contains(body, hookSectionBeginPrefix) {
		t.Errorf("pre-commit hook missing bd section inside the aliased store: %q", body)
	}
}

// TestInstallHooksAllowsMissingHooksDir pins the allowed half of the
// directory check: a hooks directory that does not exist yet is created, not
// refused.
func TestInstallHooksAllowsMissingHooksDir(t *testing.T) {
	isolateBeadsDirForTest(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	beadsDir := filepath.Join(repo, ".beads")
	writeFileForWorktreeTest(t, filepath.Join(beadsDir, "metadata.json"), []byte("{}\n"))
	runGitForWorktreeTest(t, repo, "init", "-q")
	t.Chdir(repo)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
	t.Setenv("BEADS_DIR", beadsDir)

	if err := installHooksWithOptions([]string{"pre-commit"}, false, false, false, true); err != nil {
		t.Fatalf("install into a fresh hooks directory failed: %v", err)
	}
	hookPath := filepath.Join(beadsDir, "hooks", "pre-commit")
	body := readFileForWorktreeTest(t, hookPath)
	if !strings.Contains(body, hookSectionBeginPrefix) {
		t.Errorf("pre-commit hook missing bd section: %q", body)
	}
}