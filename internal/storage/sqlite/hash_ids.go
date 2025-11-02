package sqlite

import (
	"context"
	"fmt"
	"strings"
)

// getNextChildNumber atomically increments and returns the next child counter for a parent issue.
// Uses INSERT...ON CONFLICT to ensure atomicity without explicit locking.
func (s *SQLiteStorage) getNextChildNumber(ctx context.Context, parentID string) (int, error) {
	var nextChild int
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO child_counters (parent_id, last_child)
		VALUES (?, 1)
		ON CONFLICT(parent_id) DO UPDATE SET
			last_child = last_child + 1
		RETURNING last_child
	`, parentID).Scan(&nextChild)
	if err != nil {
		return 0, fmt.Errorf("failed to generate next child number for parent %s: %w", parentID, err)
	}
	return nextChild, nil
}

// GetNextChildID generates the next hierarchical child ID for a given parent
// Returns formatted ID as parentID.{counter} (e.g., bd-a3f8e9.1 or bd-a3f8e9.1.5)
// Works at any depth (max 3 levels)
func (s *SQLiteStorage) GetNextChildID(ctx context.Context, parentID string) (string, error) {
	// Validate parent exists
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, parentID).Scan(&count)
	if err != nil {
		return "", fmt.Errorf("failed to check parent existence: %w", err)
	}
	if count == 0 {
		return "", fmt.Errorf("parent issue %s does not exist", parentID)
	}
	
	// Calculate current depth by counting dots
	depth := strings.Count(parentID, ".")
	if depth >= 3 {
		return "", fmt.Errorf("maximum hierarchy depth (3) exceeded for parent %s", parentID)
	}
	
	// Get next child number atomically
	nextNum, err := s.getNextChildNumber(ctx, parentID)
	if err != nil {
		return "", err
	}
	
	// Format as parentID.counter
	childID := fmt.Sprintf("%s.%d", parentID, nextNum)
	return childID, nil
}

// generateHashID moved to ids.go (bd-0702)
