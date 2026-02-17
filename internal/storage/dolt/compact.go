//go:build cgo

package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// CheckEligibility checks if an issue is eligible for compaction at the given tier.
// Tier 1: closed 30+ days ago, compaction_level=0
// Tier 2: closed 90+ days ago, compaction_level=1
func (s *DoltStore) CheckEligibility(ctx context.Context, issueID string, tier int) (bool, string, error) {
	var status string
	var closedAt sql.NullTime
	var compactionLevel int

	err := s.queryRowContext(ctx, func(row *sql.Row) error {
		return row.Scan(&status, &closedAt, &compactionLevel)
	}, `SELECT status, closed_at, compaction_level FROM issues WHERE id = ?`, issueID)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, fmt.Sprintf("issue %s not found", issueID), nil
		}
		return false, "", fmt.Errorf("failed to query issue: %w", err)
	}

	if status != "closed" {
		return false, fmt.Sprintf("issue is not closed (status: %s)", status), nil
	}

	if !closedAt.Valid {
		return false, "issue has no closed_at timestamp", nil
	}

	if tier == 1 {
		if compactionLevel >= 1 {
			return false, "already compacted at tier 1 or higher", nil
		}
		daysClosed := time.Since(closedAt.Time).Hours() / 24
		if daysClosed < 30 {
			return false, fmt.Sprintf("closed only %.0f days ago (need 30+)", daysClosed), nil
		}
	} else if tier == 2 {
		if compactionLevel >= 2 {
			return false, "already compacted at tier 2", nil
		}
		if compactionLevel < 1 {
			return false, "must be tier 1 compacted first", nil
		}
		daysClosed := time.Since(closedAt.Time).Hours() / 24
		if daysClosed < 90 {
			return false, fmt.Sprintf("closed only %.0f days ago (need 90+)", daysClosed), nil
		}
	} else {
		return false, fmt.Sprintf("unsupported tier: %d", tier), nil
	}

	return true, "", nil
}

// ApplyCompaction records a compaction result in the database.
// Updates compaction_level, compacted_at, compacted_at_commit, and original_size.
func (s *DoltStore) ApplyCompaction(ctx context.Context, issueID string, tier int, originalSize int, _ int, commitHash string) error {
	_, err := s.execContext(ctx,
		`UPDATE issues SET compaction_level = ?, compacted_at = ?, compacted_at_commit = ?, original_size = ?, updated_at = ? WHERE id = ?`,
		tier, time.Now().UTC(), commitHash, originalSize, time.Now().UTC(), issueID)
	if err != nil {
		return fmt.Errorf("failed to apply compaction metadata: %w", err)
	}
	return nil
}

// GetTier1Candidates returns issues eligible for tier 1 compaction.
// Tier 1: closed 30+ days ago, not yet compacted (compaction_level=0).
func (s *DoltStore) GetTier1Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	rows, err := s.queryContext(ctx, `
		SELECT i.id, i.closed_at,
			CHAR_LENGTH(i.description) + CHAR_LENGTH(i.design) + CHAR_LENGTH(i.notes) + CHAR_LENGTH(i.acceptance_criteria) AS original_size,
			COALESCE((SELECT COUNT(*) FROM dependencies d WHERE d.depends_on_id = i.id AND d.type = 'blocks'), 0) AS dependent_count
		FROM issues i
		WHERE i.status = 'closed'
			AND i.closed_at IS NOT NULL
			AND i.closed_at <= ?
			AND (i.compaction_level = 0 OR i.compaction_level IS NULL)
		ORDER BY i.closed_at ASC`,
		time.Now().UTC().Add(-30*24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("failed to query tier 1 candidates: %w", err)
	}
	defer rows.Close()

	return scanCompactionCandidates(rows)
}

// GetTier2Candidates returns issues eligible for tier 2 compaction.
// Tier 2: closed 90+ days ago, already tier 1 compacted (compaction_level=1).
func (s *DoltStore) GetTier2Candidates(ctx context.Context) ([]*types.CompactionCandidate, error) {
	rows, err := s.queryContext(ctx, `
		SELECT i.id, i.closed_at,
			CHAR_LENGTH(i.description) + CHAR_LENGTH(i.design) + CHAR_LENGTH(i.notes) + CHAR_LENGTH(i.acceptance_criteria) AS original_size,
			COALESCE((SELECT COUNT(*) FROM dependencies d WHERE d.depends_on_id = i.id AND d.type = 'blocks'), 0) AS dependent_count
		FROM issues i
		WHERE i.status = 'closed'
			AND i.closed_at IS NOT NULL
			AND i.closed_at <= ?
			AND i.compaction_level = 1
		ORDER BY i.closed_at ASC`,
		time.Now().UTC().Add(-90*24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("failed to query tier 2 candidates: %w", err)
	}
	defer rows.Close()

	return scanCompactionCandidates(rows)
}

// scanCompactionCandidates scans rows into CompactionCandidate structs.
func scanCompactionCandidates(rows *sql.Rows) ([]*types.CompactionCandidate, error) {
	var candidates []*types.CompactionCandidate
	for rows.Next() {
		c := &types.CompactionCandidate{}
		if err := rows.Scan(&c.IssueID, &c.ClosedAt, &c.OriginalSize, &c.DependentCount); err != nil {
			return nil, fmt.Errorf("failed to scan candidate: %w", err)
		}
		c.EstimatedSize = c.OriginalSize * 3 / 10 // ~70% reduction estimate
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating candidates: %w", err)
	}
	return candidates, nil
}
