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
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
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
	if err := importFromGit(ctx, dbPath, store, jsonlPath); err != nil {
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

// checkGitForIssues checks if git has issues in HEAD:.beads/beads.jsonl or issues.jsonl
// Returns (issue_count, relative_jsonl_path)
func checkGitForIssues() (int, string) {
	// Try to find .beads directory
	beadsDir := findBeadsDir()
	if beadsDir == "" {
		return 0, ""
	}

	// Construct relative path from git root
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return 0, ""
	}

	relBeads, err := filepath.Rel(gitRoot, beadsDir)
	if err != nil {
		return 0, ""
	}

	// Try canonical JSONL filenames in precedence order
	candidates := []string{
		filepath.Join(relBeads, "beads.jsonl"),
		filepath.Join(relBeads, "issues.jsonl"),
	}

	for _, relPath := range candidates {
		// Use ToSlash for git path compatibility on Windows
		gitPath := filepath.ToSlash(relPath)
		cmd := exec.Command("git", "show", fmt.Sprintf("HEAD:%s", gitPath)) // #nosec G204 - git command with safe args
		output, err := cmd.Output()
		if err == nil && len(output) > 0 {
			lines := bytes.Count(output, []byte("\n"))
			if lines > 0 {
				return lines, relPath
			}
		}
	}

	return 0, ""
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
func importFromGit(ctx context.Context, dbFilePath string, store storage.Storage, jsonlPath string) error {
	// Get content from git (use ToSlash for Windows compatibility)
	gitPath := filepath.ToSlash(jsonlPath)
	cmd := exec.Command("git", "show", fmt.Sprintf("HEAD:%s", gitPath)) // #nosec G204 - git command with safe args
	jsonlData, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to read from git: %w", err)
	}

	// Parse JSONL data
	scanner := bufio.NewScanner(bytes.NewReader(jsonlData))
	// Increase buffer size to handle large JSONL lines (e.g., big descriptions)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024) // allow up to 64MB per line
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

	// CRITICAL (bd-166): Set issue_prefix from first imported issue if missing
	// This prevents derivePrefixFromPath fallback which caused duplicate issues
	if len(issues) > 0 {
		configuredPrefix, err := store.GetConfig(ctx, "issue_prefix")
		if err == nil && strings.TrimSpace(configuredPrefix) == "" {
			// Database has no prefix configured - derive from first issue
			firstPrefix := utils.ExtractIssuePrefix(issues[0].ID)
			if firstPrefix != "" {
				if err := store.SetConfig(ctx, "issue_prefix", firstPrefix); err != nil {
					return fmt.Errorf("failed to set issue_prefix from imported issues: %w", err)
				}
			}
		}
	}

	// Use existing import logic with auto-resolve collisions
	// Note: SkipPrefixValidation allows mixed prefixes during auto-import
	// (but now we set the prefix first, so CreateIssue won't use filename fallback)
	opts := ImportOptions{
		ResolveCollisions:  true,
		DryRun:             false,
		SkipUpdate:         false,
		SkipPrefixValidation: true, // Auto-import is lenient about prefixes
	}

	_, err = importIssuesCore(ctx, dbFilePath, store, issues, opts)
	return err
}
