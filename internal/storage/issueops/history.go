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

// previousExternalRefsChunk caps how many ids one PreviousExternalRefsInTx
// history query binds, which bounds how long one statement scans
// dolt_history_issues on a long history. It bounds each statement, not the
// total: #7272 measured a wider IN-list as cheaper per id (a 16-id history
// read at 1.8 times one per-issue read, a 200-id read at 11 times, on a
// 19.6k-commit store).
const previousExternalRefsChunk = 16

// PreviousExternalRefsInTx is the batch form of PreviousExternalRefInTx: for
// each of issueIDs it returns the external_ref recorded as of the most recent
// commit at or before asOf. An id with no history entry at or before asOf is
// absent from the map; a historical NULL external_ref is present as "".
// Empty and repeated ids are ignored.
//
// Each chunk of at most previousExternalRefsChunk ids is one history query
// that picks every row at the id's greatest qualifying commit_date. When
// more than one row shares that date, PreviousExternalRefInTx breaks the tie
// in the same transaction, exactly as a per-issue lookup would, so the batch
// never picks a different winner than the single-id query. On any error the
// whole map is discarded.
func PreviousExternalRefsInTx(ctx context.Context, tx *sql.Tx, issueIDs []string, asOf time.Time) (map[string]string, error) {
	refs := make(map[string]string)
	unique := make([]string, 0, len(issueIDs))
	seen := make(map[string]struct{}, len(issueIDs))
	for _, id := range issueIDs {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	for start := 0; start < len(unique); start += previousExternalRefsChunk {
		end := min(start+previousExternalRefsChunk, len(unique))
		if err := previousExternalRefsChunkInTx(ctx, tx, unique[start:end], asOf, refs); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

func previousExternalRefsChunkInTx(ctx context.Context, tx *sql.Tx, chunk []string, asOf time.Time, refs map[string]string) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
	args := make([]any, 0, 2*len(chunk)+2)
	for _, id := range chunk {
		args = append(args, id)
	}
	args = append(args, asOf.UTC())
	args = append(args, args...)

	// The derived tables are the max1Row workaround PreviousExternalRefInTx
	// uses; both sides are restricted to the chunk and the cutoff.
	//nolint:gosec // G202: placeholders is only "?" markers; every id is a bound argument
	rows, err := tx.QueryContext(ctx, `
		SELECT h.id, h.external_ref
		FROM (
			SELECT id, external_ref, commit_date FROM dolt_history_issues
		) h
		JOIN (
			SELECT m.id, MAX(m.commit_date) AS latest_date
			FROM (
				SELECT id, commit_date FROM dolt_history_issues
			) m
			WHERE m.id IN (`+placeholders+`) AND m.commit_date <= ?
			GROUP BY m.id
		) latest ON latest.id = h.id AND latest.latest_date = h.commit_date
		WHERE h.id IN (`+placeholders+`) AND h.commit_date <= ?
	`, args...)
	if err != nil {
		return fmt.Errorf("failed to get previous external_refs: %w", err)
	}

	latest := make(map[string]string, len(chunk))
	var tied []string
	for rows.Next() {
		var id string
		var ref sql.NullString
		if err := rows.Scan(&id, &ref); err != nil {
			_ = rows.Close()
			return fmt.Errorf("failed to scan previous external_ref: %w", err)
		}
		if _, dup := latest[id]; dup {
			tied = append(tied, id)
			continue
		}
		latest[id] = ref.String
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("failed to read previous external_refs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("failed to close previous external_refs: %w", err)
	}

	// Rows sharing the latest date have no defined order in either query;
	// resolve them through the single-id lookup rather than picking one here.
	for _, id := range tied {
		if _, done := refs[id]; done {
			continue
		}
		ref, found, err := PreviousExternalRefInTx(ctx, tx, id, asOf)
		if err != nil {
			return err
		}
		if found {
			refs[id] = ref
		}
		delete(latest, id)
	}
	for id, ref := range latest {
		refs[id] = ref
	}
	return nil
}
