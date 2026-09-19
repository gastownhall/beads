package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// This file backs `bd conflicts` (federation ask #3): the issue-oriented view
// of a halted merge that spares operators the raw dolt CLI
// (`dolt conflicts cat`, `dolt conflicts resolve`, `dolt add -A && dolt
// commit`). It reads dolt's per-table conflict tables — dolt_conflicts_<t>,
// whose columns are base_<col>/our_<col>/their_<col> plus the
// our_diff_type/their_diff_type metadata — and re-presents them as one row per
// conflicted issue with its fields side by side.
//
// It is deliberately generic over tables (issues, dependencies, config,
// comments, …): the column split is derived from the result-set column names,
// not from a hardcoded schema that would rot on the next migration.

// ConflictStrategyOurs / ConflictStrategyTheirs are the two resolution
// strategies, matching `dolt conflicts resolve --ours|--theirs`.
const (
	ConflictStrategyOurs   = "ours"
	ConflictStrategyTheirs = "theirs"
)

// conflictSides are dolt's conflict-column prefixes. No prefix is a prefix of
// another, so a column is split by at most one of them and a field named e.g.
// "their_thing" survives as our_their_thing -> (our, their_thing).
var conflictSides = []string{"base_", "our_", "their_"}

// conflictMetaSuffixes are conflict-table columns that describe the conflict
// rather than the row's data: dolt adds our_diff_type/their_diff_type always,
// and base_cardinality/our_cardinality/their_cardinality for keyless tables.
var conflictMetaSuffixes = map[string]bool{
	"diff_type":   true,
	"cardinality": true,
}

// conflictRowKeyColumn maps a table to the column that identifies a row for
// per-row resolution. Only tables listed here support resolving individual
// rows; everything else is resolvable table-wide (DOLT_CONFLICTS_RESOLVE),
// which is what dolt itself offers.
var conflictRowKeyColumn = map[string]string{
	"issues": "id",
}

// ValidateConflictTable rejects anything that is not a plain SQL identifier,
// so a table name can be safely interpolated into a conflict query. Conflict
// table names come from dolt_conflicts (or an operator's --table) and MySQL
// cannot parameterize an identifier, so they are validated instead. It reuses
// the package's existing table-name gate, which ResolveConflicts already
// trusts for exactly this.
func ValidateConflictTable(table string) error {
	return validateTableName(table)
}

// ValidateConflictStrategy accepts only the two dolt strategies.
func ValidateConflictStrategy(strategy string) error {
	switch strategy {
	case ConflictStrategyOurs, ConflictStrategyTheirs:
		return nil
	default:
		return fmt.Errorf("invalid strategy %q (want %q or %q)", strategy, ConflictStrategyOurs, ConflictStrategyTheirs)
	}
}

// SupportsRowResolve reports whether table can be resolved row by row.
func SupportsRowResolve(table string) bool {
	_, ok := conflictRowKeyColumn[table]
	return ok
}

// splitConflictColumn splits a dolt conflict column into its side and field
// name. ok is false for columns that belong to no side (dolt does not emit
// any today, but a future column must not be silently treated as a field).
func splitConflictColumn(col string) (side, field string, ok bool) {
	for _, s := range conflictSides {
		if strings.HasPrefix(col, s) {
			return strings.TrimSuffix(s, "_"), strings.TrimPrefix(col, s), true
		}
	}
	return "", "", false
}

// formatConflictValue renders a scanned SQL value for display. A NULL becomes
// a nil pointer — distinct from the empty string, which is a real value.
func formatConflictValue(v any) *string {
	if v == nil {
		return nil
	}
	var s string
	switch t := v.(type) {
	case []byte:
		s = string(t)
	case string:
		s = t
	case time.Time:
		s = t.UTC().Format(time.RFC3339)
	default:
		s = fmt.Sprint(t)
	}
	return &s
}

// GetConflictRows returns the live conflicted rows of table, one entry per
// conflicted row with its columns presented per field. It reads the working
// set: a merge whose conflicts were aborted (the auto-settle path) leaves
// nothing here, which is correct — those conflicts no longer exist.
func GetConflictRows(ctx context.Context, db DBConn, table string) ([]storage.ConflictRow, error) {
	if err := ValidateConflictTable(table); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT * FROM `dolt_conflicts_"+table+"`") //nolint:gosec // table validated as an identifier above
	if err != nil {
		return nil, fmt.Errorf("query conflicts for table %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("conflict columns for table %s: %w", table, err)
	}

	var out []storage.ConflictRow
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan conflict row for table %s: %w", table, err)
		}
		out = append(out, buildConflictRow(table, cols, vals))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conflicts for table %s: %w", table, err)
	}
	return out, nil
}

