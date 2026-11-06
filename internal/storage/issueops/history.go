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

// externalRefHistoryChunk is one PreviousExternalRefsInTx query's id count.
// Both IN lists of the chunk query grow linearly in the id count, and with a
// whole pull's linked locals in one query the planner cost of evaluating
// those lists starts to rival the scan itself. 16 keeps each query small
// while a pull still asks only ceil(N/16) of them, all inside the caller's
// single transaction. HistoricalIssueIDsInTx chunks the same table for the
// same reason.
const externalRefHistoryChunk = 16

// PreviousExternalRefsInTx answers PreviousExternalRefInTx's question for
// many issue ids in one pass: the returned map holds, for every requested id
// that has a history entry at or before asOf, the external_ref recorded as
// of its most recent commit at or before asOf. An id absent from the map has
// no such entry (the single-id call's found=false); a present empty string
// is a NULL or empty historical ref. Duplicate and empty ids are ignored,
// and empty input returns an empty map without querying.
//
// The deduplicated ids are read in chunks of externalRefHistoryChunk —
// ceil(N/chunk) queries in all, each carrying at most chunk ids in either
// IN list — on the caller's one transaction; the chunk size bounds a single
// query's shape without reopening per-chunk work for the caller.
//
// Each id is resolved through the same latest-qualifying-commit-date rows
// the single-id helper reads. When an id's greatest qualifying date is tied
// across commits — which dolt_history_issues makes ordinary, since it holds
// one row per commit per issue and separate commits can share a date — the
// date-only ordering does not define a winner, so the id is resolved through
// the unchanged PreviousExternalRefInTx in this same transaction rather than
// by picking arbitrarily among the tied rows. That keeps the batch answer
// byte-for-byte the single-id answer, whatever tie the engine happens to
// break first.
//
// On any query, iteration, scan or close failure the whole partial map is
// discarded.
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
	if len(unique) == 0 {
		return refs, nil
	}
	for start := 0; start < len(unique); start += externalRefHistoryChunk {
		end := start + externalRefHistoryChunk
		if end > len(unique) {
			end = len(unique)
		}
		if err := previousExternalRefsForIDsInTx(ctx, tx, unique[start:end], asOf, refs); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// previousExternalRefsForIDsInTx resolves one chunk of ids — already
// deduplicated and non-empty — into refs through one derived-table
// MAX(commit_date) join: the inner subquery finds each id's greatest
// qualifying commit_date, and the outer side returns the rows holding those
// dates. Both sides restrict to the requested ids and the cutoff, and ids
// and the cutoff are bound as parameters, never inlined. The subquery
// wrappers avoid Dolt's max1Row optimization on PK lookups, as HistoryInTx
// does.
func previousExternalRefsForIDsInTx(ctx context.Context, tx *sql.Tx, ids []string, asOf time.Time, refs map[string]string) error {
	args := make([]any, 0, 2*len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, asOf.UTC())
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, asOf.UTC())

	// #nosec G202 -- the concatenation inserts only IN-list placeholder
	// marks built by strings.Repeat; every id and the cutoff are bound as
	// query parameters, never inlined
	query := `
		SELECT h.id, h.external_ref
		FROM (SELECT id, external_ref, commit_date FROM dolt_history_issues) h
		JOIN (
			SELECT m.id, MAX(m.commit_date) AS latest_date
			FROM (SELECT id, commit_date FROM dolt_history_issues) m
			WHERE m.id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `) AND m.commit_date <= ?
			GROUP BY m.id
		) latest ON latest.id = h.id AND latest.latest_date = h.commit_date
		WHERE h.id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `) AND h.commit_date <= ?`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to get previous external refs: %w", err)
	}

	// Collect every row before any tie resolution: the result set must be
	// fully consumed and closed before the single-id helper runs, so the
	// ties are detected from materialized rows rather than streaming state.
	tied := make(map[string]struct{})
	for rows.Next() {
		var id string
		var ref sql.NullString
		if err := rows.Scan(&id, &ref); err != nil {
			_ = rows.Close()
			return fmt.Errorf("failed to scan previous external refs: %w", err)
		}
		if _, already := refs[id]; already {
			// A second row at this id's greatest qualifying date: an
			// equal-date tie the date-only ordering cannot break.
			tied[id] = struct{}{}
			continue
		}
		refs[id] = ref.String
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("failed to iterate previous external refs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("failed to close previous external refs: %w", err)
	}

	for id := range tied {
		ref, found, err := PreviousExternalRefInTx(ctx, tx, id, asOf)
		if err != nil {
			return fmt.Errorf("failed to resolve tied history for %s: %w", id, err)
		}
		if found {
			refs[id] = ref
		} else {
			delete(refs, id)
		}
	}
	return nil
}
