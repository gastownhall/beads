package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// repairDepLocalityExternalRows is the pre-0032 repair
// (gastownhall/beads#6066): rows of the two edge tables whose target sits in
// depends_on_external even though the value names a local wisp or issue row.
// Before the presence-aware resolver landed, writer routes classified by
// prefix alone, so an edge between two local rows created with foreign-prefix
// explicit ids was stored external — invisible to blocked/ready computation.
//
// Ordering is the migration framework's: this runs on the pass that applies
// ignored migration 0032, immediately before its body, after main migrations
// (and the dependency-id re-key) have run, so the tables hold their current
// shape. The re-key has already converged edge ids to
// depid.New(issue_id, resolved target); because it keys on the target VALUE
// and not the typed column, a move from the external column to a typed column
// keeps every moved row's id depid-consistent — no id changes, which is
// exactly the preservation the repair promises.
//
// Both tables are planned before either is written, so a failure or refusal
// leaves no half-repaired table behind. A plan with no candidate rows is a
// no-op: the repair must not refuse (or write) on databases with nothing to
// fix, or the marker would wedge every future open of a dirty database.
//
// If the repair is needed and the issues or dependencies table already has
// uncommitted changes, it refuses BEFORE any write with *DirtyTablesError:
// the marker stays pending, and the documented commit-and-retry recovery
// stays reachable because the working-set-reconcile opens skip the migration
// when they see this error type via errors.As. Refusing is also what keeps
// MigrateUp's changedDirtyTableSignatures guard from turning the repair's
// writes into a plain error after the main series has committed, and what
// keeps the terminal staging from committing the repair's rows together with
// pre-existing user changes in the same table. Dolt-ignored tables (wisps,
// wisp_dependencies) are clone-local and exempt, exactly as in the re-key and
// the recompute; the dirty set read here excludes them.
func repairDepLocalityExternalRows(ctx context.Context, db DBConn) error {
	moves, err := planDepLocalityMoves(ctx, db)
	if err != nil {
		return err
	}
	if len(moves) == 0 {
		return nil
	}
	dirty, err := committableDirtyTables(ctx, db)
	if err != nil {
		return fmt.Errorf("dep locality repair: reading dirty tables: %w", err)
	}
	var refuse []string
	for _, table := range []string{"issues", "dependencies"} {
		if _, ok := dirty[table]; ok {
			refuse = append(refuse, table)
		}
	}
	if len(refuse) > 0 {
		sort.Strings(refuse)
		return &DirtyTablesError{Tables: refuse}
	}
	return applyDepLocalityMoves(ctx, db, moves)
}

// depLocalityMove is one planned reclassification: the edge row, its owning
// source id, the local target value, and the typed column it moves to.
type depLocalityMove struct {
	table   string // "dependencies" or "wisp_dependencies"
	id      string
	source  string
	target  string
	depType types.DependencyType
	toWisp  bool
}

// depLocalityExternalPrefix is the marker of a value that is external by
// intent rather than by misclassification; such rows are never moved.
const depLocalityExternalPrefix = "external:"

// planDepLocalityMoves reads both edge tables and returns the rows whose
// external-column value names a local row, wisp target first (the resolver's
// order). Rows whose destination column is already taken for the same source —
// the same logical edge stored twice, reachable only through drifted or
// hand-repaired tables now that the re-key merges value duplicates — are left
// exactly as they are: the repair preserves data, it never deletes.
func planDepLocalityMoves(ctx context.Context, db DBConn) ([]depLocalityMove, error) {
	var moves []depLocalityMove
	for _, table := range depRekeyTables {
		rows, err := db.QueryContext(ctx, fmt.Sprintf(`
			SELECT id, issue_id, depends_on_external, type FROM %[1]s
			WHERE depends_on_external IS NOT NULL
			  AND LEFT(depends_on_external, %d) <> ?
			  AND depends_on_issue_id IS NULL
			  AND depends_on_wisp_id IS NULL
			ORDER BY id`, table, len(depLocalityExternalPrefix)), depLocalityExternalPrefix)
		if err != nil {
			return nil, fmt.Errorf("dep locality repair: scanning %s: %w", table, err)
		}
		type candidate struct {
			id, source, target, depType string
		}
		var candidates []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.source, &c.target, &c.depType); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("dep locality repair: scanning %s row: %w", table, err)
			}
			candidates = append(candidates, c)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("dep locality repair: scanning %s: %w", table, err)
		}
		_ = rows.Close()
		for _, c := range candidates {
			isWisp, err := depLocalityTargetPresent(ctx, db, "wisps", c.target)
			if err != nil {
				return nil, err
			}
			toWisp := isWisp
			if !isWisp {
				present, err := depLocalityTargetPresent(ctx, db, "issues", c.target)
				if err != nil {
					return nil, err
				}
				if !present {
					// An absent target is external by the resolver's rule;
					// the row keeps its column and its unresolved-target
					// semantics.
					continue
				}
			}
			destCol := "depends_on_issue_id"
			if toWisp {
				destCol = "depends_on_wisp_id"
			}
			taken, err := depLocalityDestinationTaken(ctx, db, table, c.source, destCol, c.target)
			if err != nil {
				return nil, err
			}
			if taken {
				continue
			}
			moves = append(moves, depLocalityMove{
				table:   table,
				id:      c.id,
				source:  c.source,
				target:  c.target,
				depType: types.DependencyType(c.depType),
				toWisp:  toWisp,
			})
		}
	}
	return moves, nil
}

