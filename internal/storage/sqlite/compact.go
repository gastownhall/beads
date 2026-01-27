package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// GetTier1Candidates returns issues eligible for Tier 1 compaction.
// Criteria:
// - Status = closed
// - Closed for at least compact_tier1_days
// - No open dependents within compact_tier1_dep_levels depth
// - Not already compacted (compaction_level = 0)
func (s *SQLiteStorage) GetTier1Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	// Get configuration
	daysStr, err := s.GetConfig(ctx, "compact_tier1_days")
	if err != nil {
		return nil, fmt.Errorf("failed to get compact_tier1_days: %w", err)
	}
	if daysStr == "" {
		daysStr = "30"
	}

	depthStr, err := s.GetConfig(ctx, "compact_tier1_dep_levels")
	if err != nil {
		return nil, fmt.Errorf("failed to get compact_tier1_dep_levels: %w", err)
	}
	if depthStr == "" {
		depthStr = "2"
	}

	query := `
		WITH RECURSIVE
		  -- Find all issues that depend on (are blocked by) other issues
		  dependent_tree AS (
		    -- Base case: direct dependents
		    SELECT
		      d.depends_on_id as issue_id,
		      i.id as dependent_id,
		      i.status as dependent_status,
		      0 as depth
		    FROM dependencies d
		    JOIN issues i ON d.issue_id = i.id
		    WHERE d.type = 'blocks'

		    UNION ALL

		    -- Recursive case: parent-child relationships
		    SELECT
		      dt.issue_id,
		      i.id as dependent_id,
		      i.status as dependent_status,
		      dt.depth + 1
		    FROM dependent_tree dt
		    JOIN dependencies d ON d.depends_on_id = dt.dependent_id
		    JOIN issues i ON d.issue_id = i.id
		    WHERE d.type = 'parent-child'
		      AND dt.depth < ?
		  )
		SELECT
		  i.id,
		  i.closed_at,
		  COALESCE(i.original_size, LENGTH(i.description) + LENGTH(i.design) + LENGTH(i.notes) + LENGTH(i.acceptance_criteria)) as original_size,
		  0 as estimated_size,
		  COUNT(DISTINCT dt.dependent_id) as dependent_count
		FROM issues i
		LEFT JOIN dependent_tree dt ON i.id = dt.issue_id
		  AND dt.dependent_status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		  AND dt.depth <= ?
		WHERE i.status = 'closed'
		  AND i.closed_at IS NOT NULL
		  AND i.closed_at <= datetime('now', '-' || CAST(? AS INTEGER) || ' days')
		  AND COALESCE(i.compaction_level, 0) = 0
		  AND COALESCE(i.pinned, 0) = 0  -- Exclude pinned issues (bd-b2k)
		  AND dt.dependent_id IS NULL  -- No open dependents
		GROUP BY i.id
		ORDER BY i.closed_at ASC
	`

	rows, err := s.db.QueryContext(ctx, query, depthStr, depthStr, daysStr)
	if err != nil {
		return nil, fmt.Errorf("failed to query tier1 candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var candidates []*types.CompactionCandidate
	for rows.Next() {
		var c types.CompactionCandidate
		if err := rows.Scan(&c.IssueID, &c.ClosedAt, &c.OriginalSize, &c.EstimatedSize, &c.DependentCount); err != nil {
			return nil, fmt.Errorf("failed to scan candidate: %w", err)
		}
		candidates = append(candidates, &c)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	return candidates, nil
}

// GetTier2Candidates returns issues eligible for Tier 2 compaction.
// Criteria:
// - Status = closed
// - Closed for at least compact_tier2_days
// - No open dependents within compact_tier2_dep_levels depth
// - Already at compaction_level = 1
// - Either has many commits (compact_tier2_commits) or many dependent issues
func (s *SQLiteStorage) GetTier2Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	// Get configuration
	daysStr, err := s.GetConfig(ctx, "compact_tier2_days")
	if err != nil {
		return nil, fmt.Errorf("failed to get compact_tier2_days: %w", err)
	}
	if daysStr == "" {
		daysStr = "90"
	}

	commitsStr, err := s.GetConfig(ctx, "compact_tier2_commits")
	if err != nil {
		return nil, fmt.Errorf("failed to get compact_tier2_commits: %w", err)
	}
	if commitsStr == "" {
		commitsStr = "100"
	}

	query := `
		WITH event_counts AS (
		  SELECT issue_id, COUNT(*) as event_count
		  FROM events
		  GROUP BY issue_id
		)
		SELECT
		  i.id,
		  i.closed_at,
		  i.original_size,
		  0 as estimated_size,
		  COALESCE(ec.event_count, 0) as dependent_count
		FROM issues i
		LEFT JOIN event_counts ec ON i.id = ec.issue_id
		WHERE i.status = 'closed'
		  AND i.closed_at IS NOT NULL
		  AND i.closed_at <= datetime('now', '-' || CAST(? AS INTEGER) || ' days')
		  AND i.compaction_level = 1
		  AND COALESCE(i.pinned, 0) = 0  -- Exclude pinned issues (bd-b2k)
		  AND COALESCE(ec.event_count, 0) >= CAST(? AS INTEGER)
		  AND NOT EXISTS (
		    -- Check for open dependents
		    SELECT 1 FROM dependencies d
		    JOIN issues dep ON d.issue_id = dep.id
		    WHERE d.depends_on_id = i.id
		      AND d.type = 'blocks'
		      AND dep.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		  )
		ORDER BY i.closed_at ASC
	`

	rows, err := s.db.QueryContext(ctx, query, daysStr, commitsStr)
	if err != nil {
		return nil, fmt.Errorf("failed to query tier2 candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var candidates []*types.CompactionCandidate
	for rows.Next() {
		var c types.CompactionCandidate
		if err := rows.Scan(&c.IssueID, &c.ClosedAt, &c.OriginalSize, &c.EstimatedSize, &c.DependentCount); err != nil {
			return nil, fmt.Errorf("failed to scan candidate: %w", err)
		}
		candidates = append(candidates, &c)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	return candidates, nil
}

// CheckEligibility checks if a specific issue is eligible for compaction at the given tier.
// Returns (eligible, reason, error).
// If not eligible, reason explains why.
func (s *SQLiteStorage) CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error) {
	// Get the issue
	var status string
	var closedAt sql.NullTime
	var compactionLevel int
	var pinned int

	err := s.db.QueryRowContext(ctx, `
		SELECT status, closed_at, COALESCE(compaction_level, 0), COALESCE(pinned, 0)
		FROM issues
		WHERE id = ?
	`, issueID).Scan(&status, &closedAt, &compactionLevel, &pinned)

	if err == sql.ErrNoRows {
		return false, "issue not found", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("failed to get issue: %w", err)
	}

	// Check basic requirements
	if status != "closed" {
		return false, "issue is not closed", nil
	}

	if !closedAt.Valid {
		return false, "issue has no closed_at timestamp", nil
	}

	// Pinned issues are protected from compaction (bd-b2k)
	if pinned != 0 {
		return false, "issue is pinned (protected from compaction)", nil
	}

	switch tier {
	case 1:
		if compactionLevel != 0 {
			return false, "issue is already compacted", nil
		}
		
		// Check if it appears in tier1 candidates
		candidates, err := s.GetTier1Candidates(ctx)
		if err != nil {
			return false, "", fmt.Errorf("failed to get tier1 candidates: %w", err)
		}
		
		for _, c := range candidates {
			if c.IssueID == issueID {
				return true, "", nil
			}
		}
		
		return false, "issue has open dependents or not closed long enough", nil
		
	case 2:
		if compactionLevel != 1 {
			return false, "issue must be at compaction level 1 for tier 2", nil
		}
		
		// Check if it appears in tier2 candidates
		candidates, err := s.GetTier2Candidates(ctx)
		if err != nil {
			return false, "", fmt.Errorf("failed to get tier2 candidates: %w", err)
		}
		
		for _, c := range candidates {
			if c.IssueID == issueID {
				return true, "", nil
			}
		}
		
		return false, "issue has open dependents, not closed long enough, or insufficient events", nil
	}
	
	return false, fmt.Sprintf("invalid tier: %d", tier), nil
}

// ApplyCompaction updates the compaction metadata for an issue after successfully compacting it.
// This sets compaction_level, compacted_at, compacted_at_commit, and original_size fields.
func (s *SQLiteStorage) ApplyCompaction(ctx context.Context, issueID string, level int, originalSize int, compressedSize int, commitHash string) error {
	now := time.Now().UTC()

	return s.withTx(ctx, func(conn *sql.Conn) error {
		var commitHashPtr *string
		if commitHash != "" {
			commitHashPtr = &commitHash
		}

		res, err := conn.ExecContext(ctx, `
			UPDATE issues
			SET compaction_level = ?,
			    compacted_at = ?,
			    compacted_at_commit = ?,
			    original_size = ?,
			    updated_at = ?
			WHERE id = ?
		`, level, now, commitHashPtr, originalSize, now, issueID)

		if err != nil {
			return fmt.Errorf("failed to apply compaction metadata: %w", err)
		}

		rows, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		}
		if rows == 0 {
			return fmt.Errorf("issue %s not found", issueID)
		}

		reductionPct := 0.0
		if originalSize > 0 {
			reductionPct = (1.0 - float64(compressedSize)/float64(originalSize)) * 100
		}

		eventData := fmt.Sprintf(`{"tier":%d,"original_size":%d,"compressed_size":%d,"reduction_pct":%.1f}`,
			level, originalSize, compressedSize, reductionPct)

		_, err = conn.ExecContext(ctx, `
			INSERT INTO events (issue_id, event_type, actor, comment)
			VALUES (?, ?, 'compactor', ?)
		`, issueID, types.EventCompacted, eventData)

		if err != nil {
			return fmt.Errorf("failed to record compaction event: %w", err)
		}

		return nil
	})
}
