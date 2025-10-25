package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

// initializeNoDbMode sets up in-memory storage from JSONL file
// This is called when --no-db flag is set
func initializeNoDbMode() error {
	// Find .beads directory
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	beadsDir := filepath.Join(cwd, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return fmt.Errorf("no .beads directory found (hint: run 'bd init' first)")
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

		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: loaded %d issues from %s\n", len(issues), jsonlPath)
		}
	} else {
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: no existing %s, starting with empty database\n", jsonlPath)
		}
	}

	// Detect and set prefix
	prefix, err := detectPrefix(beadsDir, memStore)
	if err != nil {
		return fmt.Errorf("failed to detect prefix: %w", err)
	}

	ctx := context.Background()
	if err := memStore.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		return fmt.Errorf("failed to set prefix: %w", err)
	}

	if os.Getenv("BD_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "Debug: using prefix '%s'\n", prefix)
	}

	// Set global store
	store = memStore
	return nil
}

// loadIssuesFromJSONL reads all issues from a JSONL file
func loadIssuesFromJSONL(path string) ([]*types.Issue, error) {
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

		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return issues, nil
}

// detectPrefix detects the issue prefix to use in --no-db mode
// Priority:
// 1. .beads/nodb_prefix.txt file (if exists)
// 2. Common prefix from existing issues (if all share same prefix)
// 3. Current directory name (fallback)
func detectPrefix(beadsDir string, memStore *memory.MemoryStorage) (string, error) {
	// Check for nodb_prefix.txt
	prefixFile := filepath.Join(beadsDir, "nodb_prefix.txt")
	if data, err := os.ReadFile(prefixFile); err == nil {
		prefix := strings.TrimSpace(string(data))
		if prefix != "" {
			return prefix, nil
		}
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
			return "", fmt.Errorf("issues have mixed prefixes, please create .beads/nodb_prefix.txt with the desired prefix")
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
func extractIssuePrefix(issueID string) string {
	parts := strings.SplitN(issueID, "-", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// writeIssuesToJSONL writes all issues from memory storage to JSONL file atomically
func writeIssuesToJSONL(memStore *memory.MemoryStorage, beadsDir string) error {
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Get all issues from memory storage
	issues := memStore.GetAllIssues()

	// Write atomically using common helper (handles temp file + rename + permissions)
	if err := writeJSONLAtomic(jsonlPath, issues); err != nil {
		return err
	}

	if os.Getenv("BD_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "Debug: wrote %d issues to %s\n", len(issues), jsonlPath)
	}

	return nil
}
