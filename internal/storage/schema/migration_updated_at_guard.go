package schema

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// updated_at preservation across value-rewriting migrations
// (gastownhall/beads#5981).
//
// issues.updated_at and wisps.updated_at carry ON UPDATE CURRENT_TIMESTAMP
// (0001_create_issues, 0020_create_wisps), so a migration that rewrites a row
// without assigning the column — 0046/0047's is_blocked recompute, 0035/0053's
// ephemeral flips while moving rows between the tables, ignored/0007's wisp
// recompute — silently re-stamps every row it changes with the migration's
// wall clock, destroying real last-modified dates. The shipped SQL is frozen
// (migration_freeze_test.go pins its hashes), so the repair lives here, in the
// runner: for every registered step the guard snapshots (id, updated_at)
// before the body runs and restores the historical timestamps after it,
// before the cursor row and the step's atomic commit record the version —
// otherwise a pass killed right after the step would keep the stamped values
// forever.
//
// Restores are compare-and-swap writes (`WHERE id = ? AND updated_at = ?`
// against the value just read back), so a row whose timestamp changed between
// the reread and the restore is left alone, and rows the body left untouched
// are not rewritten. Rows that did not exist before the step (new ids) and ids
// the step deleted are skipped: the guard restores history, it does not
// reverse the migration's own work.

// updatedAtGuardPlan names the tables one registered migration step may
// rewrite without assigning updated_at. SnapshotTables are read before the
// step, in order; RestoreTable is the one table whose rows are restored after
// the body. A row that already existed in the restore table keeps ITS
// pre-step timestamp even when the step moved a same-id row into it
// (destination preference, the INSERT IGNORE collision shape of 0035/0053);
// the moved row's source-table value is the fallback.
type updatedAtGuardPlan struct {
	SnapshotTables []string
	RestoreTable   string
}

// updatedAtGuards is keyed like preMigrationRepairs: by migration source and
// pending version. A version absent here runs with zero guard queries.
var updatedAtGuards = map[repairKey]updatedAtGuardPlan{
	// 0035 moves agent/rig/role/message issues into wisps (INSERT IGNORE
	// carries updated_at) and then flips `ephemeral` on the moved rows, which
	// re-stamps them; a pre-existing same-id wisp keeps its own timestamp.
	{"schema_migrations", 35}: {SnapshotTables: []string{"issues", "wisps"}, RestoreTable: "wisps"},
	{"schema_migrations", 46}: {SnapshotTables: []string{"issues"}, RestoreTable: "issues"},
	{"schema_migrations", 47}: {SnapshotTables: []string{"issues"}, RestoreTable: "issues"},
	// 0053 is 0035's inverse: rig wisps move into issues, then `ephemeral`
	// flips on the moved rows.
	{"schema_migrations", 53}: {SnapshotTables: []string{"issues", "wisps"}, RestoreTable: "issues"},
}

// updatedAtGuardTableExists is the guard's table-existence probe, indirected
// so sqlmock fakes that drive runMigrations across a registered version
// without a real engine can stub it. Production uses schemaTableExists.
var updatedAtGuardTableExists = schemaTableExists

// updatedAtRestoreBatchRows bounds how many rows one restore statement
// rewrites. A var (not a const) so tests can exercise the chunk boundary.
var updatedAtRestoreBatchRows = 500

// updatedAtGuard carries one step's pre-body timestamp snapshots.
type updatedAtGuard struct {
	plan updatedAtGuardPlan
	// snapshots maps table -> id -> the updated_at literal read before the
	// body; tables that did not exist are absent.
	snapshots map[string]map[string]string
}

// snapshotUpdatedAtGuard reads the guard's snapshots for the pending step, or
// returns nil (zero queries, zero writes) when the version is not registered.
func snapshotUpdatedAtGuard(ctx context.Context, db DBConn, src migrationSource, version int) (*updatedAtGuard, error) {
	plan, ok := updatedAtGuards[repairKey{src.cursorTable, version}]
	if !ok {
		return nil, nil
	}
	g := &updatedAtGuard{plan: plan, snapshots: make(map[string]map[string]string, len(plan.SnapshotTables))}
	for _, table := range plan.SnapshotTables {
		exists, err := updatedAtGuardTableExists(ctx, db, table)
		if err != nil {
			return nil, fmt.Errorf("updated_at guard: checking table %s: %w", table, err)
		}
		if !exists {
			continue
		}
		timestamps, err := readUpdatedAtByID(ctx, db, table)
		if err != nil {
			return nil, fmt.Errorf("updated_at guard: snapshotting %s: %w", table, err)
		}
		g.snapshots[table] = timestamps
	}
	return g, nil
}

