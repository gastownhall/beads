package issueops

import (
	"context"
	"fmt"
	"strings"
)

// historicalIssueIDsChunk caps how many ids go into one IN-list. Dolt's
// history tables fan a row out per commit per id, and very wide IN-lists are
// where its planner starts choosing badly; the callers' own candidate cap is
// far below this, so chunking is a floor, not a hot path.
const historicalIssueIDsChunk = 200

// HistoricalIssueIDsInTx returns the subset of ids that appear anywhere in the
// current branch's committed history of the issues table.
//
// dolt_history_issues walks HEAD's ancestry, so an id whose only commits were
// rewound away (DOLT_RESET --hard, a checkout, a force-pushed rewrite) is NOT
// reported, and neither is an id that exists only in the uncommitted working
// set. Both omissions are load-bearing: see storage.HistoryPresence.
//
// The subquery wrapper is the same workaround HistoryInTx uses — Dolt's
// planner assumes a predicate on the PK of a dolt_history_* table matches at
// most one row, which is false there (one row per commit per id).
func HistoricalIssueIDsInTx(ctx context.Context, tx DBTX, ids []string) (map[string]struct{}, error) {
	present := make(map[string]struct{})
	if len(ids) == 0 {
		return present, nil
	}

	// Dedupe up front so a caller's repeated id can't inflate a chunk.
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

	for start := 0; start < len(unique); start += historicalIssueIDsChunk {
		end := start + historicalIssueIDsChunk
		if end > len(unique) {
			end = len(unique)
		}
		if err := historicalIssueIDChunkInTx(ctx, tx, unique[start:end], present); err != nil {
			return nil, err
		}
	}
	return present, nil
}

func historicalIssueIDChunkInTx(ctx context.Context, tx DBTX, chunk []string, present map[string]struct{}) error {
	args := make([]any, len(chunk))
	for i, id := range chunk {
		args[i] = id
	}
	query := `
		SELECT DISTINCT h.id
		FROM (
			SELECT id FROM dolt_history_issues
		) h
		WHERE h.id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",") + `)`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query issue history presence: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan issue history presence: %w", err)
		}
		present[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate issue history presence: %w", err)
	}
	return nil
}
