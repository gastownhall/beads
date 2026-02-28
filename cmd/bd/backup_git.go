package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
)

// backupGitDir returns the git working directory for backup operations.
// When backup.git-repo is set, returns that directory.
// Otherwise returns "" (git commands run in the project repo).
func backupGitDir() string {
	gitRepo := config.GetString("backup.git-repo")
	if gitRepo == "" {
		return ""
	}
	if strings.HasPrefix(gitRepo, "~/") {
		home, _ := os.UserHomeDir()
		gitRepo = filepath.Join(home, gitRepo[2:])
	}
	if _, err := os.Stat(filepath.Join(gitRepo, ".git")); err != nil {
		return ""
	}
	return gitRepo
}

// gitBackup adds, commits, and pushes the backup directory to git.
// Failures are logged as warnings, never fatal — git push is best-effort.
func gitBackup(ctx context.Context) error {
	dir, err := backupDir()
	if err != nil {
		return err
	}

	gitDir := backupGitDir()

	// When no dedicated backup.git-repo is configured, git commands must run
	// from the repository that contains the backup directory. This matters when
	// .beads/redirect points to a different project — the backup dir lives in
	// that project's repo, not the CWD's repo.
	if gitDir == "" {
		// Find the git repo root for the backup directory
		repoRoot, findErr := findGitRoot(dir)
		if findErr != nil {
			debug.Logf("backup: cannot find git repo for %s: %v\n", dir, findErr)
			return fmt.Errorf("git add: backup directory %s is not inside a git repository", dir)
		}
		gitDir = repoRoot
	}

	// git add -f backup/ (force-add past .gitignore)
	if err := gitExecInDir(ctx, gitDir, "add", "-f", dir); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// Check if there's anything to commit
	diffCmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet", "--", dir)
	if gitDir != "" {
		diffCmd.Dir = gitDir
	}
	out, err := diffCmd.CombinedOutput()
	if err == nil {
		debug.Logf("backup: no git changes to commit\n")
		return nil // nothing staged
	}
	// exit code 1 = there are differences (good, we want to commit)
	_ = out

	// git commit
	msg := fmt.Sprintf("bd: backup %s", time.Now().UTC().Format("2006-01-02 15:04"))
	if err := gitExecInDir(ctx, gitDir, "commit", "-m", msg, "--", dir); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	// git push with timeout (failure = warning only)
	pushCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := gitExecInDir(pushCtx, gitDir, "push"); err != nil {
		debug.Logf("backup: git push failed (non-fatal): %v\n", err)
		fmt.Fprintf(os.Stderr, "Warning: backup git push failed: %v\n", err)
		return nil // non-fatal
	}

	return nil
}

// findGitRoot finds the git repository root containing the given path.
func findGitRoot(path string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = path
	// If path is a file, use its parent directory
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		cmd.Dir = filepath.Dir(path)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitExec runs a git command in the current directory and returns any error.
func gitExec(ctx context.Context, args ...string) error {
	return gitExecInDir(ctx, "", args...)
}

// gitExecInDir runs a git command in the specified directory (or current dir if empty).
func gitExecInDir(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, out)
	}
	return nil
}
