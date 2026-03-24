package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

const (
	defaultTier1Days = 30
	defaultTier2Days = 90
)

// getCompactDaysInTx reads the configured compaction threshold for the given tier.
func getCompactDaysInTx(ctx context.Context, tx *sql.Tx, tier int) int {
	key := "compact_tier1_days"
	def := defaultTier1Days
	if tier == 2 {
		key = "compact_tier2_days"
		def = defaultTier2Days
	}
	var val string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&val)
	if err != nil || val == "" {
		return def
	}
	n, err := strconv.Atoi(val)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// CheckEligibilityInTx checks if an issue is eligible for compaction at the given tier.
func CheckEligibilityInTx(ctx context.Context, tx *sql.Tx, issueID string, tier int) (bool, string, error) {
	var status string
	var closedAt sql.NullTime
	var compactionLevel int

	err := tx.QueryRowContext(ctx,
		`SELECT status, closed_at, compaction_level FROM issues WHERE id = ?`, issueID,
	).Scan(&status, &closedAt, &compactionLevel)
	if err == sql.ErrNoRows {
		return false, fmt.Sprintf("issue %s not found", issueID), nil
	}
	if err != nil {
		return false, "", fmt.Errorf("query issue: %w", err)
	}

	if types.Status(status) != types.StatusClosed {
		return false, fmt.Sprintf("issue is not closed (status: %s)", status), nil
	}
	if !closedAt.Valid {
		return false, "issue has no closed_at timestamp", nil
	}

	threshold := getCompactDaysInTx(ctx, tx, tier)
	daysClosed := time.Since(closedAt.Time).Hours() / 24

	if tier == 1 {
		if compactionLevel >= 1 {
			return false, "already compacted at tier 1 or higher", nil
		}
		if daysClosed < float64(threshold) {
			return false, fmt.Sprintf("closed only %.0f days ago (need %d+)", daysClosed, threshold), nil
		}
	} else if tier == 2 {
		if compactionLevel >= 2 {
			return false, "already compacted at tier 2", nil
		}
		if compactionLevel < 1 {
			return false, "must be tier 1 compacted first", nil
		}
		if daysClosed < float64(threshold) {
			return false, fmt.Sprintf("closed only %.0f days ago (need %d+)", daysClosed, threshold), nil
		}
	} else {
		return false, fmt.Sprintf("unsupported tier: %d", tier), nil
	}

	return true, "", nil
}

// ApplyCompactionInTx records a compaction result.
func ApplyCompactionInTx(ctx context.Context, tx *sql.Tx, issueID string, tier int, originalSize int, commitHash string) error {
	now := time.Now().UTC()
	_, err := tx.ExecContext(ctx,
		`UPDATE issues SET compaction_level = ?, compacted_at = ?, compacted_at_commit = ?, original_size = ?, updated_at = ? WHERE id = ?`,
		tier, now, commitHash, originalSize, now, issueID)
	if err != nil {
		return fmt.Errorf("apply compaction: %w", err)
	}
	return nil
}

