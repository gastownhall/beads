package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Repair for dependency edges stored as external although their target is held
// locally (gastownhall/beads#6066).
//
// Until this fix every dependency writer classified a target by comparing id
// prefixes, so an edge onto a local issue or wisp whose id carries another
// prefix (an explicit --id created with --force, or a row imported from another
// rig) landed in depends_on_external. Blocked computation ignores that column,
// so the dependent read as ready while its local blocker was open. New writes
// now classify by presence (issueops.ResolveDepTargetKindAcrossInTx); this
// repair moves the rows already written into the column presence would have
// chosen, once per clone, as the pre-migration repair of ignored marker 0032.
//
// Rules, matching the write-time resolver:
//   - an explicit "external:" ref is never moved;
//   - a target present in wisps (any status) moves to depends_on_wisp_id;
//   - otherwise a target present in issues moves to depends_on_issue_id;
//   - a target this database does not hold stays external.
//
// Only the target column changes: the row keeps its id (depid derives it from
// the source and the target string, not the column), type, metadata, thread,
// created_at and created_by. A moved edge that would conflict with the parent
// hierarchy is kept as it is; the repair heals storage, it does not police
// graphs a previous binary accepted.

// localExternalDepTables are the two edge tables the repair scans, in the order
// it plans them. Both names are hardcoded constants, never user input.
var localExternalDepTables = []string{"dependencies", "wisp_dependencies"}

// localExternalDepMove is one external-column edge whose target is held
// locally, with the typed column the move writes.
type localExternalDepMove struct {
	table    string
	issueID  string
	target   string
	depType  types.DependencyType
	toColumn string
}

// repairLocalExternalDepTargets is the {ignored_schema_migrations, 32} repair.
//
// It plans both tables before writing either. When nothing needs moving it
// writes nothing, which is every fresh database and every clone that never
// held a foreign-prefix target.
//
// The moves and the recompute run in one SQL transaction: a failure in either
// rolls both back, so a retry still finds every edge to move and recomputes
// from its source. The marker records only after the repair returns.
func repairLocalExternalDepTargets(ctx context.Context, db DBConn) error {
	// Ignored 0001 creates both tables and the sentinel replay restores them,
	// so a pass reaching 0032 without them is not a clone state; there is no
	// wisp plane to resolve against and nothing to recompute into.
	hasWisps, err := schemaTableExists(ctx, db, "wisps")
	if err != nil {
		return fmt.Errorf("checking wisps table: %w", err)
	}
	if !hasWisps {
		return nil
	}

	var moves []localExternalDepMove
	for _, table := range localExternalDepTables {
		exists, err := schemaTableExists(ctx, db, table)
		if err != nil {
			return fmt.Errorf("checking %s table: %w", table, err)
		}
		if !exists {
			continue
		}
		planned, err := planLocalExternalDepMoves(ctx, db, table)
		if err != nil {
			return err
		}
		moves = append(moves, planned...)
	}
	if len(moves) == 0 {
		return nil
	}

	return runInRepairTx(ctx, db, func(tx DBConn) error {
		return applyLocalExternalDepMoves(ctx, tx, moves)
	})
}

