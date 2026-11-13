package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// This file is the recorded half of GH#5269. migration_post_checks.go stops a
// NEW cursor row from claiming a dropped default that is still in place; the
// functions here find and repair stores that already carry such a claim (a
// mark written by an older binary, or drift that arrived after the mark).
//
// Two entry points, deliberately narrow:
//
//   - CheckRecordedPostConditions is read-only, for `bd doctor`.
//   - RepairRecordedPostConditions mutates, and runs only from an explicit
//     `bd migrate schema` (MigrateUpAndRepairPostConditions, or
//     MigrateUpWithLock with WithPostConditionRepair). Ordinary opens and
//     MigrateUp never call it, so a healthy open pays nothing for it.
//
// The invariant is only "a column default a recorded migration drops is
// absent". It is not a certification of the whole schema.

// droppedDefaultRetirement names the migration that deliberately puts back a
// default an earlier migration drops. Once that migration is recorded, the
// earlier obligation no longer holds.
type droppedDefaultRetirement struct {
	source  string // "main" or "ignored"
	version int
}

// retiredDroppedDefaults lists every dropped-default obligation a later
// migration retires. It is empty today: no shipped migration re-adds a
// default to a column an earlier one drops. A migration that did so without
// an entry here would make every healthy store read as drifted;
// TestEmbeddedHealthyCurrentStoreHasNoPostConditionWork_5269 checks a freshly
// migrated store reports no violation.
var retiredDroppedDefaults = map[droppedDefault]droppedDefaultRetirement{}

// recordedObligation is one column whose default some recorded migration
// drops, with every recorded migration that drops it.
type recordedObligation struct {
	d       droppedDefault
	sources []string // "main 0050_dependencies_deterministic_id.up.sql", …
}

func (m migrationSource) label() string {
	if m.cursorTable == ignoredSource.cursorTable {
		return "ignored"
	}
	return "main"
}

// recordedCursor reads a source's recorded version without creating anything:
// an absent cursor table reads as 0. It reads the raw cursor, not
// currentVersion's reality-checked one: the question is what the database
// claims, and a store whose claim is disbelieved re-runs those migrations
// anyway.
func (m migrationSource) recordedCursor(ctx context.Context, db DBConn) (int, error) {
	present, err := tableInCurrentDatabase(ctx, db, m.cursorTable)
	if err != nil {
		return 0, fmt.Errorf("checking %s: %w", m.cursorTable, err)
	}
	if !present {
		return 0, nil
	}
	var v sql.NullInt64
	//nolint:gosec // G202: cursorTable is a package constant.
	if err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM "+m.cursorTable).Scan(&v); err != nil {
		return 0, fmt.Errorf("reading %s: %w", m.cursorTable, err)
	}
	return int(v.Int64), nil
}

// recordedDroppedDefaultObligations lists, deduplicated by table and column,
// every default a migration at or below each source's recorded cursor drops,
// minus those a recorded later migration retires.
func recordedDroppedDefaultObligations(ctx context.Context, db DBConn) ([]recordedObligation, error) {
	cursors := map[string]int{}
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		v, err := src.recordedCursor(ctx, db)
		if err != nil {
			return nil, err
		}
		cursors[src.label()] = v
	}
	return obligationsAtCursors(cursors, retiredDroppedDefaults)
}

func obligationsAtCursors(cursors map[string]int, retired map[droppedDefault]droppedDefaultRetirement) ([]recordedObligation, error) {
	byColumn := map[droppedDefault]*recordedObligation{}
	var order []droppedDefault
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		cursor := cursors[src.label()]
		for _, mf := range src.list() {
			if mf.version > cursor {
				continue
			}
			data, err := src.files.ReadFile(src.dir + "/" + mf.name)
			if err != nil {
				return nil, fmt.Errorf("reading migration %s: %w", mf.name, err)
			}
			for _, d := range droppedDefaults(string(data)) {
				if r, ok := retired[d]; ok && cursors[r.source] >= r.version {
					continue
				}
				o := byColumn[d]
				if o == nil {
					o = &recordedObligation{d: d}
					byColumn[d] = o
					order = append(order, d)
				}
				o.sources = append(o.sources, src.label()+" "+mf.name)
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i].String() < order[j].String() })
	out := make([]recordedObligation, len(order))
	for i, d := range order {
		out[i] = *byColumn[d]
	}
	return out, nil
}

