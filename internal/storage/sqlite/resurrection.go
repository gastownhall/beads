package sqlite

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TryResurrectParent attempts to resurrect a deleted parent issue from JSONL history.
// If the parent is found in the JSONL file, it creates a tombstone issue (status=closed)
// to preserve referential integrity for hierarchical children.
//
// This function is called during import when a child issue references a missing parent.
//
// Returns:
//   - true if parent was successfully resurrected or already exists
//   - false if parent was not found in JSONL history
//   - error if resurrection failed for any other reason
func (s *SQLiteStorage) TryResurrectParent(ctx context.Context, parentID string) (bool, error) {
	// Get a connection for the entire resurrection operation
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get database connection: %w", err)
	}
	defer conn.Close()

	return s.tryResurrectParentWithConn(ctx, conn, parentID)
}

// tryResurrectParentWithConn is the internal version that accepts an existing connection.
// This allows resurrection to participate in an existing transaction.
func (s *SQLiteStorage) tryResurrectParentWithConn(ctx context.Context, conn *sql.Conn, parentID string) (bool, error) {
	// First check if parent already exists in database
	var count int
	err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, parentID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check parent existence: %w", err)
	}
	if count > 0 {
		return true, nil // Parent already exists, nothing to do
	}

	// Before resurrecting this parent, ensure its entire ancestor chain exists (bd-ar2.4)
	// This handles deeply nested cases where we're resurrecting bd-root.1.2 and bd-root.1 is also missing
	ancestors := extractParentChain(parentID)
	for _, ancestor := range ancestors {
		// Recursively resurrect each ancestor in the chain
		resurrected, err := s.tryResurrectParentWithConn(ctx, conn, ancestor)
		if err != nil {
			return false, fmt.Errorf("failed to resurrect ancestor %s: %w", ancestor, err)
		}
		if !resurrected {
			return false, nil // Ancestor not found in history, can't continue
		}
	}

	// Parent doesn't exist - try to find it in JSONL history
	parentIssue, err := s.findIssueInJSONL(parentID)
	if err != nil {
		return false, fmt.Errorf("failed to search JSONL history: %w", err)
	}
	if parentIssue == nil {
		return false, nil // Parent not found in history
	}

	// Create tombstone version of the parent
	now := time.Now()
	tombstone := &types.Issue{
		ID:          parentIssue.ID,
		ContentHash: parentIssue.ContentHash,
		Title:       parentIssue.Title,
		Description: "[RESURRECTED] This issue was deleted but recreated as a tombstone to preserve hierarchical structure.",
		Status:      types.StatusClosed,
		Priority:    4, // Lowest priority
		IssueType:   parentIssue.IssueType,
		CreatedAt:   parentIssue.CreatedAt,
		UpdatedAt:   now,
		ClosedAt:    &now,
	}

	// If original issue had description, append it
	if parentIssue.Description != "" {
		tombstone.Description = fmt.Sprintf("%s\n\nOriginal description:\n%s", tombstone.Description, parentIssue.Description)
	}

	// Insert tombstone into database using the provided connection
	if err := insertIssue(ctx, conn, tombstone); err != nil {
		return false, fmt.Errorf("failed to create tombstone for parent %s: %w", parentID, err)
	}

	// Also copy dependencies if they exist in the JSONL
	if len(parentIssue.Dependencies) > 0 {
		for _, dep := range parentIssue.Dependencies {
			// Only resurrect dependencies if both source and target exist
			var targetCount int
			err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, dep.DependsOnID).Scan(&targetCount)
			if err == nil && targetCount > 0 {
				_, err := conn.ExecContext(ctx, `
					INSERT OR IGNORE INTO dependencies (issue_id, depends_on_id, type, created_by)
					VALUES (?, ?, ?, ?)
				`, parentID, dep.DependsOnID, dep.Type, "resurrection")
				if err != nil {
					// Log but don't fail - dependency resurrection is best-effort
					fmt.Fprintf(os.Stderr, "Warning: failed to resurrect dependency for %s: %v\n", parentID, err)
				}
			}
		}
	}

	return true, nil
}

