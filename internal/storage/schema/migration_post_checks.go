package schema

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
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

// verifyDroppedDefaults checks, after a migration body returned and before
// runMigrations records the migration's cursor row, that every column default
// the body drops is gone (GH#5269).
//
// The cursor row is the only record that a migration applied. Once it is
// written, every later pass trusts it and skips the body. So a body that
// returned no error but left a guarded DROP DEFAULT undone turns into a store
// that claims the version while still minting ids from the dropped default, and
// re-running `bd migrate` cannot repair it. A store was reported carrying
// v50-v53 marks with the defaults 0050 and 0051 drop still in place. This
// refuses to record the mark instead: the pass fails loudly, nothing claims the
// version, and the next pass re-runs the body, whose guard makes it safe to
// repeat.
//
// It runs only while a migration is being applied, never on the steady-state
// open path. A table the store does not have is skipped, as the body's own
// guard skips it.
func verifyDroppedDefaults(ctx context.Context, db DBConn, sqlText string) error {
	for _, d := range droppedDefaults(sqlText) {
		present, err := showTableExists(ctx, db, d.table)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		def, err := showColumnDefault(ctx, db, d.table, d.column)
		if err != nil {
			return err
		}
		if def.Valid {
			return fmt.Errorf("%s.%s still has DEFAULT %s, which this migration drops, although its SQL returned no error; "+
				"not recording the migration (GH#5269). Run `ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT` and retry",
				d.table, d.column, def.String, d.table, d.column)
		}
	}
	return nil
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