// PostConditionViolation is a column a recorded migration promises has no
// default but which still has one.
type PostConditionViolation struct {
	Table   string
	Column  string
	Default string   // the default observed
	Sources []string // the recorded migrations that drop it
}

// Name is "table.column".
func (v PostConditionViolation) Name() string { return v.Table + "." + v.Column }

func (v PostConditionViolation) String() string {
	return fmt.Sprintf("%s has DEFAULT %s, which %s drops", v.Name(), v.Default, strings.Join(v.Sources, ", "))
}

// PostConditionCheck is what CheckRecordedPostConditions saw.
type PostConditionCheck struct {
	// Applicable counts recorded conditions whose table and column exist.
	Applicable int
	Violations []PostConditionViolation
}

// CheckRecordedPostConditions reports every recorded dropped-default
// condition that does not hold. It only reads: it creates no cursor table and
// changes nothing. Probe failures do not stop the walk; they come back joined
// in the error alongside whatever was found.
func CheckRecordedPostConditions(ctx context.Context, db DBConn) (PostConditionCheck, error) {
	var out PostConditionCheck
	obligations, err := recordedDroppedDefaultObligations(ctx, db)
	if err != nil {
		return out, err
	}
	var errs []error
	for _, o := range obligations {
		state, err := probeDroppedDefault(ctx, db, o.d)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", o.d, err))
			continue
		}
		if !state.present {
			continue
		}
		out.Applicable++
		if state.violated() {
			out.Violations = append(out.Violations, PostConditionViolation{
				Table: o.d.table, Column: o.d.column, Default: state.def.String, Sources: o.sources,
			})
		}
	}
	return out, errors.Join(errs...)
}

// PostConditionRepairReport is what RepairRecordedPostConditions changed.
type PostConditionRepairReport struct {
	// Repaired lists every default dropped and verified gone, by name.
	Repaired []PostConditionViolation
	// Committed is set when the repair created a commit (tracked tables only).
	Committed bool
}

// Names lists the repaired columns as sorted "table.column" strings.
func (r PostConditionRepairReport) Names() []string {
	names := make([]string, len(r.Repaired))
	for i, v := range r.Repaired {
		names[i] = v.Name()
	}
	sort.Strings(names)
	return names
}

func (r *PostConditionRepairReport) merge(other PostConditionRepairReport) {
	have := map[string]bool{}
	for _, v := range r.Repaired {
		have[v.Name()] = true
	}
	for _, v := range other.Repaired {
		if !have[v.Name()] {
			r.Repaired = append(r.Repaired, v)
		}
	}
	sort.Slice(r.Repaired, func(i, j int) bool { return r.Repaired[i].Name() < r.Repaired[j].Name() })
	r.Committed = r.Committed || other.Committed
}

// PostConditionRepairDirtyError refuses a repair whose tracked target tables
// hold uncommitted changes. It is returned before any target is altered.
type PostConditionRepairDirtyError struct {
	Tables []string
}

func (e *PostConditionRepairDirtyError) Error() string {
	return "cannot repair recorded migration post-conditions: uncommitted changes in " + strings.Join(e.Tables, ", ") +
		"; commit them first (`bd dolt commit`), then re-run `bd migrate schema` (GH#5269)"
}

// postConditionRepairCommitMessage labels the repair's own commit.
const postConditionRepairCommitMessage = "schema: repair recorded migration post-conditions (GH#5269)"

