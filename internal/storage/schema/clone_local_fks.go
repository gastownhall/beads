package schema

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// CloneLocalFK describes a foreign key held by a clone-local (dolt_ignored,
// working-set-only) table.
//
// bd-7bpkd: CALL DOLT_RESET('--hard') silently DROPS every FK on every
// dolt_ignored table — including FKs whose referenced table is itself
// clone-local and was never touched by the reset (verified on dolt-sql-server
// 2.2.0 and 2.2.2). Enforcement stops, the loss survives server restarts, and
// orphan rows then accumulate until the constraint is re-added. Every
// production hard reset therefore goes through
// ResetHardPreservingCloneLocalFKs (ga-28co77), and `bd doctor --fix` heals a
// store severed before that existed.
//
// The spec lives here, beside the migrations that define these FKs, so the
// storage code that performs hard resets (this package and
// versioncontrolops) and the doctor can share it without an import cycle.
type CloneLocalFK struct {
	Table      string
	Constraint string
	Column     string
	RefTable   string
	RefColumn  string
}

// String names the constraint as table.constraint.
func (fk CloneLocalFK) String() string {
	return fk.Table + "." + fk.Constraint
}

// CloneLocalFKs lists every foreign key on a clone-local table (all use
// ON DELETE CASCADE ON UPDATE CASCADE). Keep in sync with the migrations that
// define them — main 0005/0042/0062 (events), 0021/0047/0058 (wisp aux), and
// their ignored-plane twins (ignored/0002, ignored/0004, ignored/0019).
// leases, local_metadata, repo_mtimes, and wisps carry no FKs;
// child_counters is tracked-plane (its FK survives resets).
// The cgo tests' drift guard asserts this list matches a freshly migrated
// store exactly.
var CloneLocalFKs = []CloneLocalFK{
	{Table: "events", Constraint: "fk_events_issue", Column: "issue_id", RefTable: "issues", RefColumn: "id"},
	{Table: "wisp_dependencies", Constraint: "fk_wisp_dep_issue", Column: "issue_id", RefTable: "wisps", RefColumn: "id"},
	{Table: "wisp_dependencies", Constraint: "fk_wisp_dep_wisp_target", Column: "depends_on_wisp_id", RefTable: "wisps", RefColumn: "id"},
	{Table: "wisp_dependencies", Constraint: "fk_wisp_dep_issue_target", Column: "depends_on_issue_id", RefTable: "issues", RefColumn: "id"},
	{Table: "wisp_labels", Constraint: "fk_wisp_labels_issue", Column: "issue_id", RefTable: "wisps", RefColumn: "id"},
	{Table: "wisp_comments", Constraint: "fk_wisp_comments_issue", Column: "issue_id", RefTable: "wisps", RefColumn: "id"},
	{Table: "wisp_events", Constraint: "fk_wisp_events_issue", Column: "issue_id", RefTable: "wisps", RefColumn: "id"},
	{Table: "wisp_child_counters", Constraint: "fk_wisp_child_counters_parent", Column: "parent_id", RefTable: "wisps", RefColumn: "id"},
}

// SeveredCloneLocalFK is one severed constraint found by the scan, with the
// number of orphaned rows that accumulated while enforcement was off.
type SeveredCloneLocalFK struct {
	CloneLocalFK
	Orphans int
}

// cloneLocalFKState is which spec FKs are present on the live schema, and
// which are missing from a table that does exist (severed). An FK whose table
// does not exist is neither: there is nothing to enforce or re-link. tables
// records which spec tables exist.
type cloneLocalFKState struct {
	present map[CloneLocalFK]bool
	severed []CloneLocalFK
	tables  map[string]bool
}

// readCloneLocalFKState reads the live clone-local FK state from
// information_schema on db's current session and database.
func readCloneLocalFKState(ctx context.Context, db DBConn) (cloneLocalFKState, error) {
	state := cloneLocalFKState{present: map[CloneLocalFK]bool{}, tables: map[string]bool{}}
	for _, fk := range CloneLocalFKs {
		exists, err := tableExists(ctx, db, fk.Table)
		if err != nil {
			return cloneLocalFKState{}, err
		}
		state.tables[fk.Table] = exists
		if !exists {
			continue
		}

		var constraints int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
			 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND CONSTRAINT_NAME = ? AND CONSTRAINT_TYPE = 'FOREIGN KEY'`,
			fk.Table, fk.Constraint,
		).Scan(&constraints); err != nil {
			return cloneLocalFKState{}, fmt.Errorf("check %s: %w", fk, err)
		}
		if constraints > 0 {
			state.present[fk] = true
		} else {
			state.severed = append(state.severed, fk)
		}
	}
	return state, nil
}

// tableExists reports whether table exists in db's current database.
func tableExists(ctx context.Context, db DBConn, table string) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`,
		table,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("check %s exists: %w", table, err)
	}
	return n > 0, nil
}