// GetTier1CandidatesInTx returns issues eligible for tier 1 compaction.
func GetTier1CandidatesInTx(ctx context.Context, tx *sql.Tx) ([]*types.CompactionCandidate, error) {
	days := getCompactDaysInTx(ctx, tx, 1)
	rows, err := tx.QueryContext(ctx, `
		SELECT i.id, i.closed_at,
			CHAR_LENGTH(i.description) + CHAR_LENGTH(i.design) + CHAR_LENGTH(i.notes) + CHAR_LENGTH(i.acceptance_criteria) AS original_size,
			COALESCE((SELECT COUNT(*) FROM dependencies d WHERE d.depends_on_id = i.id AND d.type = 'blocks'), 0) AS dependent_count
		FROM issues i
		WHERE i.status = ?
			AND i.closed_at IS NOT NULL
			AND i.closed_at <= ?
			AND (i.compaction_level = 0 OR i.compaction_level IS NULL)
		ORDER BY i.closed_at ASC`,
		string(types.StatusClosed), time.Now().UTC().Add(-time.Duration(days)*24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("query tier 1 candidates: %w", err)
	}
	defer rows.Close()
	return scanCompactionCandidates(rows)
}

// GetTier2CandidatesInTx returns issues eligible for tier 2 compaction.
func GetTier2CandidatesInTx(ctx context.Context, tx *sql.Tx) ([]*types.CompactionCandidate, error) {
	days := getCompactDaysInTx(ctx, tx, 2)
	rows, err := tx.QueryContext(ctx, `
		SELECT i.id, i.closed_at,
			CHAR_LENGTH(i.description) + CHAR_LENGTH(i.design) + CHAR_LENGTH(i.notes) + CHAR_LENGTH(i.acceptance_criteria) AS original_size,
			COALESCE((SELECT COUNT(*) FROM dependencies d WHERE d.depends_on_id = i.id AND d.type = 'blocks'), 0) AS dependent_count
		FROM issues i
		WHERE i.status = ?
			AND i.closed_at IS NOT NULL
			AND i.closed_at <= ?
			AND i.compaction_level = 1
		ORDER BY i.closed_at ASC`,
		string(types.StatusClosed), time.Now().UTC().Add(-time.Duration(days)*24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("query tier 2 candidates: %w", err)
	}
	defer rows.Close()
	return scanCompactionCandidates(rows)
}

func scanCompactionCandidates(rows *sql.Rows) ([]*types.CompactionCandidate, error) {
	var candidates []*types.CompactionCandidate
	for rows.Next() {
		c := &types.CompactionCandidate{}
		if err := rows.Scan(&c.IssueID, &c.ClosedAt, &c.OriginalSize, &c.DependentCount); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		c.EstimatedSize = c.OriginalSize * 3 / 10
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

// GetMoleculeLastActivityInTx returns the most recent activity for a molecule.
//
//nolint:gosec // G201: table names are hardcoded via WispTableRouting
func GetMoleculeLastActivityInTx(ctx context.Context, tx *sql.Tx, moleculeID string) (*types.MoleculeLastActivity, error) {
	isWisp := IsActiveWispInTx(ctx, tx, moleculeID)
	issueTable, _, _, depTable := WispTableRouting(isWisp)

	// Get child IDs
	depRows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT issue_id FROM %s
		WHERE depends_on_id = ? AND type = 'parent-child'
	`, depTable), moleculeID)
	if err != nil {
		return nil, fmt.Errorf("get molecule children: %w", err)
	}
	var childIDs []string
	for depRows.Next() {
		var id string
		if err := depRows.Scan(&id); err != nil {
			_ = depRows.Close()
			return nil, fmt.Errorf("scan child: %w", err)
		}
		childIDs = append(childIDs, id)
	}
	_ = depRows.Close()

	if len(childIDs) == 0 {
		var updatedAt time.Time
		err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT updated_at FROM %s WHERE id = ?", issueTable), moleculeID).Scan(&updatedAt)
		if err != nil {
			return nil, fmt.Errorf("molecule %s not found: %w", moleculeID, err)
		}
		return &types.MoleculeLastActivity{
			MoleculeID:   moleculeID,
			LastActivity: updatedAt,
			Source:       "molecule_updated",
		}, nil
	}

	var lastUpdatedAt time.Time
	var lastUpdatedID string
	var lastClosedAt sql.NullTime
	var lastClosedID sql.NullString

	for start := 0; start < len(childIDs); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(childIDs) {
			end = len(childIDs)
		}
		batch := childIDs[start:end]
		placeholders, args := buildSQLInClause(batch)

		var batchUpdatedAt time.Time
		var batchUpdatedID string
		scanErr := tx.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT id, updated_at FROM %s WHERE id IN (%s) ORDER BY updated_at DESC LIMIT 1",
			issueTable, placeholders), args...).Scan(&batchUpdatedID, &batchUpdatedAt)
		if scanErr == nil && batchUpdatedAt.After(lastUpdatedAt) {
			lastUpdatedAt = batchUpdatedAt
			lastUpdatedID = batchUpdatedID
		}

		var batchClosedAt sql.NullTime
		var batchClosedID sql.NullString
		_ = tx.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT id, closed_at FROM %s WHERE id IN (%s) AND closed_at IS NOT NULL ORDER BY closed_at DESC LIMIT 1",
			issueTable, placeholders), args...).Scan(&batchClosedID, &batchClosedAt)
		if batchClosedAt.Valid && (!lastClosedAt.Valid || batchClosedAt.Time.After(lastClosedAt.Time)) {
			lastClosedAt = batchClosedAt
			lastClosedID = batchClosedID
		}
	}

	if lastUpdatedID == "" {
		return nil, fmt.Errorf("no children found for molecule %s", moleculeID)
	}

	result := &types.MoleculeLastActivity{
		MoleculeID:   moleculeID,
		LastActivity: lastUpdatedAt,
		Source:       "step_updated",
		SourceStepID: lastUpdatedID,
	}

	if lastClosedAt.Valid && lastClosedAt.Time.After(lastUpdatedAt) {
		result.LastActivity = lastClosedAt.Time
		result.Source = "step_closed"
		if lastClosedID.Valid {
			result.SourceStepID = lastClosedID.String
		}
	}

	return result, nil
}
