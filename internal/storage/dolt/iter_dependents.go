// Package dolt — iter_dependents.go
//
// Streaming iterator over the issues × dependencies join. Same dedicated-
// conn pattern as iter_issues.go — labels (when needed) hydrate on a
// second pool connection so the cursor never deadlocks.
//
// Both dependency tables are included so results match the slice path
// GetDependentsWithMetadata, which the human-readable `bd show` uses.
// IterDependentsWithMetadata UNION ALLs `dependencies` (permanent-source
// edges, joined to `issues`) with `wisp_dependencies` (wisp-source edges,
// joined to `wisps`). WispTableRouting routes every edge to one table by its
// source's wisp status, so each edge table joins cleanly to its source. A pull
// can still land a durable edge whose dependent also has a `wisps` row, which
// the join cannot read the slice path's way; IterDependentsWithMetadata
// streams the slice path's answer then. Otherwise no dependent is in both
// legs: one that both legs join has a durable edge and a `wisps` row.
// IterDependenciesWithMetadata delegates to the slice path: a dependency's
// *target* may be permanent or wisp independent of which table holds the
// edge, which a single streaming join cannot resolve, and the method has no
// streaming caller today.
package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// doltDependentsIter streams the issues × dependency join, returning each
// row as an IssueWithDependencyMetadata. Used by both IterDependents-
// WithMetadata and IterDependenciesWithMetadata; the SQL differs only in
// which side of the dependencies join is bound to issueID.
type doltDependentsIter struct {
	s      *DoltStore
	conn   *sql.Conn
	rows   *sql.Rows
	cur    *types.IssueWithDependencyMetadata
	err    error
	closed bool
}

// IterDependentsWithMetadata streams dependents (issues that depend on
// issueID) with the relationship type attached. Includes both permanent
// (`dependencies`) and wisp (`wisp_dependencies`) dependents so the stream
// matches GetDependentsWithMetadata. See the package doc for why the join
// target per edge table is unambiguous, and for the state it streams the
// slice path's answer in.
func (s *DoltStore) IterDependentsWithMetadata(ctx context.Context, issueID string) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	// A durable edge whose dependent also has a wisps row (another clone's
	// promotion of a wisp, pulled next to the local wisp copy) is one the join
	// cannot read the slice path's way. The slice path lists that dependent
	// once, with the durable edge's type and the wisps row, which is canonical
	// (be-iabdi); the durable leg would stream its issues row, and the wisp leg
	// could stream it again. So the stream is the slice there, as it always is
	// on the embedded store, and `bd show --json --include-dependents` reads
	// each dependent from the row text `bd show` lists it from (bd-cif6y4). bd
	// doctor reports the state as an error, so only a database in it pays for
	// the slice. The probe and the join are two statements: a pull landing
	// between them can still show one call the join's answer.
	held, err := s.durableDependentHeldAsWisp(ctx, issueID)
	if err != nil {
		return nil, err
	}
	if held {
		deps, err := s.GetDependentsWithMetadata(ctx, issueID)
		if err != nil {
			return nil, err
		}
		return storage.NewSliceIter(deps), nil
	}
	durable := fmt.Sprintf(`
		SELECT %s, d.type
		FROM issues i
		JOIN dependencies d ON d.issue_id = i.id
		%s
		WHERE %s = ?`, prefixedIssueColumns("i"), sqlbuild.LeaseJoin("i"), depTargetExprWithAlias("d"))
	q := fmt.Sprintf(`%s
		UNION ALL
		SELECT %s, d.type
		FROM wisps w
		JOIN wisp_dependencies d ON d.issue_id = w.id
		%s
		WHERE %s = ?
		ORDER BY created_at ASC
	`, durable, prefixedIssueColumns("w"), sqlbuild.LeaseJoin("w"), depTargetExprWithAlias("d"))
	it, err := s.iterIssuesWithDepType(ctx, q, issueID, issueID)
	// The wisp plane is dolt-ignored, so a working-set reset can drop wisps or
	// wisp_dependencies from a server database (bd-cif6y4). Its absence means
	// there are no wisp dependents: stream the durable leg alone, as the slice
	// path does. Any other missing table is a broken database and stays an error.
	if name, ok := dberrors.MissingTableName(err); ok && sqlbuild.OptionalWispTable(name) {
		return s.iterIssuesWithDepType(ctx, durable+"\n\t\tORDER BY created_at ASC", issueID)
	}
	return it, err
}

// durableDependentHeldAsWisp reports whether a durable edge into issueID has a
// dependent that also holds a wisps row. A missing wisps table holds none.
func (s *DoltStore) durableDependentHeldAsWisp(ctx context.Context, issueID string) (bool, error) {
	var held bool
	err := s.queryRowContext(ctx, func(row *sql.Row) error {
		var one int
		switch err := row.Scan(&one); {
		case err == nil:
			held = true
		case dberrors.IsNoRows(err), dberrors.IsMissingTable(err, "wisps"):
			// No such dependent, or no wisp plane to hold one.
		default:
			return err
		}
		return nil
	}, fmt.Sprintf(`
		SELECT 1
		FROM dependencies d
		JOIN wisps w ON w.id = d.issue_id
		WHERE %s = ?
		LIMIT 1`, depTargetExprWithAlias("d")), issueID)
	if err != nil {
		return false, fmt.Errorf("iter dependents: probe for wisp copies: %w", err)
	}
	return held, nil
}

