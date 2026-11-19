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

// previousExternalRefsChunk caps how many ids go into one dolt_history_issues
// IN-list, matching historicalIssueIDsChunk: Dolt's history tables fan a row
// out per commit per id, and very wide IN-lists are where its planner
// starts choosing badly. A pull with more linked issues than this reads
// history once more per extra chunk, not once per issue.
const previousExternalRefsChunk = historicalIssueIDsChunk

// PreviousExternalRefsInTx is the batch form of PreviousExternalRefInTx: it
// resolves, for every id in ids, the external_ref recorded as of the most
// recent commit at or before asOf, reading dolt_history_issues once per
// 200-id chunk (ceil(U/200) queries for U unique non-empty ids) instead of
// once per issue. The returned map holds an entry only for ids with a
// committed history row at or before asOf — the single-id form's
// found=false — and a present entry is the empty string when the historical
// row's external_ref column was NULL. Empty ids are dropped and duplicates
// collapse; empty input returns an empty map without querying. Any query,
// scan or iteration failure returns the error and no map, never a partial
// answer and never a per-id fallback.
//
// The subquery wrapper is the same workaround HistoryInTx and
// PreviousExternalRefInTx use: Dolt's planner assumes a predicate on the PK
// of a dolt_history_* table matches at most one row, which is false there.
func PreviousExternalRefsInTx(ctx context.Context, tx DBTX, ids []string, asOf time.Time) (map[string]string, error) {
	refs := make(map[string]string)
	unique := uniqueNonEmptyHistoryIDs(ids)
	if len(unique) == 0 {
		return refs, nil
	}
	for start := 0; start < len(unique); start += previousExternalRefsChunk {
		end := start + previousExternalRefsChunk
		if end > len(unique) {
			end = len(unique)
		}
		if err := previousExternalRefsChunkInTx(ctx, tx, unique[start:end], asOf, refs); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// previousExternalRefsChunkInTx resolves one chunk of ids into refs. Each
// chunk's rows arrive ordered id then commit_date descending, so the first
// row per id is its newest committed value at or before asOf; later rows of
// an already-answered id are skipped. rows is closed before the next chunk
// runs.
func previousExternalRefsChunkInTx(ctx context.Context, tx DBTX, chunk []string, asOf time.Time, refs map[string]string) error {
	args := make([]any, 0, len(chunk)+1)
	for _, id := range chunk {
		args = append(args, id)
	}
	args = append(args, asOf.UTC())
	query := `
		SELECT h.id, h.external_ref
		FROM (
			SELECT id, external_ref, commit_date FROM dolt_history_issues
		) h
		WHERE h.id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",") + `) AND h.commit_date <= ?
		ORDER BY h.id, h.commit_date DESC`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query previous external refs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var ref sql.NullString
		if err := rows.Scan(&id, &ref); err != nil {
			return fmt.Errorf("scan previous external ref: %w", err)
		}
		if _, answered := refs[id]; answered {
			continue
		}
		refs[id] = ref.String
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate previous external refs: %w", err)
	}
	return nil
}

// uniqueNonEmptyHistoryIDs returns ids without empties or duplicates, in
// first-seen order, for the batched history helpers.
func uniqueNonEmptyHistoryIDs(ids []string) []string {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}
