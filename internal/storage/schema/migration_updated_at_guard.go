package schema

import (
	"context"
	"fmt"
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
// timestamps around each such step instead: it snapshots (id, updated_at)
// before the step's pre-repair and body run, and puts the old values back
// after the body succeeds, before the step's cursor row and commit.
//
// New migrations must not join this registry: they assign
// `updated_at = updated_at` in every UPDATE of an auto-stamped table, as 0059
// does. TestMigrationUpdatesOfAutoStampedTablesKeepUpdatedAt enforces both.

// updatedAtGuardSpec names the tables one registered step can stamp.
type updatedAtGuardSpec struct {
	// restore lists the tables whose rows the step rewrites without
	// assigning updated_at.
	restore []string
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
	{"schema_migrations", 35}: {restore: []string{"wisps"}, moveSources: map[string]string{"wisps": "issues"}},
	// 0046 and 0047 clear and recompute issues.is_blocked.
	{"schema_migrations", 46}: {restore: []string{"issues"}},
	{"schema_migrations", 47}: {restore: []string{"issues"}},
	// 0053 copies rig wisps back into issues, then sets issues.ephemeral = 0.
	{"schema_migrations", 53}: {restore: []string{"issues"}, moveSources: map[string]string{"issues": "wisps"}},
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

// updatedAtSnapshot holds one registered step's pre-step timestamps.
type updatedAtSnapshot struct {
	spec updatedAtGuardSpec
	// stamps maps table -> id -> updated_at. A table with no updated_at
	// column at snapshot time (absent clone-local wisps) has no entry.
	stamps map[string]map[string]any
}

// snapshotUpdatedAtForStep is the runner's entry point, a variable so runner
// fakes that cannot answer the guard's queries can stub it, following the
// issueRowCounter seam. It returns nil, without querying, for a step outside
// the registry.
var snapshotUpdatedAtForStep = snapshotUpdatedAt

func snapshotUpdatedAt(ctx context.Context, db DBConn, src migrationSource, version int) (*updatedAtSnapshot, error) {
	spec, ok := updatedAtGuards[repairKey{src.cursorTable, version}]
	if !ok {
		return nil, nil
	}
	snap := &updatedAtSnapshot{spec: spec, stamps: map[string]map[string]any{}}
	for _, table := range spec.snapshotTables() {
		present, err := schemaColumnExists(ctx, db, table, "updated_at")
		if err != nil {
			return nil, fmt.Errorf("probing %s.updated_at: %w", table, err)
		}
		if !present {
			continue
		}
		rows, err := readUpdatedAt(ctx, db, table)
		if err != nil {
			return nil, err
		}
		stamps := make(map[string]any, len(rows))
		for _, r := range rows {
			stamps[r.id] = r.updatedAt
		}
		snap.stamps[table] = stamps
	}
	return snap, nil
}

// restoreBatchSize bounds the rows one restoring UPDATE carries.
const restoreBatchSize = 500

// restore puts back every snapshotted timestamp the step changed on its
// restore targets. A row keeps its own pre-step value when it existed before
// the step; a row the step moved in from a registered source takes the
// source's value (so an INSERT IGNORE collision keeps the destination's own
// time). Rows absent from both snapshots are new and left alone. A nil
// snapshot (unregistered step) is a no-op.
func (s *updatedAtSnapshot) restore(ctx context.Context, db DBConn) error {
	if s == nil {
		return nil
	}
	for _, table := range s.spec.restore {
		present, err := schemaColumnExists(ctx, db, table, "updated_at")
		if err != nil {
			return fmt.Errorf("probing %s.updated_at: %w", table, err)
		}
		if !present {
			continue
		}
		current, err := readUpdatedAt(ctx, db, table)
		if err != nil {
			return err
		}
		own := s.stamps[table]
		var moved map[string]any
		if source, ok := s.spec.moveSources[table]; ok {
			moved = s.stamps[source]
		}
		var changed []updatedAtChange
		for _, r := range current {
			want, ok := own[r.id]
			if !ok {
				if want, ok = moved[r.id]; !ok {
					continue
				}
			}
			if sameUpdatedAt(want, r.updatedAt) {
				continue
			}
			changed = append(changed, updatedAtChange{id: r.id, want: want, got: r.updatedAt})
		}
		for start := 0; start < len(changed); start += restoreBatchSize {
			end := min(start+restoreBatchSize, len(changed))
			if err := restoreUpdatedAtBatch(ctx, db, table, changed[start:end]); err != nil {
				return fmt.Errorf("restoring %s.updated_at: %w", table, err)
			}
		}
	}
	return nil
}

type updatedAtRow struct {
	id        string
	updatedAt any
}

type updatedAtChange struct {
	id        string
	want, got any
}

// readUpdatedAt reads every (id, updated_at) of table in id order. The cursor
// is drained and closed before returning, so the caller can issue further
// statements on the same pinned connection.
func readUpdatedAt(ctx context.Context, db DBConn, table string) ([]updatedAtRow, error) {
	// table comes from the fixed updatedAtGuards registry, never input.
	rows, err := db.QueryContext(ctx, "SELECT id, updated_at FROM "+table+" ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("reading %s.updated_at: %w", table, err)
	}
	defer rows.Close()
	var out []updatedAtRow
	for rows.Next() {
		var id string
		var raw any
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("reading %s.updated_at: %w", table, err)
		}
		out = append(out, updatedAtRow{id: id, updatedAt: ownUpdatedAt(raw)})
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
// matches each row's id AND the value just read, so a row whose timestamp
// moved again after the read is left alone.
func restoreUpdatedAtBatch(ctx context.Context, db DBConn, table string, batch []updatedAtChange) error {
	var cases, where strings.Builder
	setArgs := make([]any, 0, 2*len(batch))
	whereArgs := make([]any, 0, 2*len(batch))
	for i, c := range batch {
		cases.WriteString(" WHEN ? THEN ?")
		setArgs = append(setArgs, c.id, c.want)
		if i > 0 {
			where.WriteString(" OR ")
		}
		where.WriteString("(id = ? AND updated_at = ?)")
		whereArgs = append(whereArgs, c.id, c.got)
	}
	query := "UPDATE " + table + " SET updated_at = CASE id" + cases.String() + " ELSE updated_at END WHERE " + where.String()
	_, err := db.ExecContext(ctx, query, append(setArgs, whereArgs...)...)
	return err
}
