// Package dolt — iter_issues.go
//
// Iterator over the issues table. Issue rows and their labels are read inside
// a SINGLE read transaction (one *sql.Conn), then wrapped in a SliceIter.
//
// Why not a streaming cursor with per-row label hydration: server-mode Dolt
// pins each store to MaxOpenConns=1 because branch isolation is session-level
// (DOLT_CHECKOUT applies to the connection, see dolt_test.go setupTestStore).
// A streaming cursor holds that one connection for *sql.Rows, so hydrating
// labels per row needs a SECOND connection that can never be granted — the
// classic pool deadlock (mybd-2pcb). Buffer-then-hydrate keeps everything on
// the cursor's own connection: read all rows, close the cursor, batch-fetch
// labels with WHERE issue_id IN (...), then release the connection before the
// caller walks the slice.
//
// This iterator queries only the `issues` table — wisp routing happens in
// the slice-returning SearchIssues which merges wisps and issues. Callers that
// need wisps stream IterWisps separately.
package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// IterIssues returns issues matching the filter from the `issues` table.
//
// The path queries only the issues table (wisps are returned separately via
// IterWisps). The slice path SearchIssues merges both for backward
// compatibility — that merge needs a seen-set keyed by ID across the full
// issues result set, so it stays separate from this issues-only iterator.
func (s *DoltStore) IterIssues(ctx context.Context, query string, filter types.IssueFilter) (storage.Iter[types.Issue], error) {
	if s.closed.Load() {
		return nil, ErrStoreClosed
	}
	whereClauses, args, err := issueops.BuildIssueFilterClauses(query, filter, issueops.IssuesFilterTables)
	if err != nil {
		return nil, fmt.Errorf("iter issues: build filter: %w", err)
	}
	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}
	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	//nolint:gosec // G201: whereSQL contains column comparisons with ?, limitSQL is a safe integer
	q := fmt.Sprintf(`SELECT %s FROM issues %s %s ORDER BY priority ASC, created_at DESC, id ASC%s`,
		issueops.IssueSelectColumns, sqlbuild.LeaseJoin("issues"), whereSQL, limitSQL)

	var issues []*types.Issue
	txErr := s.withReadTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("iter issues: query: %w", err)
		}
		defer func() { _ = rows.Close() }()
		ids := make([]string, 0)
		for rows.Next() {
			iss, scanErr := issueops.ScanIssueFrom(rows)
			if scanErr != nil {
				return fmt.Errorf("iter issues: scan: %w", scanErr)
			}
			issues = append(issues, iss)
			ids = append(ids, iss.ID)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iter issues: rows: %w", err)
		}
		// A *sql.Tx is bound to one connection, so the cursor must be closed
		// before the label query can run on it (idempotent with the defer).
		_ = rows.Close()
		labelMap, err := issueops.GetLabelsForIssuesFromTableInTx(ctx, tx, "labels", ids)
		if err != nil {
			return fmt.Errorf("iter issues: hydrate labels: %w", err)
		}
		for _, iss := range issues {
			if labels, ok := labelMap[iss.ID]; ok {
				iss.Labels = labels
			}
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return storage.NewSliceIter(issues), nil
}
