package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// checkAndAutoImport checks if the database is empty but git has issues.
// If so, it automatically imports them and returns true.
// Returns false if no import was needed or if import failed.
func checkAndAutoImport(ctx context.Context, store storage.Storage) bool {
	// Don't auto-import if auto-import is explicitly disabled
	if noAutoImport {
		return false
	}

	// Check if database has any issues
	stats, err := store.GetStatistics(ctx)
	if err != nil || stats.TotalIssues > 0 {
		// Either error checking or DB has issues - don't auto-import
		return false
	}

	// Database is empty - check if git has issues
	issueCount, jsonlPath := checkGitForIssues()
	if issueCount == 0 {
		// No issues in git either
		return false
	}

	// Found issues in git! Auto-import them
	if !jsonOutput {
		fmt.Fprintf(os.Stderr, "Found 0 issues in database but %d in git. Importing...\n", issueCount)
	}

	// Import from git
	if err := importFromGit(ctx, store, jsonlPath); err != nil {
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: auto-import failed: %v\n", err)
			fmt.Fprintf(os.Stderr, "Try manually: git show HEAD:%s | bd import -i /dev/stdin\n", jsonlPath)
		}
		return false
	}

	if !jsonOutput {
		fmt.Fprintf(os.Stderr, "Successfully imported %d issues from git.\n\n", issueCount)
	}

	return true
}

// checkGitForIssues checks if git has issues in HEAD:.beads/issues.jsonl
// Returns (issue_count, relative_jsonl_path)
func checkGitForIssues() (int, string) {
	// Try to find .beads directory
	beadsDir := findBeadsDir()
	if beadsDir == "" {
		return 0, ""
	}

	// Construct relative path to issues.jsonl from git root
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return 0, ""
	}

	relPath, err := filepath.Rel(gitRoot, filepath.Join(beadsDir, "issues.jsonl"))
	if err != nil {
		return 0, ""
	}

	// Check if git has this file with content
	cmd := exec.Command("git", "show", fmt.Sprintf("HEAD:%s", relPath))
	output, err := cmd.Output()
	if err != nil {
		// File doesn't exist in git or other error
		return 0, ""
	}

	// Count lines (rough estimate of issue count)
	lines := bytes.Count(output, []byte("\n"))
	if lines == 0 {
		return 0, ""
	}

	return lines, relPath
}

// findBeadsDir finds the .beads directory in current or parent directories
func findBeadsDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			return beadsDir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root
			break
		}
		dir = parent
	}

	return ""
}

// findGitRoot finds the git repository root
func findGitRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(output))
}

// importFromGit imports issues from git HEAD
func importFromGit(ctx context.Context, store storage.Storage, jsonlPath string) error {
	// Get content from git
	cmd := exec.Command("git", "show", fmt.Sprintf("HEAD:%s", jsonlPath))
	jsonlData, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to read from git: %w", err)
	}

	// Parse JSONL data
	scanner := bufio.NewScanner(bytes.NewReader(jsonlData))
	var issues []*types.Issue

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return fmt.Errorf("failed to parse issue: %w", err)
		}
		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to scan JSONL: %w", err)
	}

	// Use existing import logic with auto-resolve collisions
	opts := ImportOptions{
		ResolveCollisions:  true,
		DryRun:             false,
		SkipUpdate:         false,
		SkipPrefixValidation: true, // Auto-import is lenient about prefixes
	}

	_, err = importIssuesCore(ctx, dbPath, store, issues, opts)
	return err
}
