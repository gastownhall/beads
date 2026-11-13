package schema

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// GH#5269: a store can carry migration marks for versions whose DDL never
// applied (a guarded PREPARE/EXECUTE that reported success and did nothing).
// The applying-migration path corrects such drift before recording a mark
// (enforceDroppedDefaults); this file is the explicit repair for the stores
// that already carry the false marks: it re-derives the dropped-default
// obligations from the RECORDED cursors — the marks the store claims — and
// corrects the drift through the same direct, identifier-quoted DDL.

// postConditionObligation is one pinned dropped-default post-condition: the
// migration series (by cursor table) and version that promised to drop a
// column's default, and the column itself.
type postConditionObligation struct {
	cursorTable string
	version     int
	table       string
	column      string
}

// droppedDefaultObligations pins, in Go, the dropped-default post-conditions
// of the frozen migrations (the same conditions droppedDefaults extracts from
// each body at apply time). Pinned rather than parsed at diagnosis time
// because the repair must reach databases whose recorded versions the
// per-body view cannot act on: the mark exists, so the body will never run
// again. TestDroppedDefaultObligationsArePinnedAndRetiredExplicitly fails the
// build when a new frozen migration drops a default without being pinned here,
// or when a migration after a pinned version re-introduces the default —
// retiring an obligation is an explicit, reviewed edit of this table.
var droppedDefaultObligations = []postConditionObligation{
	{cursorTable: mainSource.cursorTable, version: 43, table: "dependencies", column: "id"},
	{cursorTable: mainSource.cursorTable, version: 50, table: "dependencies", column: "id"},
	{cursorTable: mainSource.cursorTable, version: 51, table: "events", column: "id"},
	{cursorTable: mainSource.cursorTable, version: 51, table: "comments", column: "id"},
	{cursorTable: mainSource.cursorTable, version: 51, table: "issue_snapshots", column: "id"},
	{cursorTable: mainSource.cursorTable, version: 51, table: "compaction_snapshots", column: "id"},
	{cursorTable: ignoredSource.cursorTable, version: 10, table: "wisp_events", column: "id"},
	{cursorTable: ignoredSource.cursorTable, version: 10, table: "wisp_comments", column: "id"},
	{cursorTable: ignoredSource.cursorTable, version: 10, table: "wisp_dependencies", column: "id"},
}

