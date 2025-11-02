package daemonrunner

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GitClient provides an interface for git operations to enable testing
type GitClient interface {
	HasUpstream() bool
	HasChanges(ctx context.Context, filePath string) (bool, error)
	Commit(ctx context.Context, filePath string, message string) error
	Push(ctx context.Context) error
	Pull(ctx context.Context) error
}

// DefaultGitClient implements GitClient using os/exec
type DefaultGitClient struct{}

// NewGitClient creates a new default git client
func NewGitClient() GitClient {
	return &DefaultGitClient{}
}

// HasUpstream checks if the current branch has an upstream configured
func (g *DefaultGitClient) HasUpstream() bool {
	return gitHasUpstream()
}

// HasChanges checks if the specified file has uncommitted changes
func (g *DefaultGitClient) HasChanges(ctx context.Context, filePath string) (bool, error) {
	return gitHasChanges(ctx, filePath)
}

// Commit commits the specified file
func (g *DefaultGitClient) Commit(ctx context.Context, filePath string, message string) error {
	return gitCommit(ctx, filePath, message)
}

// Push pushes to the current branch's upstream
func (g *DefaultGitClient) Push(ctx context.Context) error {
	return gitPush(ctx)
}

// Pull pulls from the current branch's upstream
func (g *DefaultGitClient) Pull(ctx context.Context) error {
	return gitPull(ctx)
}

// isGitRepo checks if we're in a git repository
func isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

// gitHasUpstream checks if the current branch has an upstream configured
func gitHasUpstream() bool {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return cmd.Run() == nil
}

// gitHasChanges checks if the specified file has uncommitted changes
func gitHasChanges(ctx context.Context, filePath string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", filePath)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// gitCommit commits the specified file
func gitCommit(ctx context.Context, filePath string, message string) error {
	// Stage the file
	addCmd := exec.CommandContext(ctx, "git", "add", filePath)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	// Generate message if not provided
	if message == "" {
		message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	}

	// Commit
	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed: %w\n%s", err, output)
	}

	return nil
}

// gitPull pulls from the current branch's upstream
func gitPull(ctx context.Context) error {
	// Get current branch name
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}
	branch := strings.TrimSpace(string(branchOutput))

	// Get remote name for current branch (usually "origin")
	remoteCmd := exec.CommandContext(ctx, "git", "config", "--get", fmt.Sprintf("branch.%s.remote", branch))
	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		// If no remote configured, default to "origin"
		remoteOutput = []byte("origin\n")
	}
	remote := strings.TrimSpace(string(remoteOutput))

	// Pull with explicit remote and branch
	cmd := exec.CommandContext(ctx, "git", "pull", remote, branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git pull failed: %w\n%s", err, output)
	}
	return nil
}

// gitPush pushes to the current branch's upstream
func gitPush(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "git", "push")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push failed: %w\n%s", err, output)
	}
	return nil
}
