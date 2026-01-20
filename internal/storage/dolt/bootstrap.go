//go:build cgo

package dolt

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// BootstrapResult contains statistics about the bootstrap operation
type BootstrapResult struct {
	IssuesImported int
	IssuesSkipped  int
	ParseErrors    []ParseError
	PrefixDetected string
}

// ParseError describes a JSONL parsing error
type ParseError struct {
	Line    int
	Message string
	Snippet string
}

// BootstrapConfig controls bootstrap behavior
type BootstrapConfig struct {
	BeadsDir    string        // Path to .beads directory
	DoltPath    string        // Path to dolt subdirectory
	LockTimeout time.Duration // Timeout waiting for bootstrap lock
}

// Bootstrap checks if Dolt DB needs bootstrapping from JSONL and performs it if needed.
// This is called during store creation to handle the cold-start scenario where
// JSONL files exist (from git clone) but no Dolt database exists yet.
//
// Returns:
//   - true, result, nil: Bootstrap was performed successfully
//   - false, nil, nil: No bootstrap needed (Dolt already exists or no JSONL)
//   - false, nil, err: Bootstrap failed
func Bootstrap(ctx context.Context, cfg BootstrapConfig) (bool, *BootstrapResult, error) {
	if cfg.LockTimeout == 0 {
		cfg.LockTimeout = 30 * time.Second
	}

	// Check if Dolt database already exists and is ready
	if doltExists(cfg.DoltPath) && schemaReady(ctx, cfg.DoltPath) {
		return false, nil, nil
	}

	// Check if JSONL exists to bootstrap from
	jsonlPath := findJSONLPath(cfg.BeadsDir)
	if jsonlPath == "" {
		// No JSONL to bootstrap from - let normal init handle it
		return false, nil, nil
	}

	// Acquire bootstrap lock to prevent concurrent bootstraps
	lockPath := cfg.DoltPath + ".bootstrap.lock"
	lockFile, err := acquireBootstrapLock(lockPath, cfg.LockTimeout)
	if err != nil {
		return false, nil, fmt.Errorf("bootstrap lock timeout: %w", err)
	}
	defer releaseBootstrapLock(lockFile, lockPath)

	// Double-check after acquiring lock - another process may have bootstrapped
	if doltExists(cfg.DoltPath) && schemaReady(ctx, cfg.DoltPath) {
		return false, nil, nil
	}

	// Perform bootstrap
	result, err := performBootstrap(ctx, cfg, jsonlPath)
	if err != nil {
		return false, nil, err
	}

	return true, result, nil
}

// doltExists checks if a Dolt database directory exists
func doltExists(doltPath string) bool {
	// The embedded Dolt driver creates the database in a subdirectory
	// named after the database (default: "beads"), with .dolt inside that.
	// So we check for any subdirectory containing a .dolt directory.
	entries, err := os.ReadDir(doltPath)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if entry.IsDir() {
			doltDir := filepath.Join(doltPath, entry.Name(), ".dolt")
			if info, err := os.Stat(doltDir); err == nil && info.IsDir() {
				return true
			}
		}
	}
	return false
}

// schemaReady checks if the Dolt database has the required schema
// This is a simple check based on the existence of expected files.
// We avoid opening a connection here since the caller will do that.
func schemaReady(_ context.Context, doltPath string) bool {
	// The embedded Dolt driver stores databases in subdirectories.
	// Check for the expected database name's config.json which indicates
	// the database was initialized.
	configPath := filepath.Join(doltPath, "beads", ".dolt", "config.json")
	_, err := os.Stat(configPath)
	return err == nil
}

// findJSONLPath looks for JSONL files in the beads directory
func findJSONLPath(beadsDir string) string {
	// Check in order of preference
	candidates := []string{
		filepath.Join(beadsDir, "issues.jsonl"),
		filepath.Join(beadsDir, "beads.jsonl"), // Legacy name
	}

	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}

	return ""
}

