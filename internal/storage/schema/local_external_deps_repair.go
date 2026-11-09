package schema

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// localExternalDepPlanes are the dependency tables ignored migration 0032's
// repair scans, each with whether its sources are wisps.
var localExternalDepPlanes = []struct {
	table      string
	wispSource bool
}{
	{table: "dependencies"},
	{table: "wisp_dependencies", wispSource: true},
}

// localExternalDepsNoticeOut is where the repair names the rows it kept
// external. Like preflipEventsNoticeOut it is deliberately NOT
// terminal-gated: a piped or served open is where the user would otherwise
// never learn a legacy edge still does not block.
var localExternalDepsNoticeOut io.Writer = os.Stderr

// SetLocalExternalDepsNoticeOutForTest redirects the repair's notices to w
// and returns a function that restores the previous writer. Test-only.
func SetLocalExternalDepsNoticeOutForTest(w io.Writer) func() {
	prev := localExternalDepsNoticeOut
	localExternalDepsNoticeOut = w
	return func() { localExternalDepsNoticeOut = prev }
}

// localExternalDep is one depends_on_external row the repair may type.
type localExternalDep struct {
	id, source, target, depType string
}

// typeLocalExternalDependencyTargets is the pre-migration repair for ignored
// migration 0032 (gastownhall/beads#8299). Before the fix a cross-prefix
// target was filed in depends_on_external by prefix alone, even when this
// database held it, so the edge never blocked. Each such row is classified the
// way a new edge is (issueops.ClassifyDepTargetInTx: an "external:" reference
// or an absent target stays external, a present wisp or issue is typed as
// one) and its target moves to the typed column, unless the edge is a block
// on the source's own ancestor or descendant, which a new edge is refused
// for: that row stays external with a notice. A typed row keeps its id, which
// depid derives from the target string, and its provenance. The converted
// sources and their descendants then have is_blocked recomputed, which keeps
// updated_at.
func typeLocalExternalDependencyTargets(ctx context.Context, db DBConn) error {
	var affectedIssues, affectedWisps []string
	for _, plane := range localExternalDepPlanes {
		exists, err := schemaTableExists(ctx, db, plane.table)
		if err != nil {
			return fmt.Errorf("check %s exists: %w", plane.table, err)
		}
		if !exists {
			continue
		}
		rows, err := readLocalExternalDeps(ctx, db, plane.table)
		if err != nil {
			return err
		}
		for _, row := range rows {
			dep := &types.Dependency{IssueID: row.source, DependsOnID: row.target, Type: types.DependencyType(row.depType)}
			kind, _, err := issueops.ClassifyDepTargetInTx(ctx, db, dep, true)
			if err != nil {
				return fmt.Errorf("classify %s target %s -> %s: %w", plane.table, row.source, row.target, err)
			}
			if kind == issueops.DepTargetExternal {
				continue
			}
			// A new edge onto the source's own ancestor or descendant is
			// refused; typing a legacy one would turn it into a block that
			// never clears, so it stays external and the user is told how to
			// drop it.
			if err := issueops.CheckBlockingHierarchyInTx(ctx, db, dep, nil); err != nil {
				var conflict *domain.DependencyHierarchyConflictError
				if !errors.As(err, &conflict) {
					return fmt.Errorf("check hierarchy of %s -> %s: %w", row.source, row.target, err)
				}
				fmt.Fprintf(localExternalDepsNoticeOut,
					"Kept dependency %s -> %s external: %v. Remove it with: bd dep remove %s %s\n",
					row.source, row.target, conflict, row.source, row.target)
				continue
			}
			//nolint:gosec // G201: table from localExternalDepPlanes; column from DepTargetKind.Column().
			if _, err := db.ExecContext(ctx,
				fmt.Sprintf("UPDATE %s SET %s = ?, depends_on_external = NULL WHERE id = ?", plane.table, kind.Column()),
				row.target, row.id); err != nil {
				return fmt.Errorf("type %s target %s -> %s: %w", plane.table, row.source, row.target, err)
			}
			var issues, wisps []string
			if plane.wispSource {
				issues, wisps, err = issueops.AffectedByDepChangeForWispInTx(ctx, db, row.source, row.target, dep.Type)
			} else {
				issues, wisps, err = issueops.AffectedByDepChangeInTx(ctx, db, row.source, row.target, dep.Type)
			}
			if err != nil {
				return fmt.Errorf("affected by typing %s -> %s: %w", row.source, row.target, err)
			}
			affectedIssues = append(affectedIssues, issues...)
			affectedWisps = append(affectedWisps, wisps...)
		}
	}
	if err := issueops.RecomputeIsBlockedInTx(ctx, db, uniqueStrings(affectedIssues), uniqueStrings(affectedWisps)); err != nil {
		return fmt.Errorf("recompute is_blocked after typing local dependency targets: %w", err)
	}
	return nil
}

// readLocalExternalDeps reads every row of table whose depends_on_external is
// not an "external:" reference, closing the cursor before any write.
//
//nolint:gosec // G201: table from localExternalDepPlanes.
func readLocalExternalDeps(ctx context.Context, db DBConn, table string) ([]localExternalDep, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		"SELECT id, issue_id, depends_on_external, type FROM %s WHERE depends_on_external IS NOT NULL AND depends_on_external NOT LIKE 'external:%%' ORDER BY id",
		table))
	if err != nil {
		return nil, fmt.Errorf("read external targets of %s: %w", table, err)
	}
	defer rows.Close()
	var out []localExternalDep
	for rows.Next() {
		var r localExternalDep
		if err := rows.Scan(&r.id, &r.source, &r.target, &r.depType); err != nil {
			return nil, fmt.Errorf("scan external targets of %s: %w", table, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read external targets of %s: %w", table, err)
	}
	return out, nil
}

// uniqueStrings returns ids without repeats, in first-seen order.
func uniqueStrings(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := ids[:0:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