// depLocalityTargetPresent probes one local table for the target value by
// primary key. Absence (sql.ErrNoRows) is the answer, not an error: every
// other failure must abort the repair rather than misclassify the row.
func depLocalityTargetPresent(ctx context.Context, db DBConn, table, id string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT 1 FROM %s WHERE id = ? LIMIT 1", table), id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("dep locality repair: probing %s for %q: %w", table, id, err)
	}
	return true, nil
}

// depLocalityDestinationTaken reports whether the source already holds an
// edge in the destination column with the same target value; such a move
// would collide with the per-column unique key, so the row stays put.
func depLocalityDestinationTaken(ctx context.Context, db DBConn, table, source, destCol, target string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT 1 FROM %s WHERE issue_id = ? AND %s = ? LIMIT 1", table, destCol),
		source, target).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("dep locality repair: probing %s destination for %q: %w", table, target, err)
	}
	return true, nil
}

// applyDepLocalityMoves writes the moves and the blocked-state recomputation
// of every affected row in ONE ordinary SQL transaction on the migration's
// pinned connection, as the design requires: autocommitted moves followed by
// a failed recompute would strand the moved-source seed on retry, and the
// ordinary transaction's ROLLBACK undoes both or neither. The recompute goes
// through issueops' existing expansion (descendants, waits-for) and mark/
// unmark templates, which preserve updated_at and write no journal rows on a
// connection without an activated journal — so the repair emits no new
// events, journal entries or versions and no timestamps change. Its writes
// to committable tables are then staged and committed by MigrateUp's
// existing migration-commit lifecycle, like every other repair's.
func applyDepLocalityMoves(ctx context.Context, db DBConn, moves []depLocalityMove) error {
	if _, err := db.ExecContext(ctx, "START TRANSACTION"); err != nil {
		return fmt.Errorf("dep locality repair: begin: %w", err)
	}
	rollback := func() {
		_, _ = db.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	}
	issueIDs, wispIDs, err := depLocalityMoveAndCollect(ctx, db, moves)
	if err != nil {
		rollback()
		return err
	}
	if len(issueIDs) > 0 || len(wispIDs) > 0 {
		if _, err := issueops.RecomputeIsBlockedInTxWithResult(ctx, db, issueIDs, wispIDs, issueops.NoRequestActor); err != nil {
			rollback()
			return fmt.Errorf("dep locality repair: recompute blocked state: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, "COMMIT"); err != nil {
		rollback()
		return fmt.Errorf("dep locality repair: commit: %w", err)
	}
	return nil
}

// depLocalityMoveAndCollect applies the moves and returns the deduplicated,
// sorted ids whose blocked state the moves can affect, gathered through the
// same issueops expansion an ordinary edge write uses (durable source via
// AffectedByDepChangeInTx, wisp source via AffectedByDepChangeForWispInTx;
// non-seeding dep types contribute nothing, matching their write-path
// behavior).
func depLocalityMoveAndCollect(ctx context.Context, db DBConn, moves []depLocalityMove) ([]string, []string, error) {
	issueSeen := map[string]bool{}
	wispSeen := map[string]bool{}
	collect := func(issues, wisps []string) {
		for _, id := range issues {
			issueSeen[id] = true
		}
		for _, id := range wisps {
			wispSeen[id] = true
		}
	}
	for _, m := range moves {
		destCol := "depends_on_issue_id"
		if m.toWisp {
			destCol = "depends_on_wisp_id"
		}
		res, err := db.ExecContext(ctx,
			fmt.Sprintf("UPDATE %s SET %s = ?, depends_on_external = NULL WHERE id = ?", m.table, destCol),
			m.target, m.id)
		if err != nil {
			return nil, nil, fmt.Errorf("dep locality repair: moving %s in %s: %w", m.id, m.table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, nil, fmt.Errorf("dep locality repair: moving %s in %s: rows affected: %w", m.id, m.table, err)
		}
		if n != 1 {
			return nil, nil, fmt.Errorf("dep locality repair: moving %s in %s: %d rows, want 1", m.id, m.table, n)
		}
		if m.table == depRekeyTables[0] {
			issues, wisps, err := issueops.AffectedByDepChangeInTx(ctx, db, m.source, m.target, m.depType)
			if err != nil {
				return nil, nil, fmt.Errorf("dep locality repair: affected ids for %s: %w", m.id, err)
			}
			collect(issues, wisps)
		} else {
			issues, wisps, err := issueops.AffectedByDepChangeForWispInTx(ctx, db, m.source, m.target, m.depType)
			if err != nil {
				return nil, nil, fmt.Errorf("dep locality repair: affected ids for %s: %w", m.id, err)
			}
			collect(issues, wisps)
		}
	}
	issueIDs := make([]string, 0, len(issueSeen))
	for id := range issueSeen {
		issueIDs = append(issueIDs, id)
	}
	sort.Strings(issueIDs)
	wispIDs := make([]string, 0, len(wispSeen))
	for id := range wispSeen {
		wispIDs = append(wispIDs, id)
	}
	sort.Strings(wispIDs)
	return issueIDs, wispIDs, nil
}
