package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	publicops "github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// depExternalRetypeMarkerVersion is ignored migration 0032, the clone-local
// marker whose pre-migration repair re-types the legacy external rows the
// prefix-era dependency classifier wrote for same-database cross-prefix
// targets (gastownhall/beads#8299). The pin is a hardcoded literal for the
// same reason depRekeyMarkerVersion is: the marker's pending state is what
// forces the one repair pass, and the repair is registered against exactly
// this version in preMigrationRepairs.
const depExternalRetypeMarkerVersion = 32

// repairRunsInTransaction reports whether src's repair for version must run
// inside a driver transaction (see preMigrationRepairInDriverTx). It checks
// the source's identity — its cursor table — as well as the version, so a
// main-source migration that someday claims the same number does not silently
// pick up the boundary.
func (m migrationSource) repairRunsInTransaction(version int) bool {
	return m.cursorTable == ignoredSource.cursorTable && version == depExternalRetypeMarkerVersion
}

// pinnedSessionConn is the surface the repair-transaction boundary needs from
// MigrateUp's caller: a database/sql transaction begun on THE session the pass
// pinned. *sql.Conn satisfies it. A pooled *sql.DB implements BeginTx too but
// does not satisfy Raw, and that is deliberate: Begin/COMMIT through a pooled
// handle can land its statements on several sessions, silently dropping the
// atomicity the boundary exists to provide — so it is refused below, exactly
// the reason depPlan.execute documents for staying out of transactions. A
// *sql.Tx also lacks Raw, so a caller that hands the pass a transaction (test
// adapters composing steps) cannot get a nested one.
type pinnedSessionConn interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	Raw(f func(driverConn any) error) error
}

// preMigrationRepairInDriverTx runs src's repair for mf inside one driver
// transaction, the boundary ignored 0032's repair requires: plan, convert,
// hierarchy validation and blocked-state recompute all commit together, and
// an error or a killed process anywhere before the Commit rolls all of them
// back together, leaving the marker pending so the next open retries.
//
// The transaction is begun with BeginTx on the pinned session MigrateUp
// documents as its contract — the same handle suspendTransactionCommit has
// already pinned dolt_transaction_commit=off on for the rest of the pass — so
// the Commit below ends the SQL transaction without minting a Dolt commit:
// the pass's existing commit ordering (the ignored lane rides the final
// staged commit) keeps owning history. A caller that cannot supply the
// pinned session fails BEFORE the repair writes anything rather than running
// it without its boundary.
func (m migrationSource) preMigrationRepairInDriverTx(ctx context.Context, db DBConn, mf migrationFile) error {
	repair, ok := preMigrationRepairs[repairKey{m.cursorTable, mf.version}]
	if !ok {
		// repairRunsInTransaction and the registry disagree; the caller gates
		// on both, so this is unreachable, but a missing repair is not a
		// reason to skip the migration itself.
		return nil
	}
	conn, ok := db.(pinnedSessionConn)
	if !ok {
		return fmt.Errorf("pre-repair for migration %s needs the pinned single session MigrateUp documents (a *sql.Conn), got %T; refusing to run the repair without its transaction boundary", mf.name, db)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning the repair transaction for migration %s: %w", mf.name, err)
	}
	repairErr := repair(ctx, tx)
	if repairErr == nil {
		if err := tx.Commit(); err != nil {
			repairErr = fmt.Errorf("committing the repair transaction for migration %s: %w", mf.name, err)
		}
	}
	if repairErr != nil {
		// The deferred-rollback pattern from embeddeddolt's commit paths:
		// the repair's own error is primary, a rollback failure is joined
		// onto it, never replaces it, and ErrTxDone after a failed Commit is
		// the transaction already being finished.
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(repairErr, fmt.Errorf("rolling back the repair transaction for migration %s: %w", mf.name, rbErr))
		}
		return repairErr
	}
	return nil
}

