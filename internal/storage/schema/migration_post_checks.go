package schema

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// postConditionNoticeOut is where a dropped-default correction or repair says
// what it changed. Like lostTableNoticeOut it is deliberately NOT
// terminal-gated: a correction firing is the only field evidence of the
// GH#5269 trigger, and bd serve, systemd, CI and agent shells are all non-tty,
// so the gated stderr would discard it exactly where it happens. Overridable
// in tests.
var postConditionNoticeOut io.Writer = os.Stderr

// droppedDefaultRe finds the column defaults a migration body drops. The
// bodies that drop one (main 0043, 0050 and 0051, ignored 0010) do it inside a
// guard: SET @sql = IF(<the default is still there>, 'ALTER TABLE t ALTER
// COLUMN c DROP DEFAULT', 'SELECT 1'); PREPARE stmt FROM @sql; EXECUTE stmt.
// The statement sits inside a string literal, so droppedDefaults strips
// comments but keeps literals before matching.
var droppedDefaultRe = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+` + "`?" + `(\w+)` + "`?" +
	`\s+ALTER\s+COLUMN\s+` + "`?" + `(\w+)` + "`?" + `\s+DROP\s+DEFAULT`)

type droppedDefault struct {
	table  string
	column string
}

func (d droppedDefault) String() string { return d.table + "." + d.column }

// droppedDefaults lists the (table, column) defaults sqlText drops, in order of
// first appearance. Comments are ignored, so prose that quotes the statement
// creates no obligation; string literals are kept, because the guarded form
// carries the statement in one.
func droppedDefaults(sqlText string) []droppedDefault {
	var out []droppedDefault
	seen := make(map[droppedDefault]bool)
	for _, m := range droppedDefaultRe.FindAllStringSubmatch(stripSQLComments(sqlText), -1) {
		d := droppedDefault{table: strings.ToLower(m[1]), column: strings.ToLower(m[2])}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// stripSQLComments removes `-- …`, `# …` and `/* … */` comments from sqlText,
