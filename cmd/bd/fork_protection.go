package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/debug"
)

// ensureForkProtection prevents contributors from accidentally committing
// the upstream issue database when working in a fork.
//
// When we detect this is a fork (origin != steveyegge/beads), we add
// .beads/issues.jsonl to .git/info/exclude so it won't be staged.
// This is a per-clone setting that doesn't modify tracked files.
func ensureForkProtection() {
	// Find git root (reuses existing findGitRoot from autoimport.go)
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return // Not in a git repo
	}

	// Check if this is the upstream repo (maintainers)
	if isUpstreamRepo(gitRoot) {
		return // Maintainers can commit issues.jsonl
	}

	// Check if already excluded
	excludePath := filepath.Join(gitRoot, ".git", "info", "exclude")
	if isAlreadyExcluded(excludePath) {
		return
	}

	// Add to .git/info/exclude
	if err := addToExclude(excludePath); err != nil {
		debug.Printf("fork protection: failed to update exclude: %v", err)
		return
	}

	debug.Printf("Fork detected: .beads/issues.jsonl excluded from git staging")
}

// isUpstreamRepo checks if origin remote points to the upstream beads repo
func isUpstreamRepo(gitRoot string) bool {
	cmd := exec.Command("git", "-C", gitRoot, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return false // Can't determine, assume fork for safety
	}

	remote := strings.TrimSpace(string(out))

	// Check for upstream repo patterns
	upstreamPatterns := []string{
		"steveyegge/beads",
		"git@github.com:steveyegge/beads",
		"https://github.com/steveyegge/beads",
	}

	for _, pattern := range upstreamPatterns {
		if strings.Contains(remote, pattern) {
			return true
		}
	}

	return false
}

// isAlreadyExcluded checks if issues.jsonl is already in the exclude file
func isAlreadyExcluded(excludePath string) bool {
	content, err := os.ReadFile(excludePath) //nolint:gosec // G304: path is constructed from git root, not user input
	if err != nil {
		return false // File doesn't exist or can't read, not excluded
	}

	return strings.Contains(string(content), ".beads/issues.jsonl")
}

// addToExclude adds the issues.jsonl pattern to .git/info/exclude
func addToExclude(excludePath string) error {
	// Ensure the directory exists
	dir := filepath.Dir(excludePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Open for append (create if doesn't exist)
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec // G302: .git/info/exclude should be world-readable
	if err != nil {
		return err
	}
	defer f.Close()

	// Add our exclusion with a comment
	_, err = f.WriteString("\n# Beads: prevent fork from committing upstream issue database\n.beads/issues.jsonl\n")
	return err
}
