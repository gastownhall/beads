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
// Only the body's own rewrites are undone. A row whose updated_at changed
// while its flag shows the body left it alone was edited by another writer
// during the step, and keeps that edit's time. A row the body rewrote that
// another writer also edited before the post-body read gets its pre-step time
// back; the restore's compare-and-swap leaves an edit after that read alone.
//
// New migrations must not join this registry: they assign
// `updated_at = updated_at` in every UPDATE of an auto-stamped table, as 0059
// does. TestMigrationUpdatesOfAutoStampedTablesKeepUpdatedAt enforces both.

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
}

// updatedAtGuards is the frozen-exception registry, keyed like
// preMigrationRepairs.
var updatedAtGuards = map[repairKey]updatedAtGuardSpec{
	// 0035 copies agent/rig/role/message issues into wisps, then sets
	// wisps.ephemeral = 1 on them.
	{"schema_migrations", 35}: {restore: []string{"wisps"}, flag: "ephemeral", moveSources: map[string]string{"wisps": "issues"}},
	// 0046 and 0047 clear and recompute issues.is_blocked.
	{"schema_migrations", 46}: {restore: []string{"issues"}, flag: "is_blocked", recompute: true},
	{"schema_migrations", 47}: {restore: []string{"issues"}, flag: "is_blocked", recompute: true},
	// 0053 copies rig wisps back into issues, then sets issues.ephemeral = 0.
	{"schema_migrations", 53}: {restore: []string{"issues"}, flag: "ephemeral", moveSources: map[string]string{"issues": "wisps"}},
	// Ignored 0007 clears and recomputes wisps.is_blocked.
	{"ignored_schema_migrations", 7}: {restore: []string{"wisps"}, flag: "is_blocked", recompute: true},
}

// snapshotTables returns the restore targets plus their move sources, sorted.
func (s updatedAtGuardSpec) snapshotTables() []string {
	seen := map[string]struct{}{}
	for _, table := range s.restore {
		seen[table] = struct{}{}
	}
	for _, table := range s.moveSources {
		seen[table] = struct{}{}
	}
	tables := make([]string, 0, len(seen))
	for table := range seen {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}

// flagFor returns the flag column read with table's rows: the step's flag on
// a restore target, none on a move source.
func (s updatedAtGuardSpec) flagFor(table string) string {
	if slices.Contains(s.restore, table) {
		return s.flag
	}
	return ""
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
	snap := &updatedAtSnapshot{spec: spec, rows: map[string]map[string]updatedAtRow{}}
	for _, table := range spec.snapshotTables() {
		rows, present, err := readGuardedRows(ctx, db, table, spec.flagFor(table))
		if err != nil {
			return nil, err
		}
		if !present {
			continue
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
// its restore targets (see stampedRows). A nil snapshot (unregistered step)
// is a no-op.
func (s *updatedAtSnapshot) restore(ctx context.Context, db DBConn) error {
	if s == nil {
		return nil
	}
	for _, table := range s.spec.restore {
		current, present, err := readGuardedRows(ctx, db, table, s.spec.flag)
		if err != nil {
			return err
		}
		if !present {
			continue
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
		want, ok := s.wantUpdatedAt(own, moved, r)
		if ok && !sameUpdatedAt(want, r.updatedAt) {
			changed = append(changed, updatedAtChange{id: r.id, want: want, got: r.updatedAt})
		}
	}
	return changed
}

// wantUpdatedAt returns the updated_at row r, read after the step's body,
// must hold, or false when the guard leaves r alone. A row its table held
// before the step counts only when the body rewrote it (its flag says so) and
// keeps its own pre-step value. A row the step moved in from a registered
// source is the body's own and takes the source's value (so an INSERT IGNORE
// collision keeps the destination's own time). Rows absent from both
// snapshots are new and left alone.
func (s *updatedAtSnapshot) wantUpdatedAt(own, moved map[string]updatedAtRow, r updatedAtRow) (any, bool) {
	if before, ok := own[r.id]; ok {
		return before.updatedAt, s.spec.rewrote(before.flag, r.flag)
	}
	before, ok := moved[r.id]
	return before.updatedAt, ok
}

type updatedAtRow struct {
	id        string
	updatedAt any
	// flag is the step's flag column; NULL when the column was not read.
	flag sql.NullInt64
}

type updatedAtChange struct {
	id        string
	want, got any
}

// readGuardedRows reads every row of table in id order: its id, its
// updated_at and, when flag is named and the column exists, flag. present is
// false, and nothing is read, when table has no updated_at column.
func readGuardedRows(ctx context.Context, db DBConn, table, flag string) (rows []updatedAtRow, present bool, err error) {
	present, err = schemaColumnExists(ctx, db, table, "updated_at")
	if err != nil {
		return nil, false, fmt.Errorf("probing %s.updated_at: %w", table, err)
	}
	if !present {
		return nil, false, nil
	}
	if flag != "" {
		hasFlag, err := schemaColumnExists(ctx, db, table, flag)
		if err != nil {
			return nil, false, fmt.Errorf("probing %s.%s: %w", table, flag, err)
		}
		if !hasFlag {
			flag = ""
		}
	}
	rows, err = readUpdatedAt(ctx, db, table, flag)
	return rows, true, err
}

// readUpdatedAt reads every (id, updated_at[, flag]) of table in id order.
// The cursor is drained and closed before returning, so the caller can issue
// further statements on the same pinned connection.
func readUpdatedAt(ctx context.Context, db DBConn, table, flag string) ([]updatedAtRow, error) {
	cols := "id, updated_at"
	if flag != "" {
		cols += ", " + flag
	}
	// table and flag come from the fixed updatedAtGuards registry, never input.
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