// setDefaultRe finds the column defaults a migration body (re-)introduces, the
// retirement side of the pinning test above.
var setDefaultRe = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+` + "`?" + `(\w+)` + "`?" +
	`\s+ALTER\s+COLUMN\s+` + "`?" + `(\w+)` + "`?" + `\s+SET\s+DEFAULT`)

// recordedCursorVersion reads the highest version a cursor table RECORDS. It
// never creates the table and applies none of the cursor-reality disbelief:
// the defect class this repairs is marks recorded ahead of the DDL, so
// diagnosis must see the recorded claim itself, not the effective version a
// migration pass would compute. exists is false when the table is absent
// (nothing recorded; no obligations covered).
func recordedCursorVersion(ctx context.Context, db DBConn, cursorTable string) (version int, exists bool, err error) {
	var maxed sql.NullInt64
	//nolint:gosec // G201: cursorTable is one of the two frozen constants.
	if err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM "+cursorTable).Scan(&maxed); err != nil {
		if dberrors.IsTableNotExist(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("reading %s: %w", cursorTable, err)
	}
	if !maxed.Valid {
		return 0, true, nil
	}
	return int(maxed.Int64), true, nil
}

// recordedDroppedDefaultObligations returns the pinned obligations whose
// versions both cursors already record, deduplicated by table/column (the
// first, lowest-version entry retains the provenance). Reading each cursor
// independently means a store that is current on one series and behind on the
// other only owes what the recorded marks claim.
func recordedDroppedDefaultObligations(ctx context.Context, db DBConn) []postConditionObligation {
	sources := []migrationSource{mainSource, ignoredSource}
	var out []postConditionObligation
	seen := make(map[string]bool)
	for _, src := range sources {
		recorded, exists, err := recordedCursorVersion(ctx, db, src.cursorTable)
		if err != nil || !exists {
			// A cursor that cannot be read is the caller's error to surface
			// (RepairDroppedDefaultDrift re-reads and fails loudly); here an
			// absent table simply records nothing.
			continue
		}
		for _, o := range droppedDefaultObligations {
			if o.cursorTable != src.cursorTable || o.version > recorded {
				continue
			}
			key := o.table + "." + o.column
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].table != out[j].table {
			return out[i].table < out[j].table
		}
		return out[i].column < out[j].column
	})
	return out
}

// PostConditionDirtyWorkError is returned by the explicit repair when a
// tracked table it must alter already holds uncommitted changes. The repair
// refuses before altering anything: committing its scoped DDL together with
// unrelated uncommitted rows would make the two impossible to separate later
// (the same entanglement MigrateUp's dirty-table guard refuses, #4566). The
// remedy is to commit (or reset) the working set first, then re-run
// `bd migrate schema`.
type PostConditionDirtyWorkError struct {
	Tables []string
}

func (e *PostConditionDirtyWorkError) Error() string {
	return "dropped-default repair would alter pre-existing dirty tables: " + strings.Join(e.Tables, ", ") +
		"; commit the working set first (bd dolt commit), then re-run `bd migrate schema` (GH#5269)"
}

// RepairDroppedDefaultDrift verifies the pinned dropped-default obligations of
// every recorded migration version and corrects, through direct
// identifier-quoted DDL, the defaults that survived the migrations that
// promised to drop them (GH#5269). It is the explicit `bd migrate schema`
// repair — ordinary opens never run it — and runs after a successful
// migration pass so both series have their final shape.
//
// A healthy store returns no repairs and writes nothing, so a repeat is a
// no-op with no commit. Rows, ids and cursor records (versions and content
// hashes) are never rewritten: the only DDL is the DEFAULT drop each recorded
// migration already promised. Successfully repaired committable tables are
// staged by name into one commit under a distinct schema-repair message;
// pre-existing staged work is unstaged first, as the migration harness does,
// so it neither rides the repair commit nor is lost. Tables under a
// dolt_ignore pattern (the wisp tables; events since main 0062) are repaired
// in place without staging: their shape is clone-local, never shared history.
//
// A failed correction leaves the pass's own error reporting to the caller:
// repairs already verified and committed are returned alongside the error
// (partial success), and the unresolved conditions are named in it.
func RepairDroppedDefaultDrift(ctx context.Context, db DBConn) (repairs []string, err error) {
	obligations := recordedDroppedDefaultObligations(ctx, db)
	if len(obligations) == 0 {
		return nil, nil
	}

	// Probe every obligation before touching anything: the full drift picture
	// decides the guard and the commit, and one unreadable column must not
	// hide the rest.
	type drift struct {
		obligation postConditionObligation
		observed   sql.NullString
	}
	var drifts []drift
	for _, o := range obligations {
		present, err := showTableExists(ctx, db, o.table)
		if err != nil {
			return nil, fmt.Errorf("checking table %s: %w", o.table, err)
		}
		if !present {
			continue // never created on this store; the body's guard skipped it too
		}
		def, err := showColumnDefault(ctx, db, o.table, o.column)
		if err != nil {
			return nil, fmt.Errorf("reading %s.%s default: %w", o.table, o.column, err)
		}
		if def.Valid {
			drifts = append(drifts, drift{obligation: o, observed: def})
		}
	}
	if len(drifts) == 0 {
		return nil, nil
	}

	// Same session hygiene as a migration pass: automatic transaction commits
	// off for the whole repair, restored on every return.
	restoreTransactionCommit, err := suspendTransactionCommit(ctx, db)
	if err != nil {
		return nil, err
	}
	defer func() { err = joinRestoreError(err, restoreTransactionCommit()) }()

	// Snapshot the working set and unstage unrelated pre-existing staged work,
	// as the migration harness does: the repair commit must contain exactly
	// the repaired tables, and pre-existing staged data must survive in the
	// working set rather than ride along.
	dirtyBefore, err := dirtyTables(ctx, db, false)
	if err != nil {
		return nil, fmt.Errorf("reading pre-repair status: %w", err)
	}
	if err := unstagePreExistingTables(ctx, db, dirtyBefore); err != nil {
		return nil, fmt.Errorf("unstaging pre-repair tables: %w", err)
	}

	// Current tracking state decides commit eligibility: a table matched by a
	// dolt_ignore pattern is repaired in place, never staged or committed.
	committable, err := existingCommittableTables(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("reading committable tables: %w", err)
	}

	var repaired []postConditionObligation
	var unresolved []string
	for _, d := range drifts {
		stmt := dropDefaultSQL(d.obligation.table, d.obligation.column)
		//nolint:gosec // G201: table and column come from the pinned obligation table.
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			unresolved = append(unresolved, fmt.Sprintf("%s.%s (v%04d, %s): %s failed: %v",
				d.obligation.table, d.obligation.column, d.obligation.version, d.obligation.cursorTable, stmt, err))
			continue
		}
		def, err := showColumnDefault(ctx, db, d.obligation.table, d.obligation.column)
		if err != nil {
			unresolved = append(unresolved, fmt.Sprintf("%s.%s (v%04d, %s): re-reading after correction failed: %v",
				d.obligation.table, d.obligation.column, d.obligation.version, d.obligation.cursorTable, err))
			continue
		}
		if def.Valid {
			unresolved = append(unresolved, fmt.Sprintf("%s.%s (v%04d, %s): %s reported success but DEFAULT %s survived",
				d.obligation.table, d.obligation.column, d.obligation.version, d.obligation.cursorTable, stmt, def.String))
			continue
		}
		repaired = append(repaired, d.obligation)
	}

	// Commit exactly the repaired committable tables, by name, under the
	// distinct repair message. Repairs on ignored-plane tables are complete
	// here: no staging, no commit, no shared history.
	var toStage []string
	for _, o := range repaired {
		if _, ok := committable[o.table]; ok {
			toStage = append(toStage, o.table)
		}
	}
	sort.Strings(toStage)
	if len(toStage) > 0 {
		for _, table := range toStage {
			if err := DrainCall(ctx, db, "CALL DOLT_ADD('-f', ?)", table); err != nil {
				return repairNames(repaired), fmt.Errorf("staging repaired table %s: %w", table, err)
			}
		}
		if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', ?)", "schema: repair dropped-default post-conditions"); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
				return repairNames(repaired), fmt.Errorf("committing dropped-default repairs: %w", err)
			}
		}
	}

	if len(unresolved) > 0 {
		return repairNames(repaired), fmt.Errorf(
			"%d dropped-default condition(s) repaired, %d unresolved (GH#5269): %s — "+
				"inspect the working set (dolt_status) and re-run `bd migrate schema` after resolving them",
			len(repaired), len(unresolved), strings.Join(unresolved, "; "))
	}
	return repairNames(repaired), nil
}

func repairNames(repaired []postConditionObligation) []string {
	if len(repaired) == 0 {
		return nil
	}
	out := make([]string, 0, len(repaired))
	for _, o := range repaired {
		out = append(out, o.table+"."+o.column)
	}
	sort.Strings(out)
	return out
}
