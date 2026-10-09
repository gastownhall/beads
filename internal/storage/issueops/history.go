package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// HistoryInTx returns the complete version history for an issue by querying
// the dolt_history_issues system table. The result is ordered newest-first.
//
// The subquery wrapper avoids Dolt's max1Row optimization on PK lookup:
// dolt_history_* tables return multiple rows per PK (one per commit), but
// the query planner incorrectly assumes WHERE id=? returns one row.
func HistoryInTx(ctx context.Context, tx DBTX, issueID string) ([]*storage.HistoryEntry, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			id, title,
			COALESCE(description, '') AS description,
			COALESCE(design, '') AS design,
			COALESCE(acceptance_criteria, '') AS acceptance_criteria,
			COALESCE(notes, '') AS notes,
			status, priority, issue_type, assignee, owner, created_by,
			estimated_minutes, created_at, updated_at, closed_at, close_reason,
			pinned, mol_type,
			commit_hash, committer, commit_date
		FROM (
			SELECT * FROM dolt_history_issues
		) h
		WHERE h.id = ?
		ORDER BY h.commit_date DESC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue history: %w", err)
	}
	defer rows.Close()

	var entries []*storage.HistoryEntry
	for rows.Next() {
		var issue types.Issue
		var createdAtStr, updatedAtStr sql.NullString
		var closedAt sql.NullTime
		var assignee, owner, createdBy, closeReason, molType sql.NullString
		var estimatedMinutes sql.NullInt64
		var pinned sql.NullInt64
		var commitHash, committer string
		var commitDate time.Time

		if err := rows.Scan(
			&issue.ID, &issue.Title, &issue.Description, &issue.Design, &issue.AcceptanceCriteria, &issue.Notes,
			&issue.Status, &issue.Priority, &issue.IssueType, &assignee, &owner, &createdBy,
			&estimatedMinutes, &createdAtStr, &updatedAtStr, &closedAt, &closeReason,
			&pinned, &molType,
			&commitHash, &committer, &commitDate,
		); err != nil {
			return nil, fmt.Errorf("failed to scan history: %w", err)
		}

		if createdAtStr.Valid {
			issue.CreatedAt = ParseTimeString(createdAtStr.String)
		}
		if updatedAtStr.Valid {
			issue.UpdatedAt = ParseTimeString(updatedAtStr.String)
		}
		if closedAt.Valid {
			issue.ClosedAt = &closedAt.Time
		}
		if assignee.Valid {
			issue.Assignee = assignee.String
		}
		if owner.Valid {
			issue.Owner = owner.String
		}
		if createdBy.Valid {
			issue.CreatedBy = createdBy.String
		}
		if estimatedMinutes.Valid {
			mins := int(estimatedMinutes.Int64)
			issue.EstimatedMinutes = &mins
		}
		if closeReason.Valid {
			issue.CloseReason = closeReason.String
		}
		if pinned.Valid && pinned.Int64 != 0 {
			issue.Pinned = true
		}
		if molType.Valid {
			issue.MolType = types.MolType(molType.String)
		}

		entries = append(entries, &storage.HistoryEntry{
			CommitHash: commitHash,
			Committer:  committer,
			CommitDate: commitDate,
			Issue:      &issue,
		})
	}

	return entries, rows.Err()
}