// buildConflictRow re-presents one raw conflict row as fields. It is pure, so
// the column-splitting rules are unit-testable without a database.
func buildConflictRow(table string, cols []string, vals []any) storage.ConflictRow {
	row := storage.ConflictRow{Table: table}
	index := make(map[string]int, len(cols))
	for i, col := range cols {
		side, field, ok := splitConflictColumn(col)
		if !ok {
			continue
		}
		v := formatConflictValue(vals[i])
		if conflictMetaSuffixes[field] {
			if field == "diff_type" && v != nil {
				switch side {
				case "our":
					row.OurDiffType = *v
				case "their":
					row.TheirDiffType = *v
				}
			}
			continue
		}
		pos, seen := index[field]
		if !seen {
			pos = len(row.Fields)
			index[field] = pos
			row.Fields = append(row.Fields, storage.ConflictFieldValue{Name: field})
		}
		switch side {
		case "base":
			row.Fields[pos].Base = v
		case "our":
			row.Fields[pos].Ours = v
		case "their":
			row.Fields[pos].Theirs = v
		}
		if v != nil {
			switch side {
			case "base":
				row.BaseExists = true
			case "our":
				row.OurExists = true
			case "their":
				row.TheirExists = true
			}
		}
	}
	row.Key = conflictRowKey(table, row.Fields)
	return row
}

// conflictRowKey picks the value that identifies a conflicted row: the table's
// key column when it has one (issues.id), else the first field carrying any
// value. Presentation only — resolution keys off conflictRowKeyColumn.
func conflictRowKey(table string, fields []storage.ConflictFieldValue) string {
	keyCol, hasKey := conflictRowKeyColumn[table]
	for _, f := range fields {
		if hasKey && f.Name != keyCol {
			continue
		}
		for _, v := range []*string{f.Ours, f.Theirs, f.Base} {
			if v != nil {
				return *v
			}
		}
		if hasKey {
			return ""
		}
	}
	return ""
}

// ResolveConflictRows resolves the named rows of table with strategy,
// leaving every other conflicted row of that table conflicted. Only tables in
// conflictRowKeyColumn support it, and only modify/modify rows (present on
// both sides): a delete/modify or add/add row is refused by name rather than
// guessed at, because "ours" and "theirs" do not describe what the operator
// wants when one side deleted the row.
//
// db must be a SINGLE session (a pinned *sql.Conn, or a *sql.Tx): the
// conflict-tolerance flags set here have to be visible to the writes that
// follow, and dolt refuses to commit a transaction touching a table with live
// conflicts without them.
func ResolveConflictRows(ctx context.Context, db DBConn, table string, keys []string, strategy string) (int, error) {
	if err := ValidateConflictTable(table); err != nil {
		return 0, err
	}
	if err := ValidateConflictStrategy(strategy); err != nil {
		return 0, err
	}
	keyCol, ok := conflictRowKeyColumn[table]
	if !ok {
		return 0, fmt.Errorf("per-row conflict resolution is not supported for table %s; resolve the whole table instead", table)
	}
	if len(keys) == 0 {
		return 0, nil
	}
	// A conflicted table cannot be written — nor the session committed —
	// without dolt's conflict-tolerance flags (the same pair MergeAndSettle
	// sets). They are session state, which is why db must be one session.
	if _, err := db.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		return 0, fmt.Errorf("set dolt_allow_commit_conflicts: %w", err)
	}
	if _, err := db.ExecContext(ctx, "SET @@dolt_force_transaction_commit = 1"); err != nil {
		return 0, fmt.Errorf("set dolt_force_transaction_commit: %w", err)
	}

	resolved := 0
	for _, key := range keys {
		row, err := loadConflictRow(ctx, db, table, keyCol, key)
		if err != nil {
			return resolved, err
		}
		if err := resolveOneConflictRow(ctx, db, table, keyCol, key, strategy, row); err != nil {
			return resolved, err
		}
		resolved++
	}
	return resolved, nil
}

// rawConflictRow is one conflict row kept in raw scanned form, so their_*
// values can be written straight back into the base table.
type rawConflictRow struct {
	cols []string
	vals []any
}

// value returns the raw value of side_field, and whether the column exists.
func (r rawConflictRow) value(side, field string) (any, bool) {
	want := side + "_" + field
	for i, c := range r.cols {
		if c == want {
			return r.vals[i], true
		}
	}
	return nil, false
}

// theirFields returns their_* data columns (metadata and the key column
// excluded) in conflict-table order.
func (r rawConflictRow) theirFields(keyCol string) (names []string, vals []any) {
	for i, c := range r.cols {
		side, field, ok := splitConflictColumn(c)
		if !ok || side != "their" || conflictMetaSuffixes[field] || field == keyCol {
			continue
		}
		names = append(names, field)
		vals = append(vals, r.vals[i])
	}
	return names, vals
}