// findIssueInJSONL searches the JSONL file for a specific issue ID.
// Returns nil if not found, or the issue if found.
func (s *SQLiteStorage) findIssueInJSONL(issueID string) (*types.Issue, error) {
	// Get database directory
	dbDir := filepath.Dir(s.dbPath)

	// JSONL file is expected at .beads/issues.jsonl relative to repo root
	// The db is at .beads/beads.db, so we need the parent directory
	jsonlPath := filepath.Join(dbDir, "issues.jsonl")

	// Check if JSONL file exists
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		return nil, nil // No JSONL file, can't resurrect
	}

	// Open and scan JSONL file
	file, err := os.Open(jsonlPath) // #nosec G304 -- jsonlPath is from trusted beads directory
	if err != nil {
		return nil, fmt.Errorf("failed to open JSONL file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Increase buffer size for large issues
	const maxCapacity = 1024 * 1024 // 1MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	lineNum := 0
	var lastMatch *types.Issue
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip empty lines
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Quick check: does this line contain our issue ID?
		// This is an optimization to avoid parsing every JSON object
		if !strings.Contains(line, `"`+issueID+`"`) {
			continue
		}

		// Parse JSON
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			// Skip malformed lines with warning
			fmt.Fprintf(os.Stderr, "Warning: skipping malformed JSONL line %d: %v\n", lineNum, err)
			continue
		}

		// Keep the last occurrence (JSONL append-only semantics)
		if issue.ID == issueID {
			issueCopy := issue
			lastMatch = &issueCopy
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading JSONL file: %w", err)
	}

	return lastMatch, nil // Returns last match or nil if not found
}

// TryResurrectParentChain recursively resurrects all missing parents in a hierarchical ID chain.
// For example, if resurrecting "bd-abc.1.2", this ensures both "bd-abc" and "bd-abc.1" exist.
//
// Returns:
//   - true if entire chain was successfully resurrected or already exists
//   - false if any parent in the chain was not found in JSONL history
//   - error if resurrection failed for any other reason
func (s *SQLiteStorage) TryResurrectParentChain(ctx context.Context, childID string) (bool, error) {
	// Get a connection for the entire chain resurrection
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get database connection: %w", err)
	}
	defer conn.Close()

	return s.tryResurrectParentChainWithConn(ctx, conn, childID)
}

// tryResurrectParentChainWithConn is the internal version that accepts an existing connection.
func (s *SQLiteStorage) tryResurrectParentChainWithConn(ctx context.Context, conn *sql.Conn, childID string) (bool, error) {
	// Extract all parent IDs from the hierarchical chain
	parents := extractParentChain(childID)

	// Resurrect from root to leaf (shallower to deeper)
	for _, parentID := range parents {
		resurrected, err := s.tryResurrectParentWithConn(ctx, conn, parentID)
		if err != nil {
			return false, fmt.Errorf("failed to resurrect parent %s: %w", parentID, err)
		}
		if !resurrected {
			return false, nil // Parent not found in history, can't continue
		}
	}

	return true, nil
}

// extractParentChain returns all parent IDs in a hierarchical chain, ordered from root to leaf.
// Example: "bd-abc.1.2" → ["bd-abc", "bd-abc.1"]
// Example: "test.example-abc.1" → ["test.example-abc"] (prefix with dot is preserved)
//
// This function uses isHierarchicalID to correctly handle prefixes containing dots (GH#664).
// It only splits on dots followed by numeric suffixes (the hierarchy delimiter).
func extractParentChain(id string) []string {
	var parents []string
	current := id

	// Walk up the hierarchy by repeatedly finding the parent
	for {
		isHierarchical, parentID := isHierarchicalID(current)
		if !isHierarchical {
			break // No more parents
		}
		// Prepend to build root-to-leaf order
		parents = append([]string{parentID}, parents...)
		current = parentID
	}

	return parents
}
