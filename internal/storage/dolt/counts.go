package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
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

// depTargetEqualsOrD is a SARGABLE target-equality predicate as an explicit
// per-column OR, qualified with the "d." alias CountDependentsByStatus's two
// dependency-table joins use, binding the target id once per typed column
// (three args, in that column order): unlike depTargetExpr's COALESCE
// wrapper, which wraps the columns in an expression no index can match, each
// disjunct is a bare indexed column. The COALESCE form forced a full table
// scan of dependencies/wisp_dependencies that grows with total dependency
// rows rather than with the matched set (GH#7666). This mirrors
// issueops.depTargetEqualsOr() on main; see that function's comment for the
// index-merge planner comparison.
const depTargetEqualsOrD = "(d.depends_on_issue_id = ? OR d.depends_on_wisp_id = ? OR d.depends_on_external = ?)"

// CountIssues returns the number of issues matching query and filter.
// Filter.Limit and Filter.Offset are ignored; all other fields apply.
// Wisps-merge semantics follow SearchIssues: SkipWisps=true counts the
// durable issues table only, otherwise the wisps tier is merged in (GH#4387).
func (s *DoltStore) CountIssues(ctx context.Context, query string, filter types.IssueFilter) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		count, err := issueops.CountIssuesInTx(ctx, tx, query, filter)
		if err != nil {
			return err
		}
		n = int64(count)
		return nil
	})
	return n, err
}

// CountIssuesByGroup returns per-group issue counts. groupBy is one of:
// status, priority, type, assignee, label.
func (s *DoltStore) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	var result map[string]int
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.CountIssuesByGroupInTx(ctx, tx, filter, groupBy)
		return err
	})
	return result, err
}

// CountDependents returns the number of issues that depend on issueID: one per
// distinct dependent across `dependencies` and `wisp_dependencies`, so the
// total matches GetDependentsWithMetadata (bd-cif6y4). An edge held in both
// planes counts once, and a database with no `wisp_dependencies` counts the
// durable plane alone. The body is issueops.CountDependencyEdgesInTx, the one
// the proxied path runs; its target lookups use the SARGABLE depTargetEqualsOr
// predicate rather than depTargetExpr's COALESCE form, which defeated the
// index and turned this into a full table scan that grew with total
// dependency rows (GH#7666).
func (s *DoltStore) CountDependents(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = issueops.CountDependencyEdgesInTx(ctx, tx, issueID, domain.DepDirectionIn, nil)
		return err
	})
	return n, err
}

// CountDependencies returns the number of issues that issueID depends on: one
// per distinct dependency across both planes, matching
// GetDependenciesWithMetadata (see CountDependents).
func (s *DoltStore) CountDependencies(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = issueops.CountDependencyEdgesInTx(ctx, tx, issueID, domain.DepDirectionOut, nil)
		return err
	})
	return n, err
}

// CountIssueComments returns the number of comments on an issue.
func (s *DoltStore) CountIssueComments(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		// Route by wisp status exactly as GetIssueComments does, so the count
		// and the rows it summarizes read the same table (GH#5565).
		table := "comments"
		if issueops.IsActiveWispInTx(ctx, tx, issueID) {
			table = "wisp_comments"
		}
		//nolint:gosec // G201: table is hardcoded
		return tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT count(*) FROM %s WHERE issue_id = ?`, table), issueID).Scan(&n)
	})
	return n, err
}

// CountEvents returns the number of audit events for an issue, capped at limit
// (or unbounded if limit == 0).
func (s *DoltStore) CountEvents(ctx context.Context, issueID string, limit int) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
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

// CountDependentsByStatus returns the number of issues that depend on issueID
// and are in the given status. Counts both dependency tables, joining each to
// its home issue table (dependencies→issues, wisp_dependencies→wisps), so wisp
// dependents are included the same way GetDependentsWithMetadata includes them.
// Counted as two separate queries summed in Go, each matching the target on
// the split physical columns (see depTargetExpr for why a count(*) cannot use
// the generated depends_on_id).
func (s *DoltStore) CountDependentsByStatus(ctx context.Context, issueID string, status types.Status) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var perm, wisp int64
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM dependencies d
			 JOIN issues i ON i.id = d.issue_id
			 WHERE `+depTargetEqualsOrD+` AND i.status = ?`,
			issueID, issueID, issueID, string(status)).Scan(&perm); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM wisp_dependencies d
			 JOIN wisps w ON w.id = d.issue_id
			 WHERE `+depTargetEqualsOrD+` AND w.status = ?`,
			issueID, issueID, issueID, string(status)).Scan(&wisp); err != nil {
			return err
		}
		n = perm + wisp
		return nil
	})
	return n, err
}