// loadConflictRow fetches the single conflict row for key. It matches on
// either side's key column so a row only one side has is *found* and then
// refused with a precise message, rather than reported as "no conflict".
func loadConflictRow(ctx context.Context, db DBConn, table, keyCol, key string) (rawConflictRow, error) {
	q := fmt.Sprintf("SELECT * FROM `dolt_conflicts_%s` WHERE `our_%s` = ? OR `their_%s` = ?", table, keyCol, keyCol) //nolint:gosec // identifiers validated
	rows, err := db.QueryContext(ctx, q, key, key)
	if err != nil {
		return rawConflictRow{}, fmt.Errorf("query conflict for %s %s: %w", table, key, err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return rawConflictRow{}, fmt.Errorf("conflict columns for table %s: %w", table, err)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return rawConflictRow{}, fmt.Errorf("query conflict for %s %s: %w", table, key, err)
		}
		return rawConflictRow{}, fmt.Errorf("no live conflict for %s %s", table, key)
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return rawConflictRow{}, fmt.Errorf("scan conflict for %s %s: %w", table, key, err)
	}
	if rows.Next() {
		return rawConflictRow{}, fmt.Errorf("multiple conflict rows for %s %s; resolve the whole table instead", table, key)
	}
	return rawConflictRow{cols: cols, vals: vals}, errors.Join(rows.Err(), rows.Close())
}

// conflictTargetStillPresent reports whether key still names a row of table.
//
// It is the matched-rows check the resolvers need after a write, because
// RowsAffected is rows CHANGED, not rows MATCHED: the DSN sets parseTime and
// multiStatements but NOT clientFoundRows (doltutil/dsn.go), so an UPDATE the
// backend normalizes to the bytes already stored reports zero exactly as a
// vanished row does. Only asking can tell the two apart.
//
// It confirms that the key still resolves to a row — NOT that our values are
// the stored ones. On the autocommit path (an embedded Pull, where db is not a
// transaction) a row deleted and re-inserted between the UPDATE and this check
// would read as present; in server mode the caller holds a transaction and the
// window does not exist. Comparing the written values instead would reintroduce
// the very normalization sensitivity this check exists to absorb.
func conflictTargetStillPresent(ctx context.Context, db DBConn, table, keyCol string, key any) (bool, error) {
	if err := ValidateConflictTable(table); err != nil {
		return false, err
	}
	if err := ValidateConflictTable(keyCol); err != nil {
		return false, err
	}
	//nolint:gosec // table and key column validated as identifiers just above.
	q := fmt.Sprintf("SELECT COUNT(*) FROM `%s` WHERE `%s` = ?", table, keyCol)
	var n int
	if err := db.QueryRowContext(ctx, q, key).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// resolveOneConflictRow applies strategy to a single modify/modify row.
//
// "ours" is dolt's manual-resolution path: the working set already holds our
// values, so deleting the conflict row *is* the resolution. "theirs" first
// writes their values over ours, then deletes the conflict row — the order
// matters, since the delete is what tells dolt the row is settled.
func resolveOneConflictRow(ctx context.Context, db DBConn, table, keyCol, key, strategy string, row rawConflictRow) error {
	ourKey, ourOK := row.value("our", keyCol)
	theirKey, theirOK := row.value("their", keyCol)
	if !ourOK || !theirOK {
		return fmt.Errorf("conflict table dolt_conflicts_%s has no our_%s/their_%s column", table, keyCol, keyCol)
	}
	if ourKey == nil || theirKey == nil {
		// delete/modify (one side removed the row) or add/add against a
		// missing key: refuse by name. Row-level ours/theirs would silently
		// resurrect or destroy a row the operator never looked at.
		return fmt.Errorf("conflict for %s %s is not a modify/modify conflict (one side has no row); "+
			"resolve it with a whole-table strategy or edit the row directly", table, key)
	}

	if strategy == ConflictStrategyTheirs {
		names, vals := row.theirFields(keyCol)
		if len(names) == 0 {
			return fmt.Errorf("conflict for %s %s carries no their_* data columns", table, key)
		}
		sets := make([]string, len(names))
		args := make([]any, 0, len(names)+1)
		for i, n := range names {
			// Column names are interpolated (MySQL cannot bind an
			// identifier) and come from the conflict table's own schema,
			// which a peer's schema merge can extend — gate them exactly
			// like the table name rather than trusting the source.
			if err := ValidateConflictTable(n); err != nil {
				return fmt.Errorf("refusing to write unexpected column %q of %s: %w", n, table, err)
			}
			sets[i] = fmt.Sprintf("`%s` = ?", n)
			args = append(args, vals[i])
		}
		args = append(args, ourKey)
		stmt := fmt.Sprintf("UPDATE `%s` SET %s WHERE `%s` = ?", table, strings.Join(sets, ", "), keyCol) //nolint:gosec // identifiers validated above
		res, err := db.ExecContext(ctx, stmt, args...)
		if err != nil {
			return fmt.Errorf("apply their values for %s %s: %w", table, key, err)
		}
		// Zero rows would mean the row we read the conflict for is no longer
		// there — another session on the same branch deleted it between the
		// read and the write. Clearing the conflict now would discard their
		// side under a --theirs invocation, undetectably. But zero is not
		// proof of that on its own (see conflictTargetStillPresent), so ask
		// before refusing: an operator who named this row deserves the abort
		// only when the row really is gone.
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			present, err := conflictTargetStillPresent(ctx, db, table, keyCol, ourKey)
			if err != nil {
				return fmt.Errorf("confirm %s %s still exists after writing their values: %w", table, key, err)
			}
			if !present {
				return fmt.Errorf("their values for %s %s matched no row (was it deleted concurrently?); conflict left unresolved", table, key)
			}
		}
	}

	del := fmt.Sprintf("DELETE FROM `dolt_conflicts_%s` WHERE `our_%s` = ?", table, keyCol) //nolint:gosec // identifiers validated
	res, err := db.ExecContext(ctx, del, ourKey)
	if err != nil {
		return fmt.Errorf("clear conflict for %s %s: %w", table, key, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("conflict for %s %s was not cleared (no conflict row deleted)", table, key)
	}
	return nil
}

// GetMergeBlockers reports the merge state that `bd conflicts` cannot show as
// rows: whether a merge is open at all, plus the schema conflicts and
// constraint violations that make dolt refuse the merge commit even when
// every dolt_conflicts row is resolved (wy-36ilm F12). Without it, that state
// surfaced only as a raw dolt error from CommitMergeResolution, after the
// operator had been told "0 conflicts remain".
//
// Each source is read independently and a MISSING source table is not an
// error: dolt_schema_conflicts and dolt_constraint_violations are dolt system
// tables whose presence has varied across versions, and a diagnosis helper
// must never be the thing that fails the command.
func GetMergeBlockers(ctx context.Context, db DBConn) (storage.MergeBlockers, error) {
	var out storage.MergeBlockers
	var errs []error

	if merging, err := mergeInProgress(ctx, db); err != nil {
		errs = append(errs, err)
	} else {
		out.Merging = merging
	}
	if tables, err := schemaConflictTables(ctx, db); err != nil {
		errs = append(errs, err)
	} else {
		out.SchemaConflictTables = tables
	}
	if violations, err := constraintViolationCounts(ctx, db); err != nil {
		errs = append(errs, err)
	} else {
		out.ConstraintViolations = violations
	}
	// Partial results are still useful — the caller gets what was readable
	// plus the reason the rest was not.
	return out, errors.Join(errs...)
}

// mergeInProgress reads dolt_merge_status.is_merging: true between a halted
// DOLT_MERGE and its concluding commit. It is what lets `--conclude` tell
// "resolved but uncommitted" apart from "nothing to conclude".
func mergeInProgress(ctx context.Context, db DBConn) (bool, error) {
	var merging bool
	err := db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging)
	if err != nil {
		if isMissingSystemTable(err) || errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("query merge status: %w", err)
	}
	return merging, nil
}

