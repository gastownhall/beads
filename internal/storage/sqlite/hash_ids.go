package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
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
		// Try to resurrect parent from JSONL history before failing (bd-dvd fix, bd-ar2.4)
		// Note: Using TryResurrectParent instead of TryResurrectParentChain because we're
		// already given the direct parent ID. TryResurrectParent will handle the direct parent,
		// and if the parent itself has missing ancestors, those should have been resurrected
		// when the parent was originally created.
		resurrected, resurrectErr := s.TryResurrectParent(ctx, parentID)
		if resurrectErr != nil {
			return "", fmt.Errorf("failed to resurrect parent %s: %w", parentID, resurrectErr)
		}
		if !resurrected {
			return "", fmt.Errorf("parent issue %s does not exist and could not be resurrected from JSONL history", parentID)
		}
	}

	// Check hierarchy depth limit (GH#995)
	if err := types.CheckHierarchyDepth(parentID, config.GetInt("hierarchy.max-depth")); err != nil {
		return "", err
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

// ensureChildCounterUpdated ensures the child_counters table has a value for parentID
// that is at least childNum. This prevents ID collisions when children are created
// with explicit IDs (via --id flag or import) rather than GetNextChildID.
// (GH#728 fix)
func (s *SQLiteStorage) ensureChildCounterUpdated(ctx context.Context, parentID string, childNum int) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO child_counters (parent_id, last_child)
		VALUES (?, ?)
		ON CONFLICT(parent_id) DO UPDATE SET
			last_child = MAX(last_child, excluded.last_child)
	`, parentID, childNum)
	if err != nil {
		return fmt.Errorf("failed to update child counter for parent %s: %w", parentID, err)
	}
	return nil
}

// ensureChildCounterUpdatedWithConn is like ensureChildCounterUpdated but uses a specific
// connection for transaction consistency. (GH#728 fix)
func ensureChildCounterUpdatedWithConn(ctx context.Context, conn *sql.Conn, parentID string, childNum int) error {
	_, err := conn.ExecContext(ctx, `
		INSERT INTO child_counters (parent_id, last_child)
		VALUES (?, ?)
		ON CONFLICT(parent_id) DO UPDATE SET
			last_child = MAX(last_child, excluded.last_child)
	`, parentID, childNum)
	if err != nil {
		return fmt.Errorf("failed to update child counter for parent %s: %w", parentID, err)
	}
	return nil
}

// generateHashID moved to ids.go (bd-0702)