// depRetypeFaultHook is a test-only seam. When non-nil it runs at each stage
// boundary of the re-type repair — "planned" after both edge tables are
// planned and before any write, "converted" after every planned row is
// written and before the recompute, "recomputed" after the recompute and
// before the repair returns to its transaction boundary. Returning an error
// aborts the repair (and, through the boundary, rolls the whole repair
// transaction back), emulating a fault mid-repair so tests can prove the
// rollback leaves no debris and the next open retries cleanly. Production
// leaves it nil.
var depRetypeFaultHook func(ctx context.Context, db DBConn, stage string) error

// SetDepRetypeFaultHookForTest installs fn as the re-type repair's fault hook
// and returns a function that restores the previous value. Test-only.
func SetDepRetypeFaultHookForTest(fn func(ctx context.Context, db DBConn, stage string) error) func() {
	prev := depRetypeFaultHook
	depRetypeFaultHook = fn
	return func() { depRetypeFaultHook = prev }
}

func fireDepRetypeFault(ctx context.Context, db DBConn, stage string) error {
	if depRetypeFaultHook == nil {
		return nil
	}
	return depRetypeFaultHook(ctx, db, stage)
}

// depRetypeRow is one planned conversion: an external row whose target THIS
// database holds, moving into its typed target column.
type depRetypeRow struct {
	table      string // "dependencies" or "wisp_dependencies"
	id         string // the row's primary key, preserved by the conversion
	issueID    string // the edge's source (an issue id or a wisp id)
	target     string // the resolved target id, unchanged by the conversion
	depType    string
	kind       issueops.DepTargetKind
	sourceWisp bool // the row lives in wisp_dependencies (its source is a wisp)
}

// repairCrossPrefixExternalDeps is ignored 0032's pre-migration repair: it
// re-types the dependency rows the prefix-era classifier stored as external
// even though THIS database holds their target, so they block readiness
// again (GH#8299). It runs inside one driver transaction begun on
// MigrateUp's pinned session (runMigrations' special case), and everything
// below is idempotent-or-atomic by that boundary: the whole repair either
// lands or rolls back, and the marker is recorded only after it lands.
//
// The shape mirrors the write path's presence-based classifier
// (issueops.ClassifyDepTargetInTx): an explicit "external:" reference is
// external by definition; a target present in wisps or issues moves into
// its typed column; a target NEITHER table holds keeps the legacy external
// column (the multi-rig "add now, route later" shape). Blocking rows run the
// same hierarchy validation the write path runs before conversion, and a
// conflict keeps the row external with a removal notice rather than
// manufacturing an edge bd dep add would have refused.
//
// db is the repair transaction itself (a *sql.Tx), whose statement surface
// is identical to DBConn, so the shared issueops helpers — which is why this
// file imports issueops at all — run against the same atomic boundary.
func repairCrossPrefixExternalDeps(ctx context.Context, db DBConn) error {
	rows, err := planDepExternalRetype(ctx, db)
	if err != nil {
		return err
	}
	if err := fireDepRetypeFault(ctx, db, "planned"); err != nil {
		return err
	}
	if err := applyDepExternalRetype(ctx, db, rows); err != nil {
		return err
	}
	if err := fireDepRetypeFault(ctx, db, "converted"); err != nil {
		return err
	}
	if err := recomputeDepExternalRetype(ctx, db, rows); err != nil {
		return err
	}
	return fireDepRetypeFault(ctx, db, "recomputed")
}

