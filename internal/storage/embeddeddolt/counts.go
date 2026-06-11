//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// depTargetExpr resolves a dependency row's target id from the split physical
// columns instead of the STORED generated `depends_on_id` column. Both
// `dependencies` and `wisp_dependencies` define depends_on_id as
// GENERATED ALWAYS AS (COALESCE(depends_on_issue_id, depends_on_wisp_id,
// depends_on_external)). Count queries must filter on the base columns: inside
// a count(*) (which projects no real columns) the pure-Go GMS analyzer can
// prune the base columns the generated column derives from and then fail with
// "column depends_on_id could not be found in any table in scope". The slice
// path projects real columns, so it can use depends_on_id directly. This
// matches issueops.DepTargetExpr on main.
const depTargetExpr = "COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)"

func (s *EmbeddedDoltStore) CountIssues(ctx context.Context, query string, filter types.IssueFilter) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		whereClauses, args, err := issueops.BuildIssueFilterClauses(query, filter, issueops.IssuesFilterTables)
		if err != nil {
			return err
		}
		where := ""
		if len(whereClauses) > 0 {
			where = " WHERE " + strings.Join(whereClauses, " AND ")
		}
		//nolint:gosec // table name is a static constant; placeholders are bound
		q := fmt.Sprintf(`SELECT count(*) FROM issues%s`, where)
		return tx.QueryRowContext(ctx, q, args...).Scan(&n)
	})
	return n, err
}

// CountIssuesByGroup returns per-group issue counts. groupBy is one of:
// status, priority, type, assignee, label.
func (s *EmbeddedDoltStore) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	var result map[string]int
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.CountIssuesByGroupInTx(ctx, tx, filter, groupBy)
		return err
	})
	return result, err
}

// CountDependents counts both dependency tables so the total matches
// GetDependentsWithMetadata: a dependent may be a permanent issue (edge in
// `dependencies`) or a wisp (edge in `wisp_dependencies`). Counted in separate
// top-level queries and summed in Go.
//
// Both tables' targets are resolved via depTargetExpr (the split physical
// columns) rather than the STORED generated depends_on_id, which a count(*)
// can fail to resolve under the pure-Go GMS analyzer.
func (s *EmbeddedDoltStore) CountDependents(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var perm, wisp int64
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM dependencies WHERE `+depTargetExpr+` = ?`, issueID).Scan(&perm); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM wisp_dependencies WHERE `+depTargetExpr+` = ?`, issueID).Scan(&wisp); err != nil {
			return err
		}
		n = perm + wisp
		return nil
	})
	return n, err
}

// CountDependencies counts both dependency tables so the total matches
// GetDependenciesWithMetadata: a wisp's outgoing edges live in
// `wisp_dependencies`, a permanent issue's in `dependencies`. Counted as two
// separate queries summed in Go (see CountDependents for why a single combined
// query is avoided).
func (s *EmbeddedDoltStore) CountDependencies(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var perm, wisp int64
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM dependencies WHERE issue_id = ?`, issueID).Scan(&perm); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM wisp_dependencies WHERE issue_id = ?`, issueID).Scan(&wisp); err != nil {
			return err
		}
		n = perm + wisp
		return nil
	})
	return n, err
}

func (s *EmbeddedDoltStore) CountIssueComments(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT count(*) FROM comments WHERE issue_id = ?`, issueID).Scan(&n)
	})
	return n, err
}

func (s *EmbeddedDoltStore) CountAttachments(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT count(*) FROM attachments WHERE issue_id = ?`, issueID).Scan(&n)
	})
	return n, err
}

func (s *EmbeddedDoltStore) CountEvents(ctx context.Context, issueID string, limit int) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT count(*) FROM events WHERE issue_id = ?`, issueID).Scan(&n)
	})
	if err != nil {
		return 0, err
	}
	if limit > 0 && n > int64(limit) {
		n = int64(limit)
	}
	return n, nil
}

// CountDependentsByStatus counts both dependency tables, joining each to its
// home issue table (dependencies→issues, wisp_dependencies→wisps), so wisp
// dependents are included the same way GetDependentsWithMetadata includes them.
// Counted as two separate queries summed in Go (see CountDependents for why a
// single combined query is avoided).
func (s *EmbeddedDoltStore) CountDependentsByStatus(ctx context.Context, issueID string, status types.Status) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var perm, wisp int64
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM dependencies d
			 JOIN issues i ON i.id = d.issue_id
			 WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND i.status = ?`,
			issueID, string(status)).Scan(&perm); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM wisp_dependencies d
			 JOIN wisps w ON w.id = d.issue_id
			 WHERE COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) = ? AND w.status = ?`,
			issueID, string(status)).Scan(&wisp); err != nil {
			return err
		}
		n = perm + wisp
		return nil
	})
	return n, err
}
