package schema

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Frozen migrations that rewrite issue or wisp rows without assigning
// updated_at let the column's ON UPDATE CURRENT_TIMESTAMP clause overwrite
// every touched row's last-modified time with the migration's wall clock
// (gastownhall/beads#5981: 0046/0047's is_blocked recompute stamped every
// blocked issue on a 1.0.x -> 1.2.x upgrade). Their SQL is content-hashed and
// cannot be edited (see migration_repairs.go), so the runner preserves the
// timestamps around each such step instead: it snapshots (id, updated_at,
// flag) before the step's pre-repair and body run, where flag is the one
// column the step's body rewrites, and puts the old updated_at back on the
// rows the body rewrote after the body succeeds, before the step's cursor row
// and commit.
//
// A step can also stamp a table it never assigns. go-mysql-server, unlike
// MySQL, applies the ON UPDATE CURRENT_TIMESTAMP of every table a multi-table
// UPDATE joins, so `UPDATE dependencies d JOIN issues i ON ... SET
// d.depends_on_issue_id = ...` (0041) stamps every issue the join matches and
// writes it back with nothing else changed. For such a joinStamped table the
// snapshot also fingerprints each row's other columns, and a row whose
// fingerprint is unchanged after the step but whose updated_at moved gets its
// pre-step value back.
//
// Only the body's own rewrites are undone. A row whose updated_at changed
// while its flag shows the body left it alone, and whose other columns (on a
// joinStamped table) changed too, was edited by another writer during the
// step, and keeps that edit's time. A row the body rewrote that another
// writer also edited before the post-body read gets its pre-step time back;
// the restore's compare-and-swap leaves an edit after that read alone.
//
// New migrations must not join this registry: they assign
// `updated_at = updated_at` in every UPDATE of an auto-stamped table, as 0059
// does, and `alias.updated_at = alias.updated_at` for every auto-stamped
// table an UPDATE joins. TestMigrationUpdatesOfAutoStampedTablesKeepUpdatedAt
// enforces both.

// updatedAtGuardSpec names the tables one registered step can stamp and the
// column whose rewrite stamps them.
type updatedAtGuardSpec struct {
	// restore lists the tables whose rows the step rewrites without
	// assigning updated_at.
	restore []string
	// flag is the only column the step's unguarded UPDATEs assign on its
	// restore targets, so comparing a row's flag before and after the step
	// tells whether the body rewrote that row.
	flag string
	// recompute marks a body that clears flag on every row and then sets it
	// where it applies (0046/0047, ignored 0007). Such a body rewrote every
	// row whose flag was set before the step or is set after it, including a
	// row it cleared and set again. Any other body rewrote the rows whose
	// flag changed.
	recompute bool
	// moveSources maps a restore target to the table the step copies rows
	// from (INSERT IGNORE ... SELECT, updated_at included) before rewriting
	// them. Source tables are snapshot inputs only, never restored.
	moveSources map[string]string
	// joinStamped lists the auto-stamped tables the step's multi-table
	// UPDATEs join without assigning any of their columns. The engine
	// stamps every row such a join matches (see the file comment); the
	// guard restores a row whose columns other than updated_at are
	// unchanged.
	joinStamped []string
}

// updatedAtGuards is the frozen-exception registry, keyed like
// preMigrationRepairs.
var updatedAtGuards = map[repairKey]updatedAtGuardSpec{
	// 0035 copies agent/rig/role/message issues into wisps, then sets
	// wisps.ephemeral = 1 on them.
	{"schema_migrations", 35}: {restore: []string{"wisps"}, flag: "ephemeral", moveSources: map[string]string{"wisps": "issues"}},
	// 0041 splits dependencies.depends_on_id with UPDATEs that join wisps
	// and issues, stamping every issue and wisp a dependency points at.
	{"schema_migrations", 41}: {joinStamped: []string{"issues", "wisps"}},
	// 0046 and 0047 clear and recompute issues.is_blocked. 0047 also splits
	// a legacy wisp_dependencies with UPDATEs that join wisps and issues.
	{"schema_migrations", 46}: {restore: []string{"issues"}, flag: "is_blocked", recompute: true},
	{"schema_migrations", 47}: {restore: []string{"issues"}, flag: "is_blocked", recompute: true, joinStamped: []string{"issues", "wisps"}},
	// 0053 copies rig wisps back into issues, then sets issues.ephemeral = 0.
	// Its child_counters UPDATE joins the rig wisps (which it then deletes).
	{"schema_migrations", 53}: {restore: []string{"issues"}, flag: "ephemeral", moveSources: map[string]string{"issues": "wisps"}, joinStamped: []string{"wisps"}},
	// Ignored 0003 splits wisp_dependencies.depends_on_id with UPDATEs that
	// join wisps and issues.
	{"ignored_schema_migrations", 3}: {joinStamped: []string{"issues", "wisps"}},
	// Ignored 0007 clears and recomputes wisps.is_blocked.
	{"ignored_schema_migrations", 7}: {restore: []string{"wisps"}, flag: "is_blocked", recompute: true},
	// Ignored 0011's wisp_child_counters UPDATE joins wisps.
	{"ignored_schema_migrations", 11}: {joinStamped: []string{"wisps"}},
}