// ScanSeveredCloneLocalFKs reports which clone-local FKs are missing from the
// live schema, with each one's orphan count. Used by `bd doctor`.
func ScanSeveredCloneLocalFKs(ctx context.Context, db DBConn) ([]SeveredCloneLocalFK, error) {
	state, err := readCloneLocalFKState(ctx, db)
	if err != nil {
		return nil, err
	}
	var severed []SeveredCloneLocalFK
	for _, fk := range state.severed {
		var orphans int
		//nolint:gosec // G201: identifiers come from the fixed CloneLocalFKs spec above, not user input.
		orphanCount := fmt.Sprintf(
			"SELECT COUNT(*) FROM `%s` t WHERE t.`%s` IS NOT NULL AND NOT EXISTS (SELECT 1 FROM `%s` r WHERE r.`%s` = t.`%s`)",
			fk.Table, fk.Column, fk.RefTable, fk.RefColumn, fk.Column,
		)
		if err := db.QueryRowContext(ctx, orphanCount).Scan(&orphans); err != nil {
			return nil, fmt.Errorf("count %s orphans: %w", fk.Table, err)
		}
		severed = append(severed, SeveredCloneLocalFK{CloneLocalFK: fk, Orphans: orphans})
	}
	return severed, nil
}

// RelinkCloneLocalFK deletes the rows of fk's table that violate fk (ADD
// CONSTRAINT validates existing rows, so they must go first), then re-adds the
// constraint in place. Verified on dolt 2.2.2: the re-added FK resolves
// against the current tracked root and enforces again. It returns how many
// orphaned rows were removed.
//
// The DELETE removes EVERY row of fk's table that violates fk, however it got
// there. On a store severed for months that can be hundreds of thousands of
// rows: only `bd doctor --fix` and a just-dropped FK
// (ResetHardPreservingCloneLocalFKs) may call this. Identifiers come from the
// fixed spec and are backtick-quoted anyway.
func RelinkCloneLocalFK(ctx context.Context, db DBConn, fk CloneLocalFK) (removed int64, err error) {
	//nolint:gosec // G201: identifiers come from the fixed CloneLocalFKs spec, not user input.
	deleteOrphans := fmt.Sprintf(
		"DELETE FROM `%s` WHERE `%s` IS NOT NULL AND NOT EXISTS (SELECT 1 FROM `%s` r WHERE r.`%s` = `%s`.`%s`)",
		fk.Table, fk.Column, fk.RefTable, fk.RefColumn, fk.Table, fk.Column,
	)
	result, err := db.ExecContext(ctx, deleteOrphans)
	if err != nil {
		return 0, fmt.Errorf("delete %s orphans: %w", fk.Table, err)
	}
	removed, _ = result.RowsAffected()

	//nolint:gosec // G201: identifiers come from the fixed CloneLocalFKs spec, not user input.
	addConstraint := fmt.Sprintf(
		"ALTER TABLE `%s` ADD CONSTRAINT `%s` FOREIGN KEY (`%s`) REFERENCES `%s` (`%s`) ON DELETE CASCADE ON UPDATE CASCADE",
		fk.Table, fk.Constraint, fk.Column, fk.RefTable, fk.RefColumn,
	)
	if _, err := db.ExecContext(ctx, addConstraint); err != nil {
		// The DELETE already committed: say how many rows it removed, so a
		// failed re-link is never mistaken for an untouched table.
		return removed, fmt.Errorf("re-add %s after deleting %d orphaned row(s): %w", fk, removed, err)
	}
	return removed, nil
}

// ErrHardResetNotRun marks a ResetHardPreservingCloneLocalFKs failure that
// happened before the reset: nothing was reset. Best-effort recovery callers
// (abortMerge) fall back to a bare reset on it.
var ErrHardResetNotRun = errors.New("hard reset not run")

