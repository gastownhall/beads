package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	internalgit "github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/utils"
)

// TestWorktreeCommandNoStoreContract locks the complete command subtree that
// inherit worktreeCmd's store exemption. A new child must be reviewed and added
// here deliberately; a store-backed child cannot silently inherit the parent
// annotation.
func TestWorktreeCommandNoStoreContract(t *testing.T) {
	want := map[string]*cobra.Command{
		"create": worktreeCreateCmd,
		"info":   worktreeInfoCmd,
		"list":   worktreeListCmd,
		"remove": worktreeRemoveCmd,
	}

	var descendants []*cobra.Command
	var walk func(*cobra.Command)
	walk = func(parent *cobra.Command) {
		for _, child := range parent.Commands() {
			descendants = append(descendants, child)
			walk(child)
		}
	}
	walk(worktreeCmd)

	if len(descendants) != len(want) {
		paths := make([]string, 0, len(descendants))
		for _, descendant := range descendants {
			paths = append(paths, descendant.CommandPath())
		}
		t.Fatalf("worktree command inventory = %v; update the no-store contract deliberately for any added or removed descendant", paths)
	}

	for _, descendant := range descendants {
		wantCommand, ok := want[descendant.Name()]
		if !ok {
			t.Errorf("worktree descendant %q is not reviewed for the no-store contract", descendant.CommandPath())
			continue
		}
		if descendant != wantCommand {
			t.Errorf("worktree descendant %q = %p, want registered command %p", descendant.CommandPath(), descendant, wantCommand)
		}
		if !commandOptsOutOfStore(descendant) {
			t.Errorf("worktree descendant %q does not inherit the store exemption", descendant.CommandPath())
		}
	}
}

// TestWorktreeCreateRejectsInvalidPathBeforeStoreOpen drives the real root
// pre-run and create handler against a configured but absent server store. The
// existing target is rejected by the worktree command itself; if the parent
// annotation is removed, PersistentPreRunE attempts the absent store first and
// this test fails before reaching that command-specific refusal.
func TestWorktreeCreateRejectsInvalidPathBeforeStoreOpen(t *testing.T) {
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")
	writeMetadataConfig(t, beadsDir, configfile.DoltModeServer, "worktree_skip_store_test")

	existingPath := filepath.Join(repoDir, "already-exists")
	if err := os.Mkdir(existingPath, 0o755); err != nil {
		t.Fatalf("create existing worktree target: %v", err)
	}

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BD_DISABLE_METRICS", "1")
	t.Setenv("BD_DISABLE_EVENT_FLUSH", "1")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	oldStore := store
	store = nil
	t.Cleanup(func() { store = oldStore })

	args := []string{existingPath}
	if err := rootCmd.PersistentPreRunE(worktreeCreateCmd, args); err != nil {
		t.Fatalf("worktree create PersistentPreRunE opened the configured absent store: %v", err)
	}
	if store != nil {
		t.Fatal("worktree create must not open the store")
	}
	if cmdCtx == nil {
		t.Fatal("worktree create must initialize the command context")
	}
	if cmdCtx.Store != nil {
		t.Fatal("worktree create must not attach a store to the command context")
	}
	if !serverMode {
		t.Fatal("test precondition broken: configured server-mode target was not loaded")
	}
	if err := worktreeCreateCmd.Args(worktreeCreateCmd, args); err != nil {
		t.Fatalf("worktree create args: %v", err)
	}
	err := worktreeCreateCmd.RunE(worktreeCreateCmd, args)
	wantErr := "path already exists: " + existingPath
	if err == nil || err.Error() != wantErr {
		t.Fatalf("worktree create error = %v, want %q", err, wantErr)
	}
}