// snapshotTables returns every table the step's snapshot reads: its restore
// targets, their move sources and its joinStamped tables, sorted.
func (s updatedAtGuardSpec) snapshotTables() []string {
	seen := map[string]struct{}{}
	for _, table := range s.restore {
		seen[table] = struct{}{}
	}
	for _, table := range s.moveSources {
		seen[table] = struct{}{}
	}
	for _, table := range s.joinStamped {
		seen[table] = struct{}{}
	}
	tables := make([]string, 0, len(seen))
	for table := range seen {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}

// restoredTables returns the tables the restore rewrites, in order: the
// restore targets, then the joinStamped tables that are not also targets.
func (s updatedAtGuardSpec) restoredTables() []string {
	tables := slices.Clone(s.restore)
	for _, table := range s.joinStamped {
		if !slices.Contains(tables, table) {
			tables = append(tables, table)
		}
	}
	return tables
}

// presentFlag returns the flag column to read with table's rows, given the
// table's current columns: flagFor's, or none when the table does not (yet)
// carry it, as before a step that adds the flag.
func (s updatedAtGuardSpec) presentFlag(table string, cols []string) string {
	flag := s.flagFor(table)
	if flag == "" || !hasColumnFold(cols, flag) {
		return ""
	}
	return flag
}

// flagFor returns the flag column read with table's rows: the step's flag on
// a restore target, none on any other table.
func (s updatedAtGuardSpec) flagFor(table string) string {
	if slices.Contains(s.restore, table) {
		return s.flag
	}
	return ""
}

// fingerprints reports whether table's rows are read with a fingerprint of
// their other columns, which is how the restore tells a join's stamp apart.
func (s updatedAtGuardSpec) fingerprints(table string) bool {
	return slices.Contains(s.joinStamped, table)
}

// rewrote reports whether the step's body rewrote a row, given the row's flag
// before and after the step. A flag column the step adds reads as NULL before
// the step; a recompute counts that, like the column's DEFAULT 0, as clear.
func (s updatedAtGuardSpec) rewrote(before, after sql.NullInt64) bool {
	if s.recompute {
		return before.Int64 != 0 || after.Int64 != 0
	}
	return before != after
}

// updatedAtSnapshot holds one registered step's pre-step rows.
type updatedAtSnapshot struct {
	spec updatedAtGuardSpec
	// rows maps table -> id -> the row before the step. A table with no
	// updated_at column at snapshot time (absent clone-local wisps) has no
	// entry.
	rows map[string]map[string]updatedAtRow
	// fingerprintColumns maps a joinStamped table to the columns its
	// fingerprint covered at snapshot time; the post-body read hashes the
	// same columns.
	fingerprintColumns map[string][]string
}

// snapshotUpdatedAtForStep is the runner's entry point, a variable so runner
// fakes that cannot answer the guard's queries can stub it, following the
// issueRowCounter seam. It returns nil, without querying, for a step outside
// the registry.
var snapshotUpdatedAtForStep = snapshotUpdatedAt

// SetUpdatedAtSnapshotHookForTest makes fn run right after each registered
// step's updated_at snapshot, so a test can write as another session would
// while the step runs; an error from fn stops the step. It returns a function
// that restores the previous behavior. Test-only.
func SetUpdatedAtSnapshotHookForTest(fn func(ctx context.Context, db DBConn, version int) error) func() {
	prev := snapshotUpdatedAtForStep
	snapshotUpdatedAtForStep = func(ctx context.Context, db DBConn, src migrationSource, version int) (*updatedAtSnapshot, error) {
		snap, err := prev(ctx, db, src, version)
		if err != nil || snap == nil {
			return snap, err
		}
		return snap, fn(ctx, db, version)
	}
	return func() { snapshotUpdatedAtForStep = prev }
}

func snapshotUpdatedAt(ctx context.Context, db DBConn, src migrationSource, version int) (*updatedAtSnapshot, error) {
	spec, ok := updatedAtGuards[repairKey{src.cursorTable, version}]
	if !ok {
		return nil, nil
	}
	snap := &updatedAtSnapshot{
		spec:               spec,
		rows:               map[string]map[string]updatedAtRow{},
		fingerprintColumns: map[string][]string{},
	}
	for _, table := range spec.snapshotTables() {
		cols, err := guardColumnsOf(ctx, db, table)
		if err != nil {
			return nil, err
		}
		if !hasColumnFold(cols, "updated_at") {
			continue
		}
		var fpCols []string
		if spec.fingerprints(table) {
			fpCols = fingerprintColumns(cols)
			snap.fingerprintColumns[table] = fpCols
		}
		rows, err := readUpdatedAt(ctx, db, table, spec.presentFlag(table, cols), fpCols)
		if err != nil {
			return nil, err
		}
		byID := make(map[string]updatedAtRow, len(rows))
		for _, r := range rows {
			byID[r.id] = r
		}
		snap.rows[table] = byID
	}
	return snap, nil
}

// restoreBatchSize bounds the rows one restoring UPDATE carries.
const restoreBatchSize = 500

// restore puts back every snapshotted timestamp the step's body overwrote on
// its restore targets and joinStamped tables (see stampedRows). A nil
// snapshot (unregistered step) is a no-op.
func (s *updatedAtSnapshot) restore(ctx context.Context, db DBConn) error {
	if s == nil {
		return nil
	}
	for _, table := range s.spec.restoredTables() {
		cols, err := guardColumnsOf(ctx, db, table)
		if err != nil {
			return err
		}
		if !hasColumnFold(cols, "updated_at") {
			continue
		}
		fpCols := s.postBodyFingerprintColumns(table, cols)
		current, err := readUpdatedAt(ctx, db, table, s.spec.presentFlag(table, cols), fpCols)
		if err != nil {
			return err
		}
		changed := s.stampedRows(table, current)
		for start := 0; start < len(changed); start += restoreBatchSize {
			end := min(start+restoreBatchSize, len(changed))
			if err := restoreUpdatedAtBatch(ctx, db, table, changed[start:end]); err != nil {
				return fmt.Errorf("restoring %s.updated_at: %w", table, err)
			}
		}
	}
	return nil
}

// postBodyFingerprintColumns returns the columns the post-body read of table
// fingerprints, given the table's columns as listed after the body: the
// snapshot's, when every one of them still exists. It returns nil, which
// turns the join rule off for table, when table was not fingerprinted or the
// step dropped one of those columns, since a fingerprint over different
// columns cannot match.
func (s *updatedAtSnapshot) postBodyFingerprintColumns(table string, cols []string) []string {
	want, ok := s.fingerprintColumns[table]
	if !ok {
		return nil
	}
	have := fingerprintColumns(cols)
	for _, col := range want {
		if !slices.Contains(have, col) {
			return nil
		}
	}
	return want
}

// stampedRows returns the rows of table, as read after the step's body, whose
// updated_at differs from the value wantUpdatedAt says they must keep.
func (s *updatedAtSnapshot) stampedRows(table string, current []updatedAtRow) []updatedAtChange {
	own := s.rows[table]
	var moved map[string]updatedAtRow
	if source, ok := s.spec.moveSources[table]; ok {
		moved = s.rows[source]
	}
	var changed []updatedAtChange
	for _, r := range current {
		want, ok := s.wantUpdatedAt(table, own, moved, r)
		if ok && !sameUpdatedAt(want, r.updatedAt) {
			changed = append(changed, updatedAtChange{id: r.id, want: want, got: r.updatedAt})
		}
	}
	return changed
}

// wantUpdatedAt returns the updated_at row r of table, read after the step's
// body, must hold, or false when the guard leaves r alone. A row its table
// held before the step counts only when the body rewrote it and keeps its own
// pre-step value: on a restore target its flag says so, and on a joinStamped
// table every column but updated_at reads as it did before the step. A row
// the step moved in from a registered source is the body's own and takes the
// source's value (so an INSERT IGNORE collision keeps the destination's own
// time). Rows absent from both snapshots are new and left alone.
func (s *updatedAtSnapshot) wantUpdatedAt(table string, own, moved map[string]updatedAtRow, r updatedAtRow) (any, bool) {
	if before, ok := own[r.id]; ok {
		if slices.Contains(s.spec.restore, table) && s.spec.rewrote(before.flag, r.flag) {
			return before.updatedAt, true
		}
		joinOnly := before.fingerprint.Valid && r.fingerprint.Valid && before.fingerprint == r.fingerprint
		return before.updatedAt, joinOnly && s.spec.fingerprints(table)
	}
	before, ok := moved[r.id]
	return before.updatedAt, ok
}

type updatedAtRow struct {
	id        string
	updatedAt any
	// flag is the step's flag column; NULL when the column was not read.
	flag sql.NullInt64
	// fingerprint hashes the row's columns other than updated_at; NULL when
	// the table is not joinStamped in this step.
	fingerprint sql.NullString
}

type updatedAtChange struct {
	id        string
	want, got any
}

// guardColumnsOf returns table's column names in table order, or nil when
// the session's database has no such table, in one SHOW TABLES and (for a
// present table) one SHOW COLUMNS. Every column question the guard asks
// (updated_at, the step's flag, the fingerprint columns) is answered from
// this one listing.
//
// It does not read INFORMATION_SCHEMA: Dolt does not push that predicate
// down, so each COLUMNS probe walks every database on the server, and the
// guard probes while holding the schema migration lock. Measured on a
// scratch Dolt 2.6.0 sql-server, one INFORMATION_SCHEMA.COLUMNS probe took
// 0.09 s with 1 database, 1.7 s with 150 and ~6.3 s with 300; SHOW COLUMNS
// took 0.1 s at 300 (see showColumnExists for the same finding on the
// content-hash probe).
//
// SHOW COLUMNS fails on a missing table, and a failed statement pins a
// pooled Dolt session to its catalog snapshot (be-bv7x), so the table's
// existence is read first with showTableExists, whose listing succeeds
// whether or not the table is there; a clone without wisps reads as absent
// without ever sending a failing statement. Names compare case-insensitively
// (hasColumnFold, showTableExists). table comes from the fixed
// updatedAtGuards registry, never input.
func guardColumnsOf(ctx context.Context, db DBConn, table string) ([]string, error) {
	present, err := showTableExists(ctx, db, table)
	if err != nil {
		return nil, fmt.Errorf("probing %s: %w", table, err)
	}
	if !present {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, "SHOW COLUMNS FROM "+table)
	if err != nil {
		return nil, fmt.Errorf("listing %s columns: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	names, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("listing %s columns: %w", table, err)
	}
	// SHOW COLUMNS returns Field, Type, Null, Key, Default, Extra (more on
	// some servers), one row per column in table order; Field is the name.
	cells := make([]sql.RawBytes, len(names))
	dest := make([]any, len(names))
	for i := range cells {
		dest[i] = &cells[i]
	}
	cols := []string{}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("listing %s columns: %w", table, err)
		}
		if len(cells) > 0 {
			cols = append(cols, string(cells[0]))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing %s columns: %w", table, err)
	}
	return cols, nil
}

// hasColumnFold reports whether cols names column, compared
// case-insensitively as MySQL compares column names.
func hasColumnFold(cols []string, column string) bool {
	return slices.ContainsFunc(cols, func(c string) bool { return strings.EqualFold(c, column) })
}

// fingerprintColumns returns cols without updated_at, in table order: the
// columns a joinStamped table's fingerprint covers.
func fingerprintColumns(cols []string) []string {
	out := make([]string, 0, len(cols))
	for _, col := range cols {
		if !strings.EqualFold(col, "updated_at") {
			out = append(out, col)
		}
	}
	return out
}

// readUpdatedAt reads every (id, updated_at[, flag][, fingerprint]) of table
// in id order. The cursor is drained and closed before returning, so the
// caller can issue further statements on the same pinned connection.
func readUpdatedAt(ctx context.Context, db DBConn, table, flag string, fpCols []string) ([]updatedAtRow, error) {
	cols := "id, updated_at"
	if flag != "" {
		cols += ", " + flag
	}
	if len(fpCols) > 0 {
		cols += ", " + fingerprintSQL(fpCols)
	}
	// table and flag come from the fixed updatedAtGuards registry, and
	// fpCols from SHOW COLUMNS (quoted), never input.
	rows, err := db.QueryContext(ctx, "SELECT "+cols+" FROM "+table+" ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("reading %s.updated_at: %w", table, err)
	}
	defer rows.Close()
	var out []updatedAtRow
	for rows.Next() {
		var r updatedAtRow
		var raw any
		dest := []any{&r.id, &raw}
		if flag != "" {
			dest = append(dest, &r.flag)
		}
		if len(fpCols) > 0 {
			dest = append(dest, &r.fingerprint)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading %s.updated_at: %w", table, err)
		}
		r.updatedAt = ownUpdatedAt(raw)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading %s.updated_at: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("reading %s.updated_at: %w", table, err)
	}
	return out, nil
}

// fingerprintSQL hashes cols, in order, into one value per row: the MD5 of
// each column's MD5 (or '-' for NULL, which keeps NULL apart from the empty
// string), comma-separated. Every column is cast to CHAR and hashed on its
// own because a large TEXT value reaches functions as Dolt's out-of-band
// *val.TextStorage, which JSON_ARRAY rejects ("unsupported type") on stores
// historical bd releases wrote.
func fingerprintSQL(cols []string) string {
	parts := make([]string, len(cols))
	for i, col := range cols {
		parts[i] = "IFNULL(MD5(CAST(`" + strings.ReplaceAll(col, "`", "``") + "` AS CHAR)), '-')"
	}
	return "MD5(CONCAT_WS(',', " + strings.Join(parts, ", ") + "))"
}

// ownUpdatedAt detaches a scanned value from the driver: a byte buffer may be
// reused by the next Next call, so it is copied into a string.
func ownUpdatedAt(raw any) any {
	if b, ok := raw.([]byte); ok {
		return string(b)
	}
	return raw
}

// sameUpdatedAt compares two scanned timestamps by value: time.Time by
// instant (Equal, not ==, which also compares location and monotonic
// reading), anything else (text) by equality.
func sameUpdatedAt(a, b any) bool {
	at, aTime := a.(time.Time)
	bt, bTime := b.(time.Time)
	if aTime || bTime {
		return aTime && bTime && at.Equal(bt)
	}
	return a == b
}

// restoreUpdatedAtBatch writes the wanted timestamps back. Assigning
// updated_at explicitly suppresses its ON UPDATE clause. The WHERE clause
// picks the batch's rows by an id IN list, which Dolt plans as a primary-key
// lookup (an OR of per-row conditions scans the whole table), and compares
// each row's updated_at with the value just read, so a row whose timestamp
// moved again after the read is left alone.
func restoreUpdatedAtBatch(ctx context.Context, db DBConn, table string, batch []updatedAtChange) error {
	setArgs := make([]any, 0, 2*len(batch))
	idArgs := make([]any, 0, len(batch))
	readArgs := make([]any, 0, 2*len(batch))
	for _, c := range batch {
		setArgs = append(setArgs, c.id, c.want)
		idArgs = append(idArgs, c.id)
		readArgs = append(readArgs, c.id, c.got)
	}
	cases := strings.Repeat(" WHEN ? THEN ?", len(batch))
	query := "UPDATE " + table + " SET updated_at = CASE id" + cases + " ELSE updated_at END" +
		" WHERE id IN (?" + strings.Repeat(", ?", len(batch)-1) + ")" +
		" AND updated_at = CASE id" + cases + " END"
	args := append(append(setArgs, idArgs...), readArgs...)
	_, err := db.ExecContext(ctx, query, args...)
	return err
}