// planLocalExternalDepMoves reads table's external-column edges whose target is
// held locally. It is read-only. An edge whose (source, target) pair already
// sits in a typed column is left alone with a log line: moving it would violate
// that column's unique key, and the dependency re-key that runs earlier in the
// same pass merges such twins.
func planLocalExternalDepMoves(ctx context.Context, db DBConn, table string) ([]localExternalDepMove, error) {
	//nolint:gosec // G201: table is one of localExternalDepTables, never user input.
	query := fmt.Sprintf(`
SELECT d.issue_id, d.depends_on_external, d.type,
       EXISTS (SELECT 1 FROM wisps w WHERE w.id = d.depends_on_external),
       EXISTS (SELECT 1 FROM issues i WHERE i.id = d.depends_on_external),
       EXISTS (SELECT 1 FROM %[1]s x WHERE x.issue_id = d.issue_id
               AND (x.depends_on_issue_id = d.depends_on_external OR x.depends_on_wisp_id = d.depends_on_external))
FROM %[1]s d
WHERE d.depends_on_external IS NOT NULL
  AND d.depends_on_external NOT LIKE 'external:%%'
ORDER BY d.issue_id, d.depends_on_external`, table)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%s: plan local external dependency targets: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var moves []localExternalDepMove
	for rows.Next() {
		var (
			m                        localExternalDepMove
			depType                  string
			isWisp, isIssue, hasTwin bool
		)
		if err := rows.Scan(&m.issueID, &m.target, &depType, &isWisp, &isIssue, &hasTwin); err != nil {
			return nil, fmt.Errorf("%s: scan local external dependency target: %w", table, err)
		}
		switch {
		case isWisp:
			m.toColumn = "depends_on_wisp_id"
		case isIssue:
			m.toColumn = "depends_on_issue_id"
		default:
			continue
		}
		if hasTwin {
			log.Printf("schema: %s: edge %s -> %s is stored both as external and as a local target; keeping both rows",
				table, m.issueID, m.target)
			continue
		}
		m.table = table
		m.depType = types.DependencyType(depType)
		moves = append(moves, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: read local external dependency targets: %w", table, err)
	}
	return moves, nil
}

// applyLocalExternalDepMoves writes every move, then recomputes blocked state
// for the sources and everything their change can reach (descendants, siblings
// under a parent-child target's ancestors, waits-for spawners). The affected
// sets are read after the moves, so they see the edges as local.
func applyLocalExternalDepMoves(ctx context.Context, tx DBConn, moves []localExternalDepMove) error {
	for _, m := range moves {
		//nolint:gosec // G201: table and column come from fixed sets, never user input.
		stmt := fmt.Sprintf(`UPDATE %s SET %s = depends_on_external, depends_on_external = NULL
WHERE issue_id = ? AND depends_on_external = ?`, m.table, m.toColumn)
		if _, err := tx.ExecContext(ctx, stmt, m.issueID, m.target); err != nil {
			return fmt.Errorf("%s: move edge %s -> %s to %s: %w", m.table, m.issueID, m.target, m.toColumn, err)
		}
	}

	var issueIDs, wispIDs []string
	issueSeen, wispSeen := map[string]bool{}, map[string]bool{}
	for _, m := range moves {
		affect := issueops.AffectedByDepChangeInTx
		if m.table == "wisp_dependencies" {
			affect = issueops.AffectedByDepChangeForWispInTx
		}
		issues, wisps, err := affect(ctx, tx, m.issueID, m.target, m.depType)
		if err != nil {
			return fmt.Errorf("%s: collect blocked-state recompute set for %s: %w", m.table, m.issueID, err)
		}
		for _, id := range issues {
			if !issueSeen[id] {
				issueSeen[id] = true
				issueIDs = append(issueIDs, id)
			}
		}
		for _, id := range wisps {
			if !wispSeen[id] {
				wispSeen[id] = true
				wispIDs = append(wispIDs, id)
			}
		}
	}
	// A migration writes no events-journal records: the derived is_blocked
	// change is part of the schema pass, not a user's change.
	if _, err := issueops.RecomputeIsBlockedInTxWithResult(issueops.WithEventsJournal(ctx, false), tx,
		issueIDs, wispIDs, issueops.NoRequestActor); err != nil {
		return fmt.Errorf("recompute blocked state after moving local dependency targets: %w", err)
	}
	return nil
}

// repairTxBeginner is satisfied by *sql.DB and *sql.Conn. A plain
// BEGIN/COMMIT through ExecContext is not safe on a pooled handle (the two can
// land on different sessions), so the repair asks the handle for a real
// transaction instead.
type repairTxBeginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// runInRepairTx runs fn in one SQL transaction on db, committing on success and
// rolling back on error. A handle that cannot begin one (a *sql.Tx, already
// inside a transaction) runs fn directly, inside its caller's transaction.
func runInRepairTx(ctx context.Context, db DBConn, fn func(tx DBConn) error) error {
	beginner, ok := db.(repairTxBeginner)
	if !ok {
		return fn(db)
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin repair transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("roll back repair transaction: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit repair transaction: %w", err)
	}
	return nil
}
