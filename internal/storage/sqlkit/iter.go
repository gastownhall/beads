package sqlkit

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// IterIssues returns issues matching the filter from the `issues` table only.
//
// Unlike SearchIssues (which merges the wisps tier), this iterator is
// contractually issues-table-only; callers that need wisps stream IterWisps
// separately. Rows and their labels are read inside one read tx, then wrapped
// in a SliceIter (buffer-then-hydrate keeps everything on one connection).
func (s *Store) IterIssues(ctx context.Context, query string, filter types.IssueFilter) (storage.Iter[types.Issue], error) {
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
	q := fmt.Sprintf(`SELECT %s FROM issues %s ORDER BY priority ASC, created_at DESC, id ASC%s`,
		issueops.IssueSelectColumns, whereSQL, limitSQL)

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

// IterDependentsWithMetadata streams the dependents of an issue (both tables).
// The underlying slice path (GetDependentsWithMetadata) scans both dependency
// tables unordered, so we sort here to match dolt/iter_dependents.go's
// `ORDER BY created_at ASC` (the dependent issue's created_at), tiebreaking on
// ID for stable, deterministic output across backends.
func (s *Store) IterDependentsWithMetadata(ctx context.Context, issueID string) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	ds, err := s.GetDependentsWithMetadata(ctx, issueID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(ds, func(i, j int) bool {
		if !ds[i].CreatedAt.Equal(ds[j].CreatedAt) {
			return ds[i].CreatedAt.Before(ds[j].CreatedAt)
		}
		return ds[i].ID < ds[j].ID
	})
	return storage.NewSliceIter(ds), nil
}

// IterDependenciesWithMetadata streams the dependencies of an issue (both tables).
func (s *Store) IterDependenciesWithMetadata(ctx context.Context, issueID string) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	ds, err := s.GetDependenciesWithMetadata(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(ds), nil
}

// IterIssueComments streams the comments on an issue.
func (s *Store) IterIssueComments(ctx context.Context, issueID string) (storage.Iter[types.Comment], error) {
	cs, err := s.GetIssueComments(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(cs), nil
}

// IterEvents streams the audit-trail events for an issue.
func (s *Store) IterEvents(ctx context.Context, issueID string, limit int) (storage.Iter[types.Event], error) {
	ev, err := s.GetEvents(ctx, issueID, limit)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(ev), nil
}

// IterReadyWork streams ready-work issues.
func (s *Store) IterReadyWork(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.Issue], error) {
	is, err := s.GetReadyWork(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(is), nil
}

// IterBlockedIssues streams blocked issues.
func (s *Store) IterBlockedIssues(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.BlockedIssue], error) {
	bs, err := s.GetBlockedIssues(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(bs), nil
}

// IterWisps streams ephemeral issues matching the filter.
func (s *Store) IterWisps(ctx context.Context, filter types.WispFilter) (storage.Iter[types.Issue], error) {
	ws, err := s.ListWisps(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(ws), nil
}