// TestGetRedirectTarget tests that getRedirectTarget resolves redirect paths correctly.
// This is the fix for GH#1266: relative paths must be resolved from the worktree root
// (parent of .beads/), not from .beads/ itself, matching FollowRedirect behavior.
func TestGetRedirectTarget(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("relative path resolved from worktree root", func(t *testing.T) {
		worktreeDir := filepath.Join(tmpDir, "worktrees", "feat-branch")
		worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
		if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create worktree .beads dir: %v", err)
		}

		mainBeadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(mainBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create main .beads dir: %v", err)
		}

		redirectFile := filepath.Join(worktreeBeadsDir, "redirect")
		if err := os.WriteFile(redirectFile, []byte("../../.beads\n"), 0644); err != nil {
			t.Fatalf("failed to write redirect file: %v", err)
		}

		got := getRedirectTarget(worktreeDir)
		if got == "" {
			t.Fatal("getRedirectTarget returned empty string")
		}

		canonicalGot := utils.CanonicalizePath(got)
		canonicalExpected := utils.CanonicalizePath(mainBeadsDir)

		if canonicalGot != canonicalExpected {
			t.Errorf("getRedirectTarget() mismatch:\n  got:      %s\n  expected: %s", canonicalGot, canonicalExpected)
		}
	})

	t.Run("absolute path returned as-is", func(t *testing.T) {
		worktreeDir := filepath.Join(tmpDir, "worktrees", "abs-test")
		worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
		if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create worktree .beads dir: %v", err)
		}

		absTarget := filepath.Join(tmpDir, "abs-target-beads")
		if err := os.MkdirAll(absTarget, 0755); err != nil {
			t.Fatalf("failed to create abs target dir: %v", err)
		}

		redirectFile := filepath.Join(worktreeBeadsDir, "redirect")
		if err := os.WriteFile(redirectFile, []byte(absTarget+"\n"), 0644); err != nil {
			t.Fatalf("failed to write redirect file: %v", err)
		}

		got := getRedirectTarget(worktreeDir)
		canonicalGot := utils.CanonicalizePath(got)
		canonicalExpected := utils.CanonicalizePath(absTarget)

		if canonicalGot != canonicalExpected {
			t.Errorf("getRedirectTarget() mismatch for absolute path:\n  got:      %s\n  expected: %s", canonicalGot, canonicalExpected)
		}
	})

	t.Run("missing redirect file returns empty", func(t *testing.T) {
		worktreeDir := filepath.Join(tmpDir, "worktrees", "no-redirect")
		worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")
		if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
			t.Fatalf("failed to create worktree .beads dir: %v", err)
		}

		got := getRedirectTarget(worktreeDir)
		if got != "" {
			t.Errorf("expected empty string for missing redirect, got %q", got)
		}
	})
}

func TestAddToGitignore(t *testing.T) {
	t.Run("recognizes existing CRLF entries", func(t *testing.T) {
		tests := []struct {
			name    string
			initial string
			entry   string
		}{
			{name: "exact entry", initial: "worktree-feature/\r\n", entry: "worktree-feature"},
			{name: "parent pattern", initial: ".worktrees/\r\n", entry: ".worktrees/worktree-one"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				repoRoot := t.TempDir()
				gitignorePath := filepath.Join(repoRoot, ".gitignore")
				if err := os.WriteFile(gitignorePath, []byte(test.initial), 0644); err != nil {
					t.Fatalf("failed to write .gitignore: %v", err)
				}

				if err := addToGitignore(context.Background(), repoRoot, test.entry); err != nil {
					t.Fatalf("addToGitignore failed: %v", err)
				}

				updated, err := os.ReadFile(gitignorePath)
				if err != nil {
					t.Fatalf("failed to read .gitignore: %v", err)
				}
				if string(updated) != test.initial {
					t.Fatalf(".gitignore changed despite existing CRLF entry:\nwant: %q\ngot:  %q", test.initial, string(updated))
				}
			})
		}
	})

	t.Run("skips append when path already ignored by broader pattern", func(t *testing.T) {
		repoRoot := initGitRepoForGitignoreTest(t)
		gitignorePath := filepath.Join(repoRoot, ".gitignore")
		initial := ".worktrees/\n"
		if err := os.WriteFile(gitignorePath, []byte(initial), 0644); err != nil {
			t.Fatalf("failed to write .gitignore: %v", err)
		}

		if err := addToGitignore(context.Background(), repoRoot, ".worktrees/worktree-one"); err != nil {
			t.Fatalf("addToGitignore failed: %v", err)
		}

		updated, err := os.ReadFile(gitignorePath)
		if err != nil {
			t.Fatalf("failed to read .gitignore: %v", err)
		}

		if string(updated) != initial {
			t.Fatalf(".gitignore should be unchanged when entry is already ignored:\nwant:\n%s\ngot:\n%s", initial, string(updated))
		}
	})

	t.Run("appends exactly once when path is not ignored", func(t *testing.T) {
		repoRoot := initGitRepoForGitignoreTest(t)
		gitignorePath := filepath.Join(repoRoot, ".gitignore")
		if err := os.WriteFile(gitignorePath, []byte("node_modules/\n"), 0644); err != nil {
			t.Fatalf("failed to write .gitignore: %v", err)
		}

		entry := "worktree-feature"
		if err := addToGitignore(context.Background(), repoRoot, entry); err != nil {
			t.Fatalf("first addToGitignore failed: %v", err)
		}
		if err := addToGitignore(context.Background(), repoRoot, entry); err != nil {
			t.Fatalf("second addToGitignore failed: %v", err)
		}

		updated, err := os.ReadFile(gitignorePath)
		if err != nil {
			t.Fatalf("failed to read .gitignore: %v", err)
		}
		content := string(updated)

		if count := strings.Count(content, "# bd worktree"); count != 1 {
			t.Fatalf("expected one worktree marker, got %d:\n%s", count, content)
		}
		if count := strings.Count(content, entry+"/"); count != 1 {
			t.Fatalf("expected one worktree entry, got %d:\n%s", count, content)
		}
	})
}