func depTargetExprWithAlias(alias string) string {
	if alias == "" {
		return depTargetExpr
	}
	return fmt.Sprintf("COALESCE(%s.depends_on_issue_id, %s.depends_on_wisp_id, %s.depends_on_external)", alias, alias, alias)
}

// IterDependenciesWithMetadata streams dependencies (issues issueID depends
// on) with the relationship type attached. It delegates to the slice path
// GetDependenciesWithMetadata (which resolves targets across both `issues`
// and `wisps`) rather than a streaming join, because a dependency's target
// table cannot be determined from the edge table alone. There is no streaming
// caller for this direction today; revisit if one appears.
func (s *DoltStore) IterDependenciesWithMetadata(ctx context.Context, issueID string) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	deps, err := s.GetDependenciesWithMetadata(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(deps), nil
}

func (s *DoltStore) iterIssuesWithDepType(ctx context.Context, q string, args ...any) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	if s.closed.Load() {
		return nil, ErrStoreClosed
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("iter dependents: acquire conn: %w", err)
	}
	rows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("iter dependents: query: %w", err)
	}
	return &doltDependentsIter{s: s, conn: conn, rows: rows}, nil
}

func (it *doltDependentsIter) Next(ctx context.Context) bool {
	if it.err != nil || it.closed {
		return false
	}
	if err := ctx.Err(); err != nil {
		it.err = err
		return false
	}
	if !it.rows.Next() {
		it.err = it.rows.Err()
		return false
	}
	iss, depType, err := scanIssueWithDepTypeFrom(it.rows)
	if err != nil {
		it.err = fmt.Errorf("iter dependents: scan: %w", err)
		return false
	}
	// NOTE: labels are NOT hydrated here. The corresponding slice path
	// (GetDependentsWithMetadata in dolt/dependencies.go) does not hydrate
	// either, so this matches for parity. Callers that need labels on
	// dependents must call GetLabels(id) explicitly.
	it.cur = &types.IssueWithDependencyMetadata{
		Issue:          *iss,
		DependencyType: types.DependencyType(depType),
	}
	return true
}

func (it *doltDependentsIter) Value() *types.IssueWithDependencyMetadata { return it.cur }
func (it *doltDependentsIter) Err() error                                { return it.err }

func (it *doltDependentsIter) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	if it.rows != nil {
		_ = it.rows.Close()
	}
	if it.conn != nil {
		_ = it.conn.Close()
	}
	return nil
}

// prefixedIssueColumns returns IssueSelectColumns with each issues-row column
// prefixed by the given table alias (e.g. "i") so it can be used in JOIN
// queries. The lease overlay columns keep their own leases. qualifier — the
// caller's FROM must include sqlbuild.LeaseJoin(alias).
func prefixedIssueColumns(alias string) string {
	cols := splitCommaList(sqlbuild.IssueBaseColumns)
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return joinCommaList(cols) + ", " + sqlbuild.LeaseSelectColumns
}

// splitCommaList splits a multi-line comma-separated column list into
// trimmed, non-empty entries. Used by prefixedIssueColumns.
func splitCommaList(s string) []string {
	out := make([]string, 0, 32)
	cur := make([]byte, 0, 32)
	flush := func() {
		t := trimSpaces(cur)
		if len(t) > 0 {
			out = append(out, string(t))
		}
		cur = cur[:0]
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ',' {
			flush()
			continue
		}
		cur = append(cur, c)
	}
	flush()
	return out
}

func joinCommaList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	n := 0
	for _, it := range items {
		n += len(it) + 2
	}
	out := make([]byte, 0, n)
	for i, it := range items {
		if i > 0 {
			out = append(out, ',', ' ')
		}
		out = append(out, it...)
	}
	return string(out)
}

func trimSpaces(b []byte) []byte {
	start, end := 0, len(b)
	for start < end {
		c := b[start]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			start++
			continue
		}
		break
	}
	for end > start {
		c := b[end-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			end--
			continue
		}
		break
	}
	return b[start:end]
}

// scanIssueWithDepTypeFrom scans a row that contains all IssueSelectColumns
// followed by a single dep_type column. Mirrors ScanIssueFrom from issueops
// but with the trailing dep_type field.
func scanIssueWithDepTypeFrom(rows *sql.Rows) (*types.Issue, string, error) {
	// Reuse ScanIssueFrom by wrapping rows in a Scan adapter that consumes
	// the trailing dep_type. ScanIssueFrom expects the exact column count
	// of IssueSelectColumns; the JOIN query selects one extra column, so we
	// bind the trailing field explicitly.
	var depType string
	rowAdapter := &issueWithExtraScan{rows: rows, extra: []any{&depType}}
	iss, err := issueops.ScanIssueFrom(rowAdapter)
	if err != nil {
		return nil, "", err
	}
	return iss, depType, nil
}

// issueWithExtraScan adapts a *sql.Rows so ScanIssueFrom consumes the
// IssueSelectColumns AND the trailing dep_type column in one Scan call.
type issueWithExtraScan struct {
	rows  *sql.Rows
	extra []any
}

func (a *issueWithExtraScan) Scan(dest ...any) error {
	all := make([]any, 0, len(dest)+len(a.extra))
	all = append(all, dest...)
	all = append(all, a.extra...)
	return a.rows.Scan(all...)
}
