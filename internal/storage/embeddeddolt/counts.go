//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// depTargetEqualsOrD is a SARGABLE target-equality predicate as an explicit
// per-column OR, qualified with the "d." alias CountDependentsByStatus's two
// dependency-table joins use, binding the target id once per typed column
// (three args, in that column order): unlike the STORED generated
// depends_on_id column's COALESCE wrapper, which wraps the columns in an
// expression no index can match and which a count(*) can fail to resolve
// under the pure-Go GMS analyzer, each disjunct here is a bare indexed
// column. This mirrors issueops.depTargetEqualsOr() on main.
const depTargetEqualsOrD = "(d.depends_on_issue_id = ? OR d.depends_on_wisp_id = ? OR d.depends_on_external = ?)"

// CountIssues returns the number of issues matching query and filter.
// Filter.Limit and Filter.Offset are ignored; all other fields apply.
// Wisps-merge semantics follow SearchIssues: SkipWisps=true counts the
// durable issues table only, otherwise the wisps tier is merged in (GH#4387).
func (s *EmbeddedDoltStore) CountIssues(ctx context.Context, query string, filter types.IssueFilter) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
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
func (s *EmbeddedDoltStore) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	var result map[string]int
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
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
// the proxied path runs; its target lookups use the SARGABLE
// depTargetEqualsOr predicate rather than the STORED generated
// depends_on_id column's COALESCE form, which defeated the index and
// turned this into a full table scan that grew with total dependency rows
// (GH#7666).
func (s *EmbeddedDoltStore) CountDependents(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		n, err = issueops.CountDependencyEdgesInTx(ctx, tx, issueID, domain.DepDirectionIn, nil)
		return err
	})
	return n, err
}

// CountDependencies returns the number of issues that issueID depends on: one
// per distinct dependency across both planes, matching
// GetDependenciesWithMetadata (see CountDependents).
func (s *EmbeddedDoltStore) CountDependencies(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		n, err = issueops.CountDependencyEdgesInTx(ctx, tx, issueID, domain.DepDirectionOut, nil)
		return err
	})
	return n, err
}

func (s *EmbeddedDoltStore) CountIssueComments(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
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
// Counted as two separate queries summed in Go. Each matches the target on the
// split physical columns (a COALESCE, as sqlbuild.DepTargetExpr) rather than
// the STORED generated depends_on_id, which a count(*) can fail to resolve
// under the pure-Go GMS analyzer.
func (s *EmbeddedDoltStore) CountDependentsByStatus(ctx context.Context, issueID string, status types.Status) (int64, error) {
	var n int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
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