func TestEnsureCreatedWorktreeCleanRejectsDirtyWorktree(t *testing.T) {
	repoRoot := newGitRepo(t)
	commitTestFile(t, repoRoot, "README.md", "# Test\n", "initial commit")

	worktreePath := filepath.Join(t.TempDir(), "dirty-worktree")
	cmd := exec.Command("git", "worktree", "add", worktreePath, "HEAD")
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create worktree: %v\n%s", err, output)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		cmd.Dir = repoRoot
		_ = cmd.Run()
	})

	if err := os.WriteFile(filepath.Join(worktreePath, "dirty.txt"), []byte("untracked\n"), 0644); err != nil {
		t.Fatalf("failed to dirty worktree: %v", err)
	}

	err := ensureCreatedWorktreeClean(context.Background(), worktreePath)
	if err == nil {
		t.Fatal("ensureCreatedWorktreeClean should reject a dirty worktree")
	}
	if !strings.Contains(err.Error(), "created worktree is dirty after checkout") {
		t.Fatalf("error should explain dirty post-create state, got: %v", err)
	}
	if !strings.Contains(err.Error(), "dirty.txt") {
		t.Fatalf("error should include porcelain status output, got: %v", err)
	}
}

func TestRunWorktreeCreateFailsWhenCreatedWorktreeIsDirty(t *testing.T) {
	repoRoot := newGitRepo(t)
	commitTestFile(t, repoRoot, "README.md", "# Test\n", "initial commit")
	if err := os.Mkdir(filepath.Join(repoRoot, ".beads"), 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".beads", "beads.db"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create beads db marker: %v", err)
	}

	beads.ResetCaches()
	internalgit.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		internalgit.ResetCaches()
	})
	t.Chdir(repoRoot)

	originalChecker := checkCreatedWorktreeClean
	t.Cleanup(func() {
		checkCreatedWorktreeClean = originalChecker
	})

	var checkedPath string
	checkCreatedWorktreeClean = func(_ context.Context, worktreePath string) error {
		checkedPath = worktreePath
		return fmt.Errorf("created worktree is dirty after checkout; refusing to continue: %s\n?? dirty.txt", worktreePath)
	}

	worktreeBranch = ""
	t.Cleanup(func() {
		worktreeBranch = ""
	})

	err := runWorktreeCreate(worktreeCreateCmd, []string{"dirty-created"})
	if err == nil {
		t.Fatal("runWorktreeCreate should fail when post-create cleanliness check fails")
	}

	wantPath := filepath.Join(repoRoot, "dirty-created")
	if checkedPath != wantPath {
		t.Fatalf("cleanliness check path = %q, want %q", checkedPath, wantPath)
	}
	if !strings.Contains(err.Error(), "dirty.txt") {
		t.Fatalf("error should preserve dirty status details, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, ".gitignore")); !os.IsNotExist(statErr) {
		t.Fatalf("runWorktreeCreate should fail before mutating .gitignore, stat err: %v", statErr)
	}
}

func initGitRepoForGitignoreTest(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init git repo: %v\n%s", err, string(output))
	}

	return repoRoot
}

func commitTestFile(t *testing.T, repoRoot, relPath, content, message string) {
	t.Helper()
	path := filepath.Join(repoRoot, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create parent directory for %s: %v", relPath, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", relPath, err)
	}
	cmd := exec.Command("git", "add", relPath)
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add %s failed: %v\n%s", relPath, err, output)
	}
	cmd = exec.Command("git", "commit", "-m", message)
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, output)
	}
}
