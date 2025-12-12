package reset

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitState represents the current state of the git repository
type GitState struct {
	IsRepo     bool   // Is this a git repository?
	IsDirty    bool   // Are there uncommitted changes?
	IsDetached bool   // Is HEAD detached?
	Branch     string // Current branch name (empty if detached)
}

// CheckGitState detects the current git repository state
func CheckGitState(beadsDir string) (*GitState, error) {
	state := &GitState{}

	// Check if we're in a git repository
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		// Not a git repo - this is OK, we'll skip git operations gracefully
		state.IsRepo = false
		return state, nil
	}
	state.IsRepo = true

	// Check if there are uncommitted changes specifically in .beads/
	// (not the entire repo, just the beads directory)
	cmd = exec.Command("git", "status", "--porcelain", "--", beadsDir)
	var statusOut bytes.Buffer
	cmd.Stdout = &statusOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to check git status: %w", err)
	}
	state.IsDirty = len(strings.TrimSpace(statusOut.String())) > 0

	// Check if HEAD is detached and get current branch
	cmd = exec.Command("git", "symbolic-ref", "-q", "HEAD")
	var branchOut bytes.Buffer
	cmd.Stdout = &branchOut
	err := cmd.Run()

	if err != nil {
		// symbolic-ref fails on detached HEAD
		state.IsDetached = true
		state.Branch = ""
	} else {
		state.IsDetached = false
		// Extract branch name from refs/heads/branch-name
		fullRef := strings.TrimSpace(branchOut.String())
		state.Branch = strings.TrimPrefix(fullRef, "refs/heads/")
	}

	return state, nil
}

// GitRemoveBeads uses git rm to remove the JSONL files from the index
// This prepares for a reset by staging the removal of beads files
func GitRemoveBeads(beadsDir string) error {
	// Find all JSONL files in the beads directory
	// We support both canonical (issues.jsonl) and legacy (beads.jsonl) names
	jsonlFiles := []string{
		filepath.Join(beadsDir, "issues.jsonl"),
		filepath.Join(beadsDir, "beads.jsonl"),
	}

	// Try to remove each file (git rm ignores non-existent files with --ignore-unmatch)
	for _, file := range jsonlFiles {
		cmd := exec.Command("git", "rm", "--ignore-unmatch", "--quiet", file)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to git rm %s: %w\nstderr: %s", file, err, stderr.String())
		}
	}

	return nil
}

// GitCommitReset creates a commit with the removal of beads files
// Returns nil without error if there's nothing to commit
func GitCommitReset(message string) error {
	// First check if there are any staged changes
	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	if err := cmd.Run(); err == nil {
		// Exit code 0 means no staged changes - nothing to commit
		return nil
	}
	// Exit code 1 means there are staged changes - proceed with commit

	cmd = exec.Command("git", "commit", "-m", message)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to commit reset: %w\nstderr: %s", err, stderr.String())
	}

	return nil
}

// GitAddAndCommit stages the beads directory and creates a commit with fresh state
func GitAddAndCommit(beadsDir, message string) error {
	// Add the entire beads directory (this will pick up the fresh JSONL)
	cmd := exec.Command("git", "add", beadsDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to git add %s: %w\nstderr: %s", beadsDir, err, stderr.String())
	}

	// Create the commit
	cmd = exec.Command("git", "commit", "-m", message)
	stderr.Reset()
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to commit fresh state: %w\nstderr: %s", err, stderr.String())
	}

	return nil
}