// restore rewrites the restore table's rows that the step's body re-stamped,
// skipping rows whose timestamp the body left alone and rows without a
// pre-step snapshot (created by the step). The whole table is reread first so
// the compare-and-swap predicate can pin the value each row currently holds.
func (g *updatedAtGuard) restore(ctx context.Context, db DBConn) error {
	table := g.plan.RestoreTable
	exists, err := updatedAtGuardTableExists(ctx, db, table)
	if err != nil {
		return fmt.Errorf("updated_at guard: checking table %s: %w", table, err)
	}
	if !exists {
		return nil
	}
	current, err := readUpdatedAtByID(ctx, db, table)
	if err != nil {
		return fmt.Errorf("updated_at guard: rereading %s: %w", table, err)
	}
	var rows []updatedAtRowRestore
	for id, now := range current {
		want, ok := g.historical(id)
		if !ok || want == now {
			continue
		}
		rows = append(rows, updatedAtRowRestore{id: id, want: want, has: now})
	}
	if len(rows) == 0 {
		return nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	for start := 0; start < len(rows); start += updatedAtRestoreBatchRows {
		end := start + updatedAtRestoreBatchRows
		if end > len(rows) {
			end = len(rows)
		}
		if err := g.restoreBatch(ctx, db, table, rows[start:end]); err != nil {
			return fmt.Errorf("updated_at guard: restoring %s: %w", table, err)
		}
	}
	return nil
}

// updatedAtRowRestore is one row the restore rewrites: the id, its pre-step
// timestamp (want), and the value the post-body reread found (has, the
// compare-and-swap pin).
type updatedAtRowRestore struct {
	id, want, has string
}

// historical reports id's pre-step timestamp: its own value in the restore
// table when it existed there, else its value in the first snapshot table
// that had it.
func (g *updatedAtGuard) historical(id string) (string, bool) {
	if ts, ok := g.snapshots[g.plan.RestoreTable][id]; ok {
		return ts, true
	}
	for _, table := range g.plan.SnapshotTables {
		if table == g.plan.RestoreTable {
			continue
		}
		if ts, ok := g.snapshots[table][id]; ok {
			return ts, true
		}
	}
	return "", false
}

// restoreBatch rewrites one bounded slice of rows in a single statement:
// a CASE id expression assigns each row its historical value, and each row's
// predicate pins the value just reread so a row changed in between is left
// untouched instead of clobbered.
func (g *updatedAtGuard) restoreBatch(ctx context.Context, db DBConn, table string, rows []updatedAtRowRestore) error {
	var stmt strings.Builder
	var args []any
	stmt.WriteString("UPDATE `" + table + "` SET updated_at = CASE id ")
	for _, r := range rows {
		stmt.WriteString("WHEN ? THEN ? ")
		args = append(args, r.id, r.want)
	}
	stmt.WriteString("ELSE updated_at END WHERE ")
	for i, r := range rows {
		if i > 0 {
			stmt.WriteString(" OR ")
		}
		stmt.WriteString("(id = ? AND updated_at = ?)")
		args = append(args, r.id, r.has)
	}
	_, err := db.ExecContext(ctx, stmt.String(), args...)
	return err
}

// readUpdatedAtByID reads (id, updated_at) from table, normalized to the SQL
// literal form restore passes back. The scan stays representation-agnostic:
// drivers hand DATETIME columns back as time.Time or raw bytes depending on
// configuration, and the assertion that matters is SQL equality, not Go type.
func readUpdatedAtByID(ctx context.Context, db DBConn, table string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, updated_at FROM `"+table+"` ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var id string
		var updated any
		if err := rows.Scan(&id, &updated); err != nil {
			return nil, fmt.Errorf("scanning %s row: %w", table, err)
		}
		literal, has, err := updatedAtLiteral(updated)
		if err != nil {
			return nil, fmt.Errorf("reading %s row: %w", table, err)
		}
		if !has {
			continue // NULL updated_at: no history to preserve
		}
		out[id] = literal
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// updatedAtLiteral renders a driver-side updated_at value as the SQL literal
// the guard writes back; has=false means NULL. Fractional seconds are
// preserved where the column carries them (Format drops trailing zero
// fractions). Any other representation is an error, not a silent skip: a
// driver change here would otherwise quietly disable the guard.
func updatedAtLiteral(v any) (literal string, has bool, err error) {
	switch t := v.(type) {
	case nil:
		return "", false, nil
	case string:
		return t, true, nil
	case []byte:
		return string(t), true, nil
	case time.Time:
		return t.Format("2006-01-02 15:04:05.999999"), true, nil
	default:
		return "", false, fmt.Errorf("updated_at guard: unsupported value type %T", v)
	}
}
