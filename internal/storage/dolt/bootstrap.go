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
)

// BootstrapResult contains statistics about the bootstrap operation
type BootstrapResult struct {
	IssuesImported       int
	IssuesSkipped        int
	RoutesImported       int
	InteractionsImported int
	ParseErrors          []ParseError
	PrefixDetected       string
}

// ParseError describes a JSONL parsing error
type ParseError struct {
	Line    int
	Message string
	Snippet string
}

// BootstrapRoute holds route data for bootstrap import.
// This is a local type to avoid importing internal/routing (which imports dolt, causing a cycle).
type BootstrapRoute struct {
	Prefix string
	Path   string
}

// bootstrapInteractionEntry is a local type for deserializing interactions.jsonl entries.
// Avoids importing internal/audit (which imports internal/beads → dolt, causing a cycle).
type bootstrapInteractionEntry struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	CreatedAt time.Time      `json:"created_at"`
	Actor     string         `json:"actor,omitempty"`
	IssueID   string         `json:"issue_id,omitempty"`
	Model     string         `json:"model,omitempty"`
	Prompt    string         `json:"prompt,omitempty"`
	Response  string         `json:"response,omitempty"`
	Error     string         `json:"error,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	ExitCode  *int           `json:"exit_code,omitempty"`
	ParentID  string         `json:"parent_id,omitempty"`
	Label     string         `json:"label,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// BootstrapConfig controls bootstrap behavior