// leaving quoted strings and identifiers (”, "", “) byte-for-byte intact,
// including any comment markers inside them. A removed comment becomes one
// space (a line comment keeps its newline) so adjacent tokens stay apart.
func stripSQLComments(sqlText string) string {
	var b strings.Builder
	b.Grow(len(sqlText))
	for i := 0; i < len(sqlText); {
		c := sqlText[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(sqlText) {
				if sqlText[j] == '\\' && c != '`' && j+1 < len(sqlText) {
					j += 2
					continue
				}
				if sqlText[j] == c {
					if j+1 < len(sqlText) && sqlText[j+1] == c { // doubled quote
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			b.WriteString(sqlText[i:j])
			i = j
		case c == '#' || (c == '-' && strings.HasPrefix(sqlText[i:], "--")):
			j := strings.IndexByte(sqlText[i:], '\n')
			if j < 0 {
				return b.String()
			}
			b.WriteByte(' ')
			i += j // keep the newline
		case c == '/' && strings.HasPrefix(sqlText[i:], "/*"):
			j := strings.Index(sqlText[i+2:], "*/")
			if j < 0 {
				return b.String()
			}
			b.WriteByte(' ')
			i += 2 + j + 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// droppedDefaultState is what one probe of a dropped-default condition saw.
type droppedDefaultState struct {
	// present is false when the table or the column does not exist: the
	// condition does not apply, as the body's own guard skips it.
	present bool
	// def is the column's Default. Valid means a default is still there.
	def sql.NullString
}

func (s droppedDefaultState) violated() bool { return s.present && s.def.Valid }

// probeDroppedDefault reads whether d's table and column exist and, if so,
// the column's default. The table's existence is read first: SHOW COLUMNS on a
// missing table fails, and a failed statement pins the pooled Dolt session to
// its old catalog (see sentinelColumnExists).
func probeDroppedDefault(ctx context.Context, db DBConn, d droppedDefault) (droppedDefaultState, error) {
	present, err := showTableExists(ctx, db, d.table)
	if err != nil {
		return droppedDefaultState{}, err
	}
	if !present {
		return droppedDefaultState{}, nil
	}
	return showColumnDefault(ctx, db, d.table, d.column)
}

// droppedDefaultFailure is one condition that could not be confirmed met.
type droppedDefaultFailure struct {
	d        droppedDefault
	observed sql.NullString // the default last seen, if any
	cause    string
}

func (f droppedDefaultFailure) String() string {
	if f.observed.Valid {
		return fmt.Sprintf("%s (DEFAULT %s): %s", f.d, f.observed.String, f.cause)
	}
	return fmt.Sprintf("%s: %s", f.d, f.cause)
}

// droppedDefaultCorrection is one default that was found in place, dropped
// directly, and verified gone.
type droppedDefaultCorrection struct {
	d       droppedDefault
	removed string // the default that was dropped
}

// dropDefaultSQL is the direct correction for d, identifiers quoted.
func dropDefaultSQL(d droppedDefault) string {
	return "ALTER TABLE " + quoteSchemaIdent(d.table) + " ALTER COLUMN " + quoteSchemaIdent(d.column) + " DROP DEFAULT"
}

func quoteSchemaIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// correctDroppedDefaults makes every condition in ds hold: a column whose
// default is still there gets one direct, unguarded DROP DEFAULT and is probed
// again. It does not stop at the first problem, so the caller can report every
// discoverable violation at once. A condition whose table or column is absent
// does not apply and is skipped. Nothing here retries: one statement per
// violation, then a re-read.
func correctDroppedDefaults(ctx context.Context, db DBConn, ds []droppedDefault) ([]droppedDefaultCorrection, []droppedDefaultFailure) {
	var corrected []droppedDefaultCorrection
	var failures []droppedDefaultFailure
	for _, d := range ds {
		state, err := probeDroppedDefault(ctx, db, d)
		if err != nil {
			failures = append(failures, droppedDefaultFailure{d: d, cause: fmt.Sprintf("probe failed: %v", err)})
			continue
		}
		if !state.violated() {
			continue
		}
		c, f := dropDefaultAndVerify(ctx, db, d, state.def)
		if f != nil {
			failures = append(failures, *f)
			continue
		}
		corrected = append(corrected, c)
	}
	return corrected, failures
}

// dropDefaultAndVerify runs d's direct correction on a column observed with
// default observed, then reads the column again.
func dropDefaultAndVerify(ctx context.Context, db DBConn, d droppedDefault, observed sql.NullString) (droppedDefaultCorrection, *droppedDefaultFailure) {
	stmt := dropDefaultSQL(d)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return droppedDefaultCorrection{}, &droppedDefaultFailure{d: d, observed: observed, cause: fmt.Sprintf("%s failed: %v", stmt, err)}
	}
	after, err := showColumnDefault(ctx, db, d.table, d.column)
	if err != nil {
		return droppedDefaultCorrection{}, &droppedDefaultFailure{d: d, observed: observed, cause: fmt.Sprintf("re-reading after %s failed: %v", stmt, err)}
	}
	if after.violated() {
		return droppedDefaultCorrection{}, &droppedDefaultFailure{d: d, observed: after.def, cause: fmt.Sprintf("default still present after %s returned no error", stmt)}
	}
	return droppedDefaultCorrection{d: d, removed: observed.String}, nil
}

// verifyDroppedDefaults runs after a migration body returned and before
// runMigrations records the migration's cursor row: every column default the
// body drops must be gone (GH#5269).
//
// The cursor row is the only record that a migration applied. Once it is
// written, every later pass trusts it and skips the body. So a body that
// returned no error but left a guarded DROP DEFAULT undone turns into a store
// that claims the version while still minting ids from the dropped default,
// and re-running `bd migrate` cannot repair it. A store was reported carrying
// v50-v53 marks with the defaults 0050 and 0051 drop still in place.
//
// So a default the body left in place is dropped here directly
// (correctDroppedDefaults) and read back. The correction is ordinary schema
// work of this step: the per-step commit picks up the table it dirtied along
// with the cursor row. If any condition cannot be confirmed met — a probe
// fails, the ALTER fails, or the default survives it — the error names every
// such column, and runMigrations returns before the cursor row and the step
// commit, so nothing claims the version. Anything else the body changed stays
// uncommitted in the working set, as after any failed step, and the next
// pass's dirty-table guard refuses until it is committed. Then (at once, when
// the body changed nothing else) the migration and this check run again.
//
// It runs only while a migration is being applied, never on the steady-state
// open path. A table the store does not have is skipped, as the body's own
// guard skips it.
func verifyDroppedDefaults(ctx context.Context, db DBConn, src migrationSource, version int, sqlText string) error {
	corrected, failures := correctDroppedDefaults(ctx, db, droppedDefaults(sqlText))
	for _, c := range corrected {
		fmt.Fprintf(postConditionNoticeOut, "migration %s v%04d: corrected %s: the migration left DEFAULT %s in place; dropped it directly (GH#5269)\n",
			src.cursorTable, version, c.d, c.removed)
	}
	if len(failures) == 0 {
		return nil
	}
	perr := &PostConditionError{Source: src.cursorTable, Version: version, Failures: make([]PostConditionFailure, len(failures))}
	for i, f := range failures {
		perr.Failures[i] = PostConditionFailure{
			Source: src.cursorTable, Version: version,
			Table: f.d.table, Column: f.d.column, Observed: f.observed, Cause: f.cause,
		}
	}
	return perr
}

// PostConditionFailure is one dropped-default post-condition a migration pass
// could not confirm met: the migration that owns it, the column, the default
// last observed there (if one was read) and why it is not met.
type PostConditionFailure struct {
	// Source is the cursor table of the migration series that owns the
	// obligation ("schema_migrations" or "ignored_schema_migrations").
	Source string
	// Version is the migration the obligation belongs to.
	Version int
	Table   string
	Column  string
	// Observed is the default the last probe read, when it read one.
	Observed sql.NullString
	// Cause is why the condition is not met: a failed probe, a failed
	// correction, or a default that survived its correction.
	Cause string
}

func (f PostConditionFailure) String() string {
	return droppedDefaultFailure{d: droppedDefault{table: f.Table, column: f.Column}, observed: f.Observed, cause: f.Cause}.String()
}

// PostConditionError is returned when a migration's dropped-default
// post-conditions could not all be confirmed met after direct correction. The
// migration's cursor row and step commit are not written: the mark is the
// only record that a migration applied, and once written every later pass
// trusts it and skips the body (GH#5269). Any other change the step made
// stays uncommitted in the working set, so the next pass first meets the
// dirty-table guard (*DirtyTablesError) for it.
type PostConditionError struct {
	Source   string
	Version  int
	Failures []PostConditionFailure
}

// postConditionErrorRemedy is what PostConditionError tells the operator to
// do, in order, naming only steps that work in each mode. A re-run comes
// first, because a transient cause (a lock wait, a dropped connection) clears
// by itself. A manual drop is offered for server mode only: there any SQL
// client reaches the database, but it dirties the table, so it must be
// committed before the re-run, or the dirty-table guard refuses the pass. In
// embedded mode bd's only SQL surface, `bd sql`, opens through the same
// failing migration, so the last step is the report.
const postConditionErrorRemedy = "Re-run `bd migrate schema` first: a transient cause clears by itself. " +
	"If it persists in server mode, drop each default over SQL (ALTER TABLE <table> ALTER COLUMN <column> DROP DEFAULT), " +
	"commit it (CALL DOLT_COMMIT('-Am', 'drop leftover default')), then re-run `bd migrate schema`. " +
	"Otherwise (embedded mode, or the drop does not take), report this error with your Dolt version"

func (e *PostConditionError) Error() string {
	parts := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		parts[i] = f.String()
	}
	return fmt.Sprintf("dropped-default post-conditions of %s v%04d unmet, not recording the migration (GH#5269): %s. %s",
		e.Source, e.Version, strings.Join(parts, "; "), postConditionErrorRemedy)
}

// showColumnDefault reads a column's Default from SHOW COLUMNS. It is SQL NULL
// when the column has no default; an empty-string default is a non-NULL value.
// An absent column reads as not present. The caller checks that the table
// exists first: SHOW COLUMNS on a missing table fails, and a failed statement
// pins the pooled Dolt session to its old catalog (see sentinelColumnExists).
func showColumnDefault(ctx context.Context, db DBConn, table, column string) (droppedDefaultState, error) {
	//nolint:gosec // G201: table and column come from a frozen, embedded migration body.
	rows, err := db.QueryContext(ctx, "SHOW COLUMNS FROM "+table+" LIKE '"+column+"'")
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return droppedDefaultState{}, nil
		}
		return droppedDefaultState{}, fmt.Errorf("reading %s.%s default: %w", table, column, err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return droppedDefaultState{}, fmt.Errorf("reading %s.%s default: %w", table, column, err)
	}
	defaultAt := -1
	for i, name := range cols {
		if strings.EqualFold(name, "Default") {
			defaultAt = i
		}
	}
	if defaultAt < 0 {
		return droppedDefaultState{}, fmt.Errorf("reading %s.%s default: SHOW COLUMNS returned no Default column (%v)", table, column, cols)
	}
	cells := make([]sql.NullString, len(cols))
	dest := make([]any, len(cols))
	for i := range cells {
		dest[i] = &cells[i]
	}
	var found droppedDefaultState
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return droppedDefaultState{}, fmt.Errorf("reading %s.%s default: %w", table, column, err)
		}
		// LIKE matches the name as a pattern (an underscore matches any
		// character); take the row whose Field is the column itself.
		if strings.EqualFold(cells[0].String, column) {
			found = droppedDefaultState{present: true, def: cells[defaultAt]}
		}
	}
	if err := rows.Err(); err != nil {
		return droppedDefaultState{}, fmt.Errorf("reading %s.%s default: %w", table, column, err)
	}
	return found, nil
}