// acquireBootstrapLock acquires an exclusive lock for bootstrap operations
func acquireBootstrapLock(lockPath string, timeout time.Duration) (*os.File, error) {
	// Create lock file
	// #nosec G304 - controlled path
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to create lock file: %w", err)
	}

	// Try to acquire lock with timeout
	deadline := time.Now().Add(timeout)
	for {
		err := lockfile.FlockExclusiveBlocking(f)
		if err == nil {
			// Lock acquired
			return f, nil
		}

		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("timeout waiting for bootstrap lock")
		}

		// Wait briefly before retrying
		time.Sleep(100 * time.Millisecond)
	}
}

// releaseBootstrapLock releases the bootstrap lock and removes the lock file
func releaseBootstrapLock(f *os.File, lockPath string) {
	if f != nil {
		_ = lockfile.FlockUnlock(f)
		_ = f.Close()
	}
	// Clean up lock file
	_ = os.Remove(lockPath)
}

// performBootstrap performs the actual bootstrap from JSONL
func performBootstrap(ctx context.Context, cfg BootstrapConfig, jsonlPath string) (*BootstrapResult, error) {
	result := &BootstrapResult{}

	// Parse JSONL with graceful error handling
	issues, parseErrors := parseJSONLWithErrors(jsonlPath)
	result.ParseErrors = parseErrors

	if len(parseErrors) > 0 {
		fmt.Fprintf(os.Stderr, "Bootstrap: Skipped %d malformed lines during bootstrap:\n", len(parseErrors))
		maxShow := 5
		if len(parseErrors) < maxShow {
			maxShow = len(parseErrors)
		}
		for i := 0; i < maxShow; i++ {
			e := parseErrors[i]
			fmt.Fprintf(os.Stderr, "  Line %d: %s\n", e.Line, e.Message)
		}
		if len(parseErrors) > maxShow {
			fmt.Fprintf(os.Stderr, "  ... and %d more\n", len(parseErrors)-maxShow)
		}
	}

	if len(issues) == 0 {
		return nil, fmt.Errorf("no valid issues found in JSONL file %s", jsonlPath)
	}

	// Detect prefix from issues
	result.PrefixDetected = detectPrefixFromIssues(issues)

	// Create Dolt store (this initializes schema)
	store, err := New(ctx, &Config{Path: cfg.DoltPath})
	if err != nil {
		return nil, fmt.Errorf("failed to create Dolt store: %w", err)
	}
	defer func() { _ = store.Close() }()

	// Set issue prefix
	if result.PrefixDetected != "" {
		if err := store.SetConfig(ctx, "issue_prefix", result.PrefixDetected); err != nil {
			return nil, fmt.Errorf("failed to set issue_prefix: %w", err)
		}
	}

	// Import issues in a transaction
	imported, skipped, err := importIssuesBootstrap(ctx, store, issues)
	if err != nil {
		return nil, fmt.Errorf("failed to import issues: %w", err)
	}

	result.IssuesImported = imported
	result.IssuesSkipped = skipped

	// Commit the bootstrap
	if err := store.Commit(ctx, "Bootstrap from JSONL"); err != nil {
		// Non-fatal - data is still in the database
		fmt.Fprintf(os.Stderr, "Bootstrap: warning: failed to create Dolt commit: %v\n", err)
	}

	return result, nil
}

// parseJSONLWithErrors parses JSONL, collecting errors instead of failing
func parseJSONLWithErrors(jsonlPath string) ([]*types.Issue, []ParseError) {
	// #nosec G304 - controlled path
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil, []ParseError{{Line: 0, Message: fmt.Sprintf("failed to open file: %v", err)}}
	}
	defer func() { _ = f.Close() }()

	var issues []*types.Issue
	var parseErrors []ParseError

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024), 2*1024*1024) // 2MB buffer for large lines
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Skip Git merge conflict markers
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<<<<<<< ") ||
			trimmed == "=======" ||
			strings.HasPrefix(trimmed, ">>>>>>> ") {
			parseErrors = append(parseErrors, ParseError{
				Line:    lineNo,
				Message: "Git merge conflict marker",
				Snippet: truncateSnippet(line, 50),
			})
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			parseErrors = append(parseErrors, ParseError{
				Line:    lineNo,
				Message: err.Error(),
				Snippet: truncateSnippet(line, 50),
			})
			continue
		}

		// Apply defaults for omitted fields
		issue.SetDefaults()

		// Fix closed_at invariant
		if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
			now := time.Now()
			issue.ClosedAt = &now
		}

		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		parseErrors = append(parseErrors, ParseError{
			Line:    lineNo,
			Message: fmt.Sprintf("scanner error: %v", err),
		})
	}

	return issues, parseErrors
}

