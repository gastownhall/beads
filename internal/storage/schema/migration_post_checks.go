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

// droppedDefaultRe finds the column defaults a migration body drops. The
// bodies that drop one (main 0043, 0050 and 0051, ignored 0010) do it inside a
// guard: SET @sql = IF(<the default is still there>, 'ALTER TABLE t ALTER
// COLUMN c DROP DEFAULT', 'SELECT 1'); PREPARE stmt FROM @sql; EXECUTE stmt.
var droppedDefaultRe = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+` + "`?" + `(\w+)` + "`?" +
	`\s+ALTER\s+COLUMN\s+` + "`?" + `(\w+)` + "`?" + `\s+DROP\s+DEFAULT`)

type droppedDefault struct {
	table  string
	column string
}

// droppedDefaults lists the (table, column) defaults sqlText drops, in order of
// first appearance.
func droppedDefaults(sqlText string) []droppedDefault {
	var out []droppedDefault
	seen := make(map[droppedDefault]bool)
	for _, m := range droppedDefaultRe.FindAllStringSubmatch(sqlText, -1) {
		d := droppedDefault{table: strings.ToLower(m[1]), column: strings.ToLower(m[2])}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// dropDefaultSQL is the direct, identifier-quoted statement that removes a
// lingering column default. The frozen bodies drop defaults through a guarded
// statement (SET @sql = IF(...); PREPARE; EXECUTE) precisely so a re-run on an
// already-correct store is a no-op — but that guard is also what let a store
// end up with the mark recorded while the default survived: a PREPARE/EXECUTE
// that reports success without applying (the shape of dolthub/dolt#11345)
// leaves nothing to retry from, because the mark now skips the body. The
// direct statement asserts beads' declared schema unconditionally; it is only
// issued after a probe observed the default still present, so a healthy store
// never sees it.
func dropDefaultSQL(table, column string) string {
	return "ALTER TABLE `" + table + "` ALTER COLUMN `" + column + "` DROP DEFAULT"
}

// PostConditionFailure names one dropped-default post-condition a pass could
// not assert: the column, the default that survived (when one was observed),
// and why the direct correction did not land.
type PostConditionFailure struct {
	// Source is the cursor table of the migration series that owns the
	// obligation ("schema_migrations" / "ignored_schema_migrations").
	Source string
	// Version is the migration the obligation belongs to.
	Version int
	Table   string
	Column  string
	// Observed is the default the probe read, when it read one.
	Observed sql.NullString
	// Cause is why the condition could not be asserted: a failed probe, a
	// failed correction, or a default that survived the correction.
	Cause string
}

func (f PostConditionFailure) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s.%s (v%04d, %s)", f.Table, f.Column, f.Version, f.Source)
	if f.Observed.Valid {
		fmt.Fprintf(&b, " still has DEFAULT %s", f.Observed.String)
	}
	fmt.Fprintf(&b, ": %s", f.Cause)
	return b.String()
}

// PostConditionError is returned when a migration's dropped-default
// post-conditions could not all be asserted after direct correction. The
// caller must not record the migration's cursor row: the mark is the only
// record that a migration applied, and once written every later pass trusts it
// and skips the body (GH#5269).
type PostConditionError struct {
	Failures []PostConditionFailure
}

func (e *PostConditionError) Error() string {
	parts := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		parts = append(parts, f.String())
	}
	sort.Strings(parts)
	return fmt.Sprintf("%d dropped-default post-condition(s) unmet and uncorrectable (GH#5269): %s",
		len(e.Failures), strings.Join(parts, "; "))
}

// enforceDroppedDefaults verifies, after a migration body returned and before
// runMigrations records the migration's cursor row, that every column default
// the body drops is gone (GH#5269).
//
// The cursor row is the only record that a migration applied. Once it is
// written, every later pass trusts it and skips the body. So a body that
// returned no error but left a guarded DROP DEFAULT undone turns into a store
// that claims the version while still minting ids from the dropped default, and
// re-running `bd migrate` cannot repair it. A store was reported carrying
// v50-v53 marks with the defaults 0050 and 0051 drop still in place.
//
// A default the probe still observes is corrected directly — one
// identifier-quoted ALTER TABLE ... ALTER COLUMN ... DROP DEFAULT, then a
// re-probe — and only a correction that provably landed lets the mark through.
// The correction dirties the table after the step's dirty-table snapshot, so
// the step's commit records the corrected DDL together with the cursor row and
// no later pass can resurrect the drift. The corrections that landed are
// returned so the caller can report them. Any condition that cannot be probed
// or corrected makes the whole step fail: the mark is withheld, nothing claims
// the version, and the next pass runs the body again, whose guard makes it
// safe to repeat.
//
// It runs only while a migration is being applied, never on the steady-state
// open path. A table the store does not have is skipped, as the body's own
// guard skips it.
func enforceDroppedDefaults(ctx context.Context, db DBConn, src migrationSource, version int, sqlText string) ([]droppedDefault, error) {
	targets := droppedDefaults(sqlText)
	if len(targets) == 0 {
		return nil, nil
	}

	// Probe every target first: one unreadable column must not hide the others.
	type violation struct {
		target   droppedDefault
		observed sql.NullString
	}
	var violations []violation
	var failures []PostConditionFailure
	fail := func(d droppedDefault, observed sql.NullString, cause string) {
		failures = append(failures, PostConditionFailure{
			Source: src.cursorTable, Version: version,
			Table: d.table, Column: d.column, Observed: observed, Cause: cause,
		})
	}
	for _, d := range targets {
		present, err := showTableExists(ctx, db, d.table)
		if err != nil {
			fail(d, sql.NullString{}, fmt.Sprintf("probe failed: %v", err))
			continue
		}
		if !present {
			continue
		}
		def, err := showColumnDefault(ctx, db, d.table, d.column)
		if err != nil {
			fail(d, sql.NullString{}, fmt.Sprintf("probe failed: %v", err))
			continue
		}
		if def.Valid {
			violations = append(violations, violation{target: d, observed: def})
		}
	}

	var corrections []droppedDefault
	for _, v := range violations {
		stmt := dropDefaultSQL(v.target.table, v.target.column)
		//nolint:gosec // G201: table and column come from the frozen, embedded migration body.
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			fail(v.target, v.observed, fmt.Sprintf("correction %s failed: %v", stmt, err))
			continue
		}
		def, err := showColumnDefault(ctx, db, v.target.table, v.target.column)
		if err != nil {
			fail(v.target, v.observed, fmt.Sprintf("probe after correction failed: %v", err))
			continue
		}
		if def.Valid {
			fail(v.target, def, fmt.Sprintf("correction %s reported success but the default survived", stmt))
			continue
		}
		corrections = append(corrections, v.target)
	}

	if len(failures) > 0 {
		return corrections, &PostConditionError{Failures: failures}
	}
	return corrections, nil
}

// showColumnDefault reads a column's Default from SHOW COLUMNS. It is SQL NULL
// when the column has no default; an empty-string default is a non-NULL value.
// An absent column reads as no default. The caller checks that the table
// exists first: SHOW COLUMNS on a missing table fails, and a failed statement
// pins the pooled Dolt session to its old catalog (see sentinelColumnExists).
func showColumnDefault(ctx context.Context, db DBConn, table, column string) (sql.NullString, error) {
	//nolint:gosec // G201: table and column come from a frozen, embedded migration body.
	rows, err := db.QueryContext(ctx, "SHOW COLUMNS FROM "+table+" LIKE '"+column+"'")
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return sql.NullString{}, nil
		}
		return sql.NullString{}, fmt.Errorf("reading %s.%s default: %w", table, column, err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return sql.NullString{}, fmt.Errorf("reading %s.%s default: %w", table, column, err)
	}
	defaultAt := -1
	for i, name := range cols {
		if strings.EqualFold(name, "Default") {
			defaultAt = i
		}
	}
	if defaultAt < 0 {
		return sql.NullString{}, fmt.Errorf("reading %s.%s default: SHOW COLUMNS returned no Default column (%v)", table, column, cols)
	}
	cells := make([]sql.NullString, len(cols))
	dest := make([]any, len(cols))
	for i := range cells {
		dest[i] = &cells[i]
	}
	var found sql.NullString
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return sql.NullString{}, fmt.Errorf("reading %s.%s default: %w", table, column, err)
		}
		// LIKE matches the name as a pattern (an underscore matches any
		// character); take the row whose Field is the column itself.
		if strings.EqualFold(cells[0].String, column) {
			found = cells[defaultAt]
		}
	}
	if err := rows.Err(); err != nil {
		return sql.NullString{}, fmt.Errorf("reading %s.%s default: %w", table, column, err)
	}
	return found, nil
}