// RelinkedCloneLocalFK is a constraint a hard reset dropped and
// ResetHardPreservingCloneLocalFKs re-added, with the number of that FK's
// orphan rows the helper deleted first: normally the rows the reset itself
// orphaned, plus any older orphans the FK had (see the helper's doc).
type RelinkedCloneLocalFK struct {
	CloneLocalFK
	OrphansRemoved int64
}

// ResetHardResult reports what ResetHardPreservingCloneLocalFKs did to the
// clone-local FKs around its reset.
type ResetHardResult struct {
	// Relinked are the FKs present before the reset that the reset dropped
	// and the helper re-added.
	Relinked []RelinkedCloneLocalFK
	// RemovedWithTable are FKs present before the reset whose own table, or
	// the table they reference, the reset removed (for example a
	// fresh-bootstrap heal discarding an uncommitted table): the FK went with
	// its table, so there is nothing to re-link. When the FK's own table is
	// gone, its rows went too. When only the referenced table is gone, the
	// surviving table's rows may now point at nothing; they are left as they
	// are, never purged here, for the same reason AlreadySevered rows are not.
	// Not a failure, and not in Warning().
	RemovedWithTable []CloneLocalFK
	// AlreadySevered are the FKs that were already missing before the reset.
	// They are left exactly as found — no orphan purge, no re-add — because a
	// store severed for months can hold hundreds of thousands of orphans and a
	// routine reset must never spring that DELETE. Callers must surface
	// Warning() where an operator reads it.
	AlreadySevered []CloneLocalFK
}

// Warning is the operator-facing notice for AlreadySevered, or "" when every
// clone-local FK was enforcing before the reset.
func (r ResetHardResult) Warning() string {
	if len(r.AlreadySevered) == 0 {
		return ""
	}
	names := make([]string, 0, len(r.AlreadySevered))
	for _, fk := range r.AlreadySevered {
		names = append(names, fk.String())
	}
	return fmt.Sprintf("%d clone-local foreign key(s) were already severed before this hard reset and were left as found: %s; "+
		"run 'bd doctor --fix' to remove their orphaned rows and re-link them",
		len(names), strings.Join(names, ", "))
}

// CloneLocalFKRelinkFailure is one FK ResetHardPreservingCloneLocalFKs could
// not restore after a successful reset.
type CloneLocalFKRelinkFailure struct {
	FK  CloneLocalFK
	Err error
}

// CloneLocalFKRelinkError means the hard reset SUCCEEDED but the clone-local
// FKs are not known to be restored. Either:
//
//   - Failures: FKs the reset is confirmed to have dropped whose re-link
//     failed — enforcement on them is off until `bd doctor --fix` runs; or
//   - VerifyErr: the post-reset probe failed, so whether the reset dropped any
//     of Unverified (the FKs present before it) is UNKNOWN.
type CloneLocalFKRelinkError struct {
	Failures   []CloneLocalFKRelinkFailure
	Unverified []CloneLocalFK
	VerifyErr  error
}

func (e *CloneLocalFKRelinkError) Error() string {
	if e.VerifyErr != nil {
		names := make([]string, 0, len(e.Unverified))
		for _, fk := range e.Unverified {
			names = append(names, fk.String())
		}
		return fmt.Sprintf("hard reset succeeded, but could not verify the clone-local foreign keys afterwards (%v): "+
			"whether it dropped any of the %d present before it (%s) is unknown; "+
			"run 'bd doctor' to check them and 'bd doctor --fix' to re-link any that are missing",
			e.VerifyErr, len(names), strings.Join(names, ", "))
	}
	parts := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		parts = append(parts, fmt.Sprintf("%s: %v", f.FK, f.Err))
	}
	return fmt.Sprintf("hard reset succeeded, but %d clone-local foreign key(s) it dropped could not be re-linked (%s); "+
		"enforcement is off until 'bd doctor --fix' re-links them",
		len(e.Failures), strings.Join(parts, "; "))
}

func (e *CloneLocalFKRelinkError) Unwrap() []error {
	errs := make([]error, 0, len(e.Failures)+1)
	if e.VerifyErr != nil {
		errs = append(errs, e.VerifyErr)
	}
	for _, f := range e.Failures {
		errs = append(errs, f.Err)
	}
	return errs
}