// truncateSnippet truncates a string for display
func truncateSnippet(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// detectPrefixFromIssues detects the most common prefix from issues
func detectPrefixFromIssues(issues []*types.Issue) string {
	prefixCounts := make(map[string]int)

	for _, issue := range issues {
		if issue.ID == "" {
			continue
		}
		prefix := utils.ExtractIssuePrefix(issue.ID)
		if prefix != "" {
			prefixCounts[prefix]++
		}
	}

	// Find most common prefix
	var maxPrefix string
	var maxCount int
	for prefix, count := range prefixCounts {
		if count > maxCount {
			maxPrefix = prefix
			maxCount = count
		}
	}

	return maxPrefix
}

// importIssuesBootstrap imports issues during bootstrap
// Returns (imported, skipped, error)
func importIssuesBootstrap(ctx context.Context, store *DoltStore, issues []*types.Issue) (int, int, error) {
	// Skip validation during bootstrap since we're importing existing data
	// The data was already validated when originally created

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	imported := 0
	skipped := 0
	seenIDs := make(map[string]bool)

	for _, issue := range issues {
		// Skip duplicates within batch
		if seenIDs[issue.ID] {
			skipped++
			continue
		}
		seenIDs[issue.ID] = true

		// Set timestamps if missing
		now := time.Now().UTC()
		if issue.CreatedAt.IsZero() {
			issue.CreatedAt = now
		}
		if issue.UpdatedAt.IsZero() {
			issue.UpdatedAt = now
		}

		// Compute content hash if missing
		if issue.ContentHash == "" {
			issue.ContentHash = issue.ComputeContentHash()
		}

		// Insert issue
		if err := insertIssue(ctx, tx, issue); err != nil {
			// Check for duplicate key (issue already exists)
			if strings.Contains(err.Error(), "Duplicate entry") ||
				strings.Contains(err.Error(), "UNIQUE constraint") {
				skipped++
				continue
			}
			return imported, skipped, fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
		}

		// Import labels
		for _, label := range issue.Labels {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO labels (issue_id, label)
				VALUES (?, ?)
			`, issue.ID, label)
			if err != nil && !strings.Contains(err.Error(), "Duplicate entry") {
				return imported, skipped, fmt.Errorf("failed to insert label for %s: %w", issue.ID, err)
			}
		}

		imported++
	}

	// Import dependencies in a second pass (after all issues exist)
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			// Check if both issues exist
			var exists int
			err := tx.QueryRowContext(ctx, "SELECT 1 FROM issues WHERE id = ?", dep.DependsOnID).Scan(&exists)
			if err != nil {
				// Target doesn't exist, skip dependency
				continue
			}

			_, err = tx.ExecContext(ctx, `
				INSERT INTO dependencies (issue_id, depends_on_id, type, created_by, created_at)
				VALUES (?, ?, ?, ?, ?)
				ON DUPLICATE KEY UPDATE type = type
			`, dep.IssueID, dep.DependsOnID, dep.Type, "bootstrap", time.Now().UTC())
			if err != nil && !strings.Contains(err.Error(), "Duplicate entry") {
				// Non-fatal for dependencies
				fmt.Fprintf(os.Stderr, "Bootstrap: warning: failed to import dependency %s -> %s: %v\n",
					dep.IssueID, dep.DependsOnID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return imported, skipped, fmt.Errorf("failed to commit: %w", err)
	}

	return imported, skipped, nil
}