// planDepExternalRetype reads both edge tables and decides, without writing,
// which external rows convert and which stay. Planning both tables before
// any write is the same contract rekeyDependencyIDs carries: a refusal or
// failure leaves the tables exactly as they were.
//
// Only well-formed rows participate — exactly one non-null target column,
// the external one. A row with no target or several (ck_dep_one_target
// drift; see depRow.mergeable for the twin gate) is left alone for
// bd doctor to surface.
func planDepExternalRetype(ctx context.Context, db DBConn) ([]depRetypeRow, error) {
	var rows []depRetypeRow
	for _, table := range depRekeyTables {
		exists, err := schemaTableExists(ctx, db, table)
		if err != nil {
			return nil, fmt.Errorf("%s: checking table for the 0032 retype: %w", table, err)
		}
		if !exists {
			// dependencies always exists by this point in the ignored lane;
			// wisp_dependencies can legitimately be absent on a schema whose
			// ignored lane is behind, and an absent table can hold no
			// external row.
			continue
		}
		//nolint:gosec // G201: table is a hardcoded constant from depRekeyTables
		scanned, err := db.QueryContext(ctx, fmt.Sprintf(`
			SELECT id, issue_id, depends_on_external, type FROM %s
			WHERE depends_on_external IS NOT NULL
			  AND depends_on_issue_id IS NULL
			  AND depends_on_wisp_id IS NULL
			ORDER BY id`, table))
		if err != nil {
			return nil, fmt.Errorf("%s: reading external rows for the 0032 retype: %w", table, err)
		}
		for scanned.Next() {
			var id, issueID string
			var target, depType sql.NullString
			if err := scanned.Scan(&id, &issueID, &target, &depType); err != nil {
				_ = scanned.Close()
				return nil, fmt.Errorf("%s: scanning external row for the 0032 retype: %w", table, err)
			}
			// An explicit external reference names something outside beads
			// entirely; it is external by definition, not misfiled.
			if strings.HasPrefix(target.String, "external:") {
				continue
			}
			kind, present, err := resolveDepRetypeTarget(ctx, db, target.String)
			if err != nil {
				_ = scanned.Close()
				return nil, err
			}
			if !present {
				// The multi-rig "add now, route later" shape: the target
				// lives in another rig's database, or has not been hydrated
				// into this one yet. Keep the legacy column.
				continue
			}
			dep := &types.Dependency{IssueID: issueID, DependsOnID: target.String, Type: types.DependencyType(depType.String)}
			if types.IsGateEdge(dep.Type) {
				// depTables nil: CheckBlockingHierarchyInTx falls back to its
				// own both-tables default, the same set the write path walks.
				if err := issueops.CheckBlockingHierarchyInTx(ctx, db, dep, nil); err != nil {
					var conflict *publicops.DependencyHierarchyConflictError
					if !errors.As(err, &conflict) {
						_ = scanned.Close()
						return nil, fmt.Errorf("%s: hierarchy check before the 0032 retype of %s -> %s: %w", table, issueID, target.String, err)
					}
					// The write path would refuse this edge; the repair must
					// not manufacture it. Keep the row external so it still
					// blocks nothing, and tell the operator exactly which
					// edge to remove.
					fmt.Fprintf(stderr,
						"WARN: migration 0032: keeping cross-prefix edge %s -> %s external: converting it would violate the blocking hierarchy; remove it with 'bd dep remove %s %s'\n",
						issueID, target.String, issueID, target.String)
					continue
				}
			}
			rows = append(rows, depRetypeRow{
				table: table, id: id, issueID: issueID,
				target: target.String, depType: depType.String,
				kind: kind, sourceWisp: table != "dependencies",
			})
		}
		if err := scanned.Err(); err != nil {
			_ = scanned.Close()
			return nil, fmt.Errorf("%s: iterating external rows for the 0032 retype: %w", table, err)
		}
		_ = scanned.Close()
	}
	// Plan order is a pure function of table content (table, then id), so two
	// clones holding the same rows plan the same repair.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].table != rows[j].table {
			return rows[i].table < rows[j].table
		}
		return rows[i].id < rows[j].id
	})
	return rows, nil
}

