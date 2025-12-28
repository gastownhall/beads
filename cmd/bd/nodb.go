package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// initializeNoDbMode sets up in-memory storage from JSONL file
// This is called when --no-db flag is set
func initializeNoDbMode() error {
	// Find .beads directory
	var beadsDir string

	// Check BEADS_DIR environment variable first
	if envDir := os.Getenv("BEADS_DIR"); envDir != "" {
		// Canonicalize the path
		beadsDir = utils.CanonicalizePath(envDir)
	} else {
		// Fall back to current directory
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		beadsDir = filepath.Join(cwd, ".beads")
	}

	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return fmt.Errorf("no .beads directory found (hint: run 'bd init' first or set BEADS_DIR)")
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Create memory storage
	memStore := memory.New(jsonlPath)

	// Try to load from JSONL if it exists
	if _, err := os.Stat(jsonlPath); err == nil {
		issues, err := loadIssuesFromJSONL(jsonlPath)
		if err != nil {
			return fmt.Errorf("failed to load issues from %s: %w", jsonlPath, err)
		}

		if err := memStore.LoadFromIssues(issues); err != nil {
			return fmt.Errorf("failed to load issues into memory: %w", err)
		}

		debug.Logf("loaded %d issues from %s", len(issues), jsonlPath)
	} else {
		debug.Logf("no existing %s, starting with empty database", jsonlPath)
	}

	// Detect and set prefix
	prefix, err := detectPrefix(beadsDir, memStore)
	if err != nil {
		return fmt.Errorf("failed to detect prefix: %w", err)
	}

	ctx := rootCtx
	if err := memStore.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		return fmt.Errorf("failed to set prefix: %w", err)
	}

	debug.Logf("using prefix '%s'", prefix)

	// Set global store and mark as active (fixes bd comment --no-db)
	storeMutex.Lock()
	store = memStore
	storeActive = true
	storeMutex.Unlock()
	return nil
}

// loadIssuesFromJSONL reads all issues from a JSONL file
func loadIssuesFromJSONL(path string) ([]*types.Issue, error) {
	// nolint:gosec // G304: path is validated JSONL file from findJSONLPath
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var issues []*types.Issue
	scanner := bufio.NewScanner(file)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip empty lines
		if strings.TrimSpace(line) == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
		issue.SetDefaults() // Apply defaults for omitted fields (beads-399)

		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return issues, nil
}

// detectPrefix detects the issue prefix to use in --no-db mode
// Priority:
// 1. issue-prefix from config.yaml (if set)
// 2. Common prefix from existing issues (if all share same prefix)
// 3. Current directory name (fallback)
func detectPrefix(_ string, memStore *memory.MemoryStorage) (string, error) {
	// Check config.yaml for issue-prefix
	configPrefix := config.GetString("issue-prefix")
	if configPrefix != "" {
		return configPrefix, nil
	}

	// Check existing issues for common prefix
	issues := memStore.GetAllIssues()
	if len(issues) > 0 {
		// Extract prefix from first issue
		firstPrefix := extractIssuePrefix(issues[0].ID)

		// Check if all issues share the same prefix
		allSame := true
		for _, issue := range issues {
			if extractIssuePrefix(issue.ID) != firstPrefix {
				allSame = false
				break
			}
		}

		if allSame && firstPrefix != "" {
			return firstPrefix, nil
		}

		// If issues have mixed prefixes, we can't auto-detect
		if !allSame {
			return "", fmt.Errorf("issues have mixed prefixes, please set issue-prefix in .beads/config.yaml")
		}
	}

	// Fallback to directory name
	cwd, err := os.Getwd()
	if err != nil {
		return "bd", nil // Ultimate fallback
	}

	prefix := filepath.Base(cwd)
	// Sanitize prefix (remove special characters, use only alphanumeric and hyphens)
	prefix = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A') // Convert to lowercase
		}
		return -1 // Remove character
	}, prefix)

	if prefix == "" {
		prefix = "bd"
	}

	return prefix, nil
}

// extractIssuePrefix extracts the prefix from an issue ID like "bd-123" -> "bd"
// Uses the last hyphen before a numeric suffix, so "beads-vscode-1" -> "beads-vscode"
func extractIssuePrefix(issueID string) string {
	// Try last hyphen first (handles multi-part prefixes like "beads-vscode-1")
	lastIdx := strings.LastIndex(issueID, "-")
	if lastIdx <= 0 {
		return ""
	}

	suffix := issueID[lastIdx+1:]
	// Check if suffix is numeric
	if len(suffix) > 0 {
		numPart := suffix
		if dotIdx := strings.Index(suffix, "."); dotIdx > 0 {
			numPart = suffix[:dotIdx]
		}
		var num int
		if _, err := fmt.Sscanf(numPart, "%d", &num); err == nil {
			return issueID[:lastIdx]
		}
	}

	// Suffix is not numeric, fall back to first hyphen
	firstIdx := strings.Index(issueID, "-")
	if firstIdx <= 0 {
		return ""
	}
	return issueID[:firstIdx]
}

// writeIssuesToJSONL writes all issues from memory storage to JSONL file atomically
func writeIssuesToJSONL(memStore *memory.MemoryStorage, beadsDir string) error {
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Get all issues from memory storage
	issues := memStore.GetAllIssues()

	// Filter out wisps - they should never be exported to JSONL
	// Wisps exist only in SQLite and are shared via .beads/redirect, not JSONL.
	filtered := make([]*types.Issue, 0, len(issues))
	for _, issue := range issues {
		if !issue.Ephemeral {
			filtered = append(filtered, issue)
		}
	}
	issues = filtered

	// Write atomically using common helper (handles temp file + rename + permissions)
	if _, err := writeJSONLAtomic(jsonlPath, issues); err != nil {
		return err
	}

	debug.Logf("wrote %d issues to %s", len(issues), jsonlPath)

	return nil
}