// PreviousExternalRefInTx returns the external_ref value recorded for
// issueID as of the most recent commit at or before asOf, by querying the
// dolt_history_issues system table. found is false if no history entry
// exists for issueID at or before asOf.
//
// The subquery wrapper avoids Dolt's max1Row optimization on PK lookup, for
// the same reason described on HistoryInTx above.
func PreviousExternalRefInTx(ctx context.Context, tx *sql.Tx, issueID string, asOf time.Time) (string, bool, error) {
	var previousRef sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT external_ref
		FROM (
			SELECT id, external_ref, commit_date FROM dolt_history_issues
		) h
		WHERE h.id = ? AND h.commit_date <= ?
		ORDER BY h.commit_date DESC
		LIMIT 1
	`, issueID, asOf.UTC()).Scan(&previousRef)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to get previous external_ref: %w", err)
	}
	return previousRef.String, true, nil
}

// PreviousExternalRefsInTx answers PreviousExternalRefInTx for many ids at one asOf.
//
// Results are keyed by Go string equality on the ids SQL returns, which
// matches PreviousExternalRefInTx's `h.id = ?` only under the binary, NO PAD
// utf8mb4_0900_bin collation every beads table is created with.
func PreviousExternalRefsInTx(ctx context.Context, tx DBTX, ids []string, asOf time.Time, historyChunk int) (map[string]string, error) {
	refs := make(map[string]string, len(ids))
	pending := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			pending = append(pending, id)
		}
	}
	if len(pending) == 0 {
		return refs, nil
	}

	commit, tied, err := newestCommitAtOrBefore(ctx, tx, asOf)
	if err != nil || commit == "" {
		return refs, err
	}
	if !tied {
		atCommit, err := externalRefsAtCommit(ctx, tx, commit)
		if err != nil {
			return nil, err
		}
		misses := pending[:0]
		for _, id := range pending {
			if ref, ok := atCommit[id]; ok {
				refs[id] = ref
			} else {
				misses = append(misses, id)
			}
		}
		pending = misses
	}

	for start := 0; start < len(pending); start += historyChunk {
		end := min(start+historyChunk, len(pending))
		if err := newestHistoricalExternalRefs(ctx, tx, pending[start:end], asOf, refs); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

func newestCommitAtOrBefore(ctx context.Context, tx DBTX, asOf time.Time) (string, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT commit_hash, date FROM dolt_log
		WHERE date <= ?
		ORDER BY date DESC
		LIMIT 2
	`, asOf.UTC())
	if err != nil {
		return "", false, fmt.Errorf("failed to find newest commit at or before %s: %w", asOf, err)
	}
	defer rows.Close()
	var hashes []string
	var dates []time.Time
	for rows.Next() {
		var hash string
		var date time.Time
		if err := rows.Scan(&hash, &date); err != nil {
			return "", false, fmt.Errorf("failed to scan newest commit: %w", err)
		}
		hashes = append(hashes, hash)
		dates = append(dates, date)
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("failed to find newest commit at or before %s: %w", asOf, err)
	}
	if len(hashes) == 0 {
		return "", false, nil
	}
	return hashes[0], len(dates) == 2 && dates[0].Equal(dates[1]), nil
}

func externalRefsAtCommit(ctx context.Context, tx DBTX, commit string) (map[string]string, error) {
	if err := ValidateRef(commit); err != nil {
		return nil, fmt.Errorf("invalid commit %q: %w", commit, err)
	}
	//nolint:gosec // G201: commit passed ValidateRef; AS OF takes no bind parameter
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT id, external_ref FROM issues AS OF '%s'`, commit))
	if isTableNotExistError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get external refs as of %s: %w", commit, err)
	}
	defer rows.Close()
	refs := make(map[string]string)
	for rows.Next() {
		var id string
		var ref sql.NullString
		if err := rows.Scan(&id, &ref); err != nil {
			return nil, fmt.Errorf("failed to scan external ref as of %s: %w", commit, err)
		}
		refs[id] = ref.String
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to get external refs as of %s: %w", commit, err)
	}
	return refs, nil
}

func newestHistoricalExternalRefs(ctx context.Context, tx DBTX, ids []string, asOf time.Time, refs map[string]string) error {
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, asOf.UTC())
	rows, err := tx.QueryContext(ctx, `
		SELECT id, external_ref, commit_date
		FROM (
			SELECT id, external_ref, commit_date FROM dolt_history_issues
		) h
		WHERE h.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) AND h.commit_date <= ?
	`, args...)
	if err != nil {
		return fmt.Errorf("failed to get previous external_refs: %w", err)
	}
	defer rows.Close()
	newest := make(map[string]time.Time, len(ids))
	for rows.Next() {
		var id string
		var ref sql.NullString
		var date time.Time
		if err := rows.Scan(&id, &ref, &date); err != nil {
			return fmt.Errorf("failed to scan previous external_ref: %w", err)
		}
		// On a commit_date tie, keep the first row in scan order, as the
		// per-issue query's TopN does.
		if at, ok := newest[id]; ok && !date.After(at) {
			continue
		}
		newest[id] = date
		refs[id] = ref.String
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to get previous external_refs: %w", err)
	}
	return nil
}