// resolveDepRetypeTarget probes whether THIS database holds target, and in
// which table. Wisp first, mirroring ClassifyDepTargetInTx's order; only a
// probe error (never absence) aborts the repair. A missing wisps table on a
// schema that has not created it yet cannot hold the target, the same
// optional-wisp-table tolerance the read paths apply.
func resolveDepRetypeTarget(ctx context.Context, db DBConn, target string) (issueops.DepTargetKind, bool, error) {
	probes := []struct {
		table string
		kind  issueops.DepTargetKind
	}{
		{"wisps", issueops.DepTargetWisp},
		{"issues", issueops.DepTargetIssue},
	}
	for _, probe := range probes {
		var held int
		//nolint:gosec // G201: table is a hardcoded constant ("wisps" or "issues")
		err := db.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", probe.table), target).Scan(&held)
		if err != nil {
			if probe.table == "wisps" && dberrors.IsTableNotExist(err) {
				continue
			}
			return 0, false, fmt.Errorf("probing %s for the 0032 retype of target %s: %w", probe.table, target, err)
		}
		if held > 0 {
			return probe.kind, true, nil
		}
	}
	return 0, false, nil
}

// applyDepExternalRetype moves each planned row's target into its typed
// column and clears the external column. The id, type, metadata, thread and
// provenance columns are deliberately untouched: the id is already derived
// from (issue_id, target) by the dependency re-key that runs earlier in this
// same pass, and the conversion moves the target between columns without
// changing its value, so the derived id stays valid.
func applyDepExternalRetype(ctx context.Context, db DBConn, rows []depRetypeRow) error {
	for _, row := range rows {
		//nolint:gosec // G201: table is a hardcoded constant and the target column comes from DepTargetKind.Column()
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			"UPDATE %s SET %s = ?, depends_on_external = NULL WHERE id = ?",
			row.table, row.kind.Column()), row.target, row.id); err != nil {
			return fmt.Errorf("%s: converting external row %s to its typed target column: %w", row.table, row.id, err)
		}
	}
	return nil
}

// recomputeDepExternalRetype seeds the ids the conversion affected and runs
// the shared is_blocked recompute, exactly the way a dep write does: direct
// sources alone miss descendants (the recompute fixpoint does not expand its
// input), so each converted row seeds through issueops.AffectedByDepChange*
// — which already walks parent-child descendants and the waits-for fanout —
// and the union of every walk is what the fixpoint receives. The recompute
// preserves updated_at on both issue tables.
func recomputeDepExternalRetype(ctx context.Context, db DBConn, rows []depRetypeRow) error {
	if len(rows) == 0 {
		return nil
	}
	issueSeen := make(map[string]bool)
	wispSeen := make(map[string]bool)
	var issueIDs, wispIDs []string
	for _, row := range rows {
		var affectedIssues, affectedWisps []string
		var err error
		if row.sourceWisp {
			affectedIssues, affectedWisps, err = issueops.AffectedByDepChangeForWispInTx(ctx, db, row.issueID, row.target, types.DependencyType(row.depType))
		} else {
			affectedIssues, affectedWisps, err = issueops.AffectedByDepChangeInTx(ctx, db, row.issueID, row.target, types.DependencyType(row.depType))
		}
		if err != nil {
			return fmt.Errorf("%s: seeding affected ids for the 0032 retype of %s -> %s: %w", row.table, row.issueID, row.target, err)
		}
		for _, id := range affectedIssues {
			if !issueSeen[id] {
				issueSeen[id] = true
				issueIDs = append(issueIDs, id)
			}
		}
		for _, id := range affectedWisps {
			if !wispSeen[id] {
				wispSeen[id] = true
				wispIDs = append(wispIDs, id)
			}
		}
	}
	if len(issueIDs) == 0 && len(wispIDs) == 0 {
		return nil
	}
	// Deterministic order, the same convergence argument the plan sort makes.
	sort.Strings(issueIDs)
	sort.Strings(wispIDs)
	if _, err := issueops.RecomputeIsBlockedInTxWithResult(ctx, db, issueIDs, wispIDs); err != nil {
		return fmt.Errorf("recomputing is_blocked after the 0032 retype: %w", err)
	}
	return nil
}