type BootstrapConfig struct {
	BeadsDir    string        // Path to .beads directory
	DoltPath    string        // Path to dolt subdirectory
	LockTimeout time.Duration // Timeout waiting for bootstrap lock
	Database    string        // Database name (e.g. "beads_hq"); defaults to "beads"

	// Routes to import during bootstrap (loaded by caller from routes.jsonl).
	// If nil, route import is skipped.
	Routes []BootstrapRoute
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
	if doltExists(cfg.DoltPath) && schemaReady(ctx, cfg.DoltPath, cfg.Database) {
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
	if doltExists(cfg.DoltPath) && schemaReady(ctx, cfg.DoltPath, cfg.Database) {
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
		// Use os.Stat to follow symlinks - entry.IsDir() returns false for symlinks
		fullPath := filepath.Join(doltPath, entry.Name())
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.IsDir() {
			doltDir := filepath.Join(fullPath, ".dolt")
			if doltInfo, err := os.Stat(doltDir); err == nil && doltInfo.IsDir() {
				return true
			}
		}
	}
	return false
}

// schemaReady checks if the Dolt database has the required schema
// This is a simple check based on the existence of expected files.
// We avoid opening a connection here since the caller will do that.
func schemaReady(_ context.Context, doltPath string, dbName string) bool {
	if dbName == "" {
		dbName = "beads"
	}
	// The embedded Dolt driver stores databases in subdirectories.
	// Check for the expected database name's config.json which indicates
	// the database was initialized.
	configPath := filepath.Join(doltPath, dbName, ".dolt", "config.json")
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

// staleLockAge is the maximum age of a lock file before it's considered stale.
// Bootstrap operations should complete well within this window.
const staleLockAge = 5 * time.Minute

// acquireBootstrapLock acquires an exclusive lock for bootstrap operations.
// Uses non-blocking flock with polling to respect the timeout deadline.
// Detects and cleans up stale lock files from crashed processes.
func acquireBootstrapLock(lockPath string, timeout time.Duration) (*os.File, error) {
	// Check for stale lock file before attempting to acquire.
	// If the lock file is very old, the holding process likely crashed
	// without cleanup. Remove it so we can proceed.
	if info, err := os.Stat(lockPath); err == nil {
		age := time.Since(info.ModTime())
		if age > staleLockAge {
			fmt.Fprintf(os.Stderr, "Bootstrap: removing stale lock file (age: %s)\n", age.Round(time.Second))
			_ = os.Remove(lockPath) // Best effort cleanup of lock file
		}
	}

	// Create lock file
	// #nosec G304 - controlled path
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to create lock file: %w", err)
	}

	// Try to acquire lock with non-blocking flock and polling.
	// The previous implementation used FlockExclusiveBlocking which blocks
	// indefinitely, making the timeout unreachable.
	deadline := time.Now().Add(timeout)
	for {
		err := lockfile.FlockExclusiveNonBlocking(f)
		if err == nil {
			// Lock acquired - update modification time for stale detection
			return f, nil
		}

		if !lockfile.IsLocked(err) {
			// Unexpected error (not contention)
			_ = f.Close() // Best effort cleanup on error path
			return nil, fmt.Errorf("failed to acquire bootstrap lock: %w", err)
		}

		if time.Now().After(deadline) {
			_ = f.Close() // Best effort cleanup on error path
			return nil, fmt.Errorf("timeout after %s waiting for bootstrap lock (another bootstrap may be running)", timeout)
		}

		// Wait briefly before retrying
		time.Sleep(100 * time.Millisecond)
	}
}

// releaseBootstrapLock releases the bootstrap lock and removes the lock file
func releaseBootstrapLock(f *os.File, lockPath string) {
	if f != nil {
		_ = lockfile.FlockUnlock(f) // Best effort: unlock may fail if fd is bad
		_ = f.Close()               // Best effort cleanup
	}
	// Clean up lock file
	_ = os.Remove(lockPath) // Best effort cleanup of lock file
}

// performBootstrap performs the actual bootstrap from JSONL files.
// Import order: routes -> issues -> interactions (dependencies require issues to exist)
func performBootstrap(ctx context.Context, cfg BootstrapConfig, jsonlPath string) (*BootstrapResult, error) {
	result := &BootstrapResult{}

	// Parse issues JSONL with graceful error handling
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
	defer func() { _ = store.Close() }() // Best effort cleanup

	// Set issue prefix
	if result.PrefixDetected != "" {
		if err := store.SetConfig(ctx, "issue_prefix", result.PrefixDetected); err != nil {
			return nil, fmt.Errorf("failed to set issue_prefix: %w", err)
		}
	}

	// Import routes first (no dependencies)
	routesImported, err := importRoutesBootstrap(ctx, store, cfg.Routes)
	if err != nil {
		// Non-fatal - routes may not exist
		fmt.Fprintf(os.Stderr, "Bootstrap: warning: failed to import routes: %v\n", err)
	}
	result.RoutesImported = routesImported

	// Import issues in a transaction
	imported, skipped, err := importIssuesBootstrap(ctx, store, issues)
	if err != nil {
		return nil, fmt.Errorf("failed to import issues: %w", err)
	}

	result.IssuesImported = imported
	result.IssuesSkipped = skipped

	// Import interactions (after issues, since interactions may reference issue_id)
	interactionsPath := filepath.Join(cfg.BeadsDir, "interactions.jsonl")
	interactionsImported, err := importInteractionsBootstrap(ctx, store, interactionsPath)
	if err != nil {
		// Non-fatal - interactions.jsonl may not exist
		fmt.Fprintf(os.Stderr, "Bootstrap: warning: failed to import interactions: %v\n", err)
	}
	result.InteractionsImported = interactionsImported

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
	defer func() { _ = f.Close() }() // Best effort cleanup

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
				Snippet: truncateSnippet(line),
			})
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			parseErrors = append(parseErrors, ParseError{
				Line:    lineNo,
				Message: err.Error(),
				Snippet: truncateSnippet(line),
			})
			continue
		}

		// Apply defaults for omitted fields
		issue.SetDefaults()

		// Validate ID is present (corruption check)
		if issue.ID == "" {
			parseErrors = append(parseErrors, ParseError{
				Line:    lineNo,
				Message: "issue has empty ID",
				Snippet: truncateSnippet(line),
			})
			continue
		}

		// Validate status enum (catches corruption like 'opne' for 'open')
		if !issue.Status.IsValid() {
			parseErrors = append(parseErrors, ParseError{
				Line:    lineNo,
				Message: fmt.Sprintf("invalid status %q for issue %s", issue.Status, issue.ID),
				Snippet: truncateSnippet(line),
			})
			continue
		}

		// Fix closed_at invariant
		if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
			now := time.Now()
			issue.ClosedAt = &now
		}

		// Fix non-closed issue with closed_at set (corruption recovery)
		if issue.Status != types.StatusClosed && issue.ClosedAt != nil {
			issue.ClosedAt = nil
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

// truncateSnippet truncates a string for display (max 50 chars)
func truncateSnippet(s string) string {
	if len(s) > 50 {
		return s[:50] + "..."
	}
	return s
}

// detectPrefixFromIssues detects the most common prefix from issues.
// Uses a simple first-hyphen extraction to avoid importing internal/utils (cycle).
func detectPrefixFromIssues(issues []*types.Issue) string {
	prefixCounts := make(map[string]int)

	for _, issue := range issues {
		if issue.ID == "" {
			continue
		}
		// Simple prefix extraction: take everything before the first hyphen.
		// For bootstrap purposes this is sufficient (e.g., "bd-abc" → "bd").
		idx := strings.Index(issue.ID, "-")
		if idx > 0 {
			prefixCounts[issue.ID[:idx]]++
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
	// Issues are validated during parsing (parseJSONLWithErrors).
	// This function handles cross-issue uniqueness checks.

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // No-op after successful commit

	imported := 0
	skipped := 0
	seenIDs := make(map[string]bool)
	seenExternalRefs := make(map[string]bool)

	for _, issue := range issues {
		// Skip duplicates within batch
		if seenIDs[issue.ID] {
			skipped++
			continue
		}

		seenIDs[issue.ID] = true

		// Skip duplicate external_ref values (corruption protection)
		if issue.ExternalRef != nil && *issue.ExternalRef != "" {
			if seenExternalRefs[*issue.ExternalRef] {
				fmt.Fprintf(os.Stderr, "Bootstrap: warning: skipping issue %s with duplicate external_ref %s\n",
					issue.ID, *issue.ExternalRef)
				skipped++
				continue
			}
			seenExternalRefs[*issue.ExternalRef] = true
		}

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

// importRoutesBootstrap imports routes during bootstrap.
// Routes are passed via BootstrapConfig to avoid importing internal/routing (cycle).
func importRoutesBootstrap(ctx context.Context, store *DoltStore, routes []BootstrapRoute) (int, error) {
	if len(routes) == 0 {
		return 0, nil // No routes to import
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // No-op after successful commit

	imported := 0
	for _, route := range routes {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO routes (prefix, path, created_at)
			VALUES (?, ?, ?)
			ON DUPLICATE KEY UPDATE path = VALUES(path)
		`, route.Prefix, route.Path, time.Now().UTC())
		if err != nil {
			return imported, fmt.Errorf("failed to insert route %s: %w", route.Prefix, err)
		}
		imported++
	}

	if err := tx.Commit(); err != nil {
		return imported, fmt.Errorf("failed to commit routes: %w", err)
	}

	return imported, nil
}

// importInteractionsBootstrap imports interactions from interactions.jsonl during bootstrap
// Returns the number of interactions imported
func importInteractionsBootstrap(ctx context.Context, store *DoltStore, interactionsPath string) (int, error) {
	// #nosec G304 - controlled path
	f, err := os.Open(interactionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // No interactions file is not an error
		}
		return 0, err
	}
	defer func() { _ = f.Close() }() // Best effort cleanup

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // No-op after successful commit

	imported := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024), 2*1024*1024) // 2MB buffer for large lines

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var entry bootstrapInteractionEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Skip malformed lines during bootstrap
			continue
		}

		// Convert extra map to JSON (default to empty object for valid JSON)
		extraJSON := []byte("{}")
		if entry.Extra != nil {
			extraJSON, _ = json.Marshal(entry.Extra) // json.Marshal on map types does not fail in practice
		}

		_, err := tx.ExecContext(ctx, `
			INSERT INTO interactions (id, kind, created_at, actor, issue_id, model, prompt, response, error, tool_name, exit_code, parent_id, label, reason, extra)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE kind = kind
		`, entry.ID, entry.Kind, entry.CreatedAt, entry.Actor, entry.IssueID, entry.Model, entry.Prompt, entry.Response, entry.Error, entry.ToolName, entry.ExitCode, entry.ParentID, entry.Label, entry.Reason, extraJSON)
		if err != nil && !strings.Contains(err.Error(), "Duplicate entry") {
			// Non-fatal - skip individual failures
			fmt.Fprintf(os.Stderr, "Bootstrap: warning: failed to import interaction %s: %v\n", entry.ID, err)
			continue
		}
		imported++
	}

	if err := scanner.Err(); err != nil {
		return imported, fmt.Errorf("scanner error: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return imported, fmt.Errorf("failed to commit interactions: %w", err)
	}

	return imported, nil
}