// schemaConflictTables lists the tables whose SCHEMAS conflict — dolt keeps
// them out of dolt_conflicts entirely, so totalConflicts cannot see them.
func schemaConflictTables(ctx context.Context, db DBConn) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT table_name FROM dolt_schema_conflicts")
	if err != nil {
		if isMissingSystemTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("query schema conflicts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan schema conflict: %w", err)
		}
		tables = append(tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema conflicts: %w", err)
	}
	return tables, nil
}

// constraintViolationCounts lists the tables carrying outstanding constraint
// violations. mergesettle.go repairs the FK-cascade class on the auto path;
// anything it declined lands here, blocking the commit.
func constraintViolationCounts(ctx context.Context, db DBConn) ([]storage.ConstraintViolation, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT `table`, num_violations FROM dolt_constraint_violations WHERE num_violations > 0")
	if err != nil {
		if isMissingSystemTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("query constraint violations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []storage.ConstraintViolation
	for rows.Next() {
		var v storage.ConstraintViolation
		if err := rows.Scan(&v.Table, &v.Count); err != nil {
			return nil, fmt.Errorf("scan constraint violation: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate constraint violations: %w", err)
	}
	return out, nil
}

// isMissingSystemTable reports whether err is dolt/MySQL's "table does not
// exist". Matched on the message because the embedded engine and the MySQL
// driver report it as different error types.
func isMissingSystemTable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "table not found") ||
		strings.Contains(msg, "doesn't exist") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "unknown table")
}
