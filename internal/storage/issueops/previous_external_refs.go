package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// previousExternalRefsChunk caps how many ids go into one history IN-list,
// for the same reason historicalIssueIDsChunk does: dolt_history_issues fans
// a row out per commit per id, and very wide IN-lists are where Dolt's
// planner starts choosing badly.
const previousExternalRefsChunk = historicalIssueIDsChunk

// PreviousExternalRefsInTx answers PreviousExternalRefInTx for many ids at
// once: for each id it returns the external_ref recorded by the most recent
// commit at or before asOf. It runs one dolt_history_issues query per
// previousExternalRefsChunk unique ids instead of one per id, which is what a
// tracker pull asks for every linked issue at the same lastSync (GH#6605).
//
// An id is absent from the map when it has no history row at or before asOf
// (created later, never committed, or its history was rewritten); a present
// empty string is a NULL or empty external_ref. An issue deleted after it was
// committed keeps the value its last committed row held. Empty ids are
// dropped, repeated ids are asked once, and no ids means no query. Any query,
// scan or iteration failure returns an error and no partial map.
//
// The predicate and order match PreviousExternalRefInTx, including the
// subquery wrapper that keeps Dolt's max1Row PK optimization away from a
// table with one row per commit per id. Rows with equal commit dates have no
// defined winner, as there.
func PreviousExternalRefsInTx(ctx context.Context, tx DBTX, ids []string, asOf time.Time) (map[string]string, error) {
	refs := make(map[string]string)
	unique := uniqueNonEmptyIDs(ids)
	for start := 0; start < len(unique); start += previousExternalRefsChunk {
		end := min(start+previousExternalRefsChunk, len(unique))
		if err := previousExternalRefsChunkInTx(ctx, tx, unique[start:end], asOf.UTC(), refs); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

func uniqueNonEmptyIDs(ids []string) []string {
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

func previousExternalRefsChunkInTx(ctx context.Context, tx DBTX, chunk []string, asOf time.Time, refs map[string]string) error {
	args := make([]any, 0, len(chunk)+1)
	for _, id := range chunk {
		args = append(args, id)
	}
	args = append(args, asOf)
	query := `
		SELECT h.id, h.external_ref
		FROM (
			SELECT id, external_ref, commit_date FROM dolt_history_issues
		) h
		WHERE h.id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",") + `) AND h.commit_date <= ?
		ORDER BY h.id, h.commit_date DESC`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to get previous external_refs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var ref sql.NullString
		if err := rows.Scan(&id, &ref); err != nil {
			return fmt.Errorf("scan previous external_ref: %w", err)
		}
		// Rows come newest first within each id: the first one decides.
		if _, done := refs[id]; done {
			continue
		}
		refs[id] = ref.String
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate previous external_refs: %w", err)
	}
	return rows.Close()
}