// RepairRecordedPostConditions drops every column default a recorded
// migration promises is gone, verifies each, and commits the tracked ones.
// Run it only from an explicit `bd migrate schema`, after MigrateUp, on the
// same pinned session and under the same migration lock.
//
//   - Nothing to repair: no writes at all, so a repeat creates no commit.
//   - A tracked target table with uncommitted changes refuses the whole repair
//     before any ALTER (*PostConditionRepairDirtyError).
//   - Unrelated staged work is unstaged first (its data stays in the working
//     set), exactly as the migration pass does, so the repair commit carries
//     only the repaired tables, staged by name.
//   - A dolt-ignored target (events since main 0062, the wisp tables) is
//     altered in place and never staged or committed: commit eligibility is
//     the table's current dolt_ignore state, not the source of the migration.
//   - Rows, ids and cursor rows are not touched.
//
// A partial failure returns the report of what was repaired together with an
// error naming what was not; a failed ALTER may leave working-set changes,
// which the existing dirty-table guidance covers.
func RepairRecordedPostConditions(ctx context.Context, db DBConn) (report PostConditionRepairReport, err error) {
	check, checkErr := CheckRecordedPostConditions(ctx, db)
	if len(check.Violations) == 0 {
		return report, checkErr
	}

	committable, err := existingCommittableTables(ctx, db)
	if err != nil {
		return report, fmt.Errorf("reading tracked tables: %w", err)
	}
	dirtyBefore, err := committableDirtyTables(ctx, db)
	if err != nil {
		return report, fmt.Errorf("reading working-set status: %w", err)
	}
	var dirtyTargets []string
	for _, v := range check.Violations {
		if _, tracked := committable[v.Table]; !tracked {
			continue
		}
		if _, dirty := dirtyBefore[v.Table]; dirty {
			dirtyTargets = append(dirtyTargets, v.Table)
		}
	}
	if len(dirtyTargets) > 0 {
		sort.Strings(dirtyTargets)
		return report, errors.Join(&PostConditionRepairDirtyError{Tables: dedupStrings(dirtyTargets)}, checkErr)
	}

	restoreTransactionCommit, err := suspendTransactionCommit(ctx, db)
	if err != nil {
		return report, err
	}
	defer func() { err = joinRestoreError(err, restoreTransactionCommit()) }()

	if err := unstagePreExistingTables(ctx, db, dirtyBefore); err != nil {
		return report, fmt.Errorf("unstaging pre-existing tables: %w", err)
	}

	var failures []string
	var toStage []string
	for _, v := range check.Violations {
		d := droppedDefault{table: v.Table, column: v.Column}
		if _, f := dropDefaultAndVerify(ctx, db, d, sql.NullString{String: v.Default, Valid: true}); f != nil {
			failures = append(failures, f.String())
			continue
		}
		report.Repaired = append(report.Repaired, v)
		fmt.Fprintf(stderr, "Repaired %s: dropped DEFAULT %s (GH#5269)\n", v.Name(), v.Default)
		if _, tracked := committable[v.Table]; tracked {
			toStage = append(toStage, v.Table)
		}
	}

	if len(toStage) > 0 {
		for _, table := range dedupStrings(toStage) {
			if err := DrainCall(ctx, db, "CALL DOLT_ADD(?)", table); err != nil {
				return report, errors.Join(fmt.Errorf("staging repaired table %s: %w", table, err), joinFailures(failures), checkErr)
			}
		}
		if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', ?)", postConditionRepairCommitMessage); err != nil {
			if !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
				return report, errors.Join(fmt.Errorf("committing repair: %w", err), joinFailures(failures), checkErr)
			}
		} else {
			report.Committed = true
		}
	}
	return report, errors.Join(joinFailures(failures), checkErr)
}

func joinFailures(failures []string) error {
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("could not repair recorded migration post-conditions (GH#5269): %s", strings.Join(failures, "; "))
}

func dedupStrings(in []string) []string {
	sorted := append([]string(nil), in...)
	sort.Strings(sorted)
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// MigrateUpAndRepairPostConditions is MigrateUp followed, on the same pinned
// session, by RepairRecordedPostConditions: the explicit `bd migrate schema`
// for callers without a migration lock (embedded mode, whose driver serializes
// access). MigrateUp's own consent gate and no-work return are unchanged; the
// repair runs once both sources are at their final shape.
func MigrateUpAndRepairPostConditions(ctx context.Context, db DBConn) (int, PostConditionRepairReport, error) {
	applied, err := MigrateUp(ctx, db)
	if err != nil {
		return applied, PostConditionRepairReport{}, err
	}
	report, err := RepairRecordedPostConditions(ctx, db)
	return applied, report, err
}

// WithPostConditionRepair makes MigrateUpWithLock run
// RepairRecordedPostConditions after a successful MigrateUp, under the same
// lock, and merge what it repaired into report. It also skips the lock-free
// convergence fast path, which would otherwise return before the repair could
// look: only the explicit `bd migrate schema` passes it.
func WithPostConditionRepair(report *PostConditionRepairReport) MigrateLockOption {
	return func(o *migrateLockOptions) {
		o.postConditionRepair = report
	}
}