// ResetHardPreservingCloneLocalFKs runs CALL DOLT_RESET('--hard'[, target])
// and restores the clone-local FKs the reset drops (bd-7bpkd, ga-28co77):
//
//  1. read which clone-local FKs are present before the reset;
//  2. run the reset;
//  3. for each FK present before and missing after, delete THAT FK's orphans
//     and re-add the constraint exactly as `bd doctor --fix` does — unless the
//     reset removed the FK's own table or the table it references, in which
//     case the FK went with its table: it is listed in
//     Result.RemovedWithTable and not touched (upstream review, #6772).
//
// The DELETE removes every orphan of an FK the reset dropped. That equals
// "the rows the reset orphaned" (e.g. events rows whose issue the reset
// removed) only if the FK was actually enforcing before the reset; a write
// made under foreign_key_checks = 0 (dolt/transaction.go does so for
// cross-tier wisp dependencies) can leave an older orphan that goes too.
//
// Cost: every FK the reset drops costs an anti-join DELETE plus the ADD
// CONSTRAINT's validation scan of its table, on every reset, even with no
// orphans.
//
// Race: nothing enforces the FK between the DELETE and the ALTER, so an orphan
// another session inserts in that window makes the ALTER fail; the caller
// then gets the reset-succeeded error below (compact and flatten still clean
// up their temp branch).
//
// An FK already missing before the reset is not touched; it is listed in
// Result.AlreadySevered for the caller to surface.
//
// conn must be the single session the reset belongs to (a pinned *sql.Conn,
// or the caller's existing session): the probe, the reset, and the re-link
// must all see the same branch and working set. A *sql.DB pool could run the
// ALTER on another connection — another session, possibly another branch.
//
// Errors: if the pre-reset probe fails, the reset is not run and the error
// wraps ErrHardResetNotRun. A reset failure is returned as-is. If the reset
// succeeds and any re-link fails, the error is a *CloneLocalFKRelinkError
// naming each FK; the helper still attempts every other FK first. If the
// reset succeeds and the post-reset probe fails, it is a
// *CloneLocalFKRelinkError whose VerifyErr is set: the FK state is unknown.
func ResetHardPreservingCloneLocalFKs(ctx context.Context, conn DBConn, target string) (ResetHardResult, error) {
	var result ResetHardResult

	before, err := readCloneLocalFKState(ctx, conn)
	if err != nil {
		return result, fmt.Errorf("%w: could not read clone-local foreign keys first: %w", ErrHardResetNotRun, err)
	}
	result.AlreadySevered = before.severed

	// Drained, not Exec'd: an undrained procedure result set would poison the
	// statements that follow on this same session.
	if target == "" {
		err = DrainCall(ctx, conn, "CALL DOLT_RESET('--hard')")
	} else {
		err = DrainCall(ctx, conn, "CALL DOLT_RESET('--hard', ?)", target)
	}
	if err != nil {
		return result, err
	}

	if len(before.present) == 0 {
		return result, nil
	}

	after, err := readCloneLocalFKState(ctx, conn)
	if err != nil {
		var unverified []CloneLocalFK
		for _, fk := range CloneLocalFKs {
			if before.present[fk] {
				unverified = append(unverified, fk)
			}
		}
		return result, &CloneLocalFKRelinkError{Unverified: unverified, VerifyErr: err}
	}

	var failures []CloneLocalFKRelinkFailure
	refExists := map[string]bool{}

	for _, fk := range CloneLocalFKs {
		if !before.present[fk] || after.present[fk] {
			continue
		}
		// The FK is gone. If the reset removed its table, or the table it
		// references, the FK went with that table: nothing to re-link.
		if !after.tables[fk.Table] {
			result.RemovedWithTable = append(result.RemovedWithTable, fk)
			continue
		}
		exists, seen := refExists[fk.RefTable]
		if !seen {
			var err error
			if exists, err = tableExists(ctx, conn, fk.RefTable); err != nil {
				failures = append(failures, CloneLocalFKRelinkFailure{FK: fk, Err: err})
				continue
			}
			refExists[fk.RefTable] = exists
		}
		if !exists {
			result.RemovedWithTable = append(result.RemovedWithTable, fk)
			continue
		}
		removed, err := RelinkCloneLocalFK(ctx, conn, fk)
		if err != nil {
			failures = append(failures, CloneLocalFKRelinkFailure{FK: fk, Err: err})
			continue
		}
		result.Relinked = append(result.Relinked, RelinkedCloneLocalFK{CloneLocalFK: fk, OrphansRemoved: removed})
	}
	if len(failures) > 0 {
		return result, &CloneLocalFKRelinkError{Failures: failures}
	}
	return result, nil
}
