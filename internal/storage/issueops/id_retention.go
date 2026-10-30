package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	publicops "github.com/steveyegge/beads/issueops"
)

// IDTombstoneTable and WispIDTombstoneTable hold the ids a retaining retention
// sweep retired (publicops.SweepRequest.RetainIDs), one table per storage
// plane so a tombstone is exactly as durable as the row it replaces:
// id_tombstones is versioned like issues, wisp_id_tombstones is clone-local
// like wisps (the wisp_% dolt_ignore pattern covers it). Each row is the id
// and the swept row's closed_at, nothing else (migration 0074).
const (
	IDTombstoneTable     = "id_tombstones"
	WispIDTombstoneTable = "wisp_id_tombstones"
)

// IDTombstoneTables names both tombstone tables, versioned first. A Dolt
// commit that stages after a retaining sweep stages these; staging the
// clone-local one is a no-op.
func IDTombstoneTables() []string {
	return []string{IDTombstoneTable, WispIDTombstoneTable}
}

// RetiredID is one id to retire: the id of a row a retaining sweep deleted and
// the closed_at that row carried.
type RetiredID struct {
	ID       string
	ClosedAt time.Time
}

// idOccupancySQL reads, in ONE statement, every place an id can be held: the
// two row tables and the two tombstone tables. One statement is one snapshot
// on every backend and at every isolation level — under READ COMMITTED two
// separate reads could see a sweep land between them and answer "neither".
const idOccupancySQL = `SELECT 'issues' AS src, closed_at FROM issues WHERE id = ?
UNION ALL SELECT 'wisps', closed_at FROM wisps WHERE id = ?
UNION ALL SELECT 'id_tombstones', closed_at FROM id_tombstones WHERE id = ?
UNION ALL SELECT 'wisp_id_tombstones', closed_at FROM wisp_id_tombstones WHERE id = ?`

// probeIDOccupancyInTx reports whether a live row holds id in either plane and,
// when no live row does, whether a tombstone retired it. A live row wins over a
// tombstone: retired is nil whenever live is true, so the caller answers
// ErrAlreadyExists for an id that is both (a merge brought a live row onto a
// retired id) rather than refusing a create of a row that exists.
func probeIDOccupancyInTx(ctx context.Context, tx DBTX, id string) (live bool, retired *RetiredID, err error) {
	rows, err := tx.QueryContext(ctx, idOccupancySQL, id, id, id, id)
	if err != nil {
		return false, nil, fmt.Errorf("probe id occupancy for %q: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var src string
		var closedAt sql.NullTime
		if err := rows.Scan(&src, &closedAt); err != nil {
			return false, nil, fmt.Errorf("probe id occupancy for %q: %w", id, err)
		}
		switch src {
		case "issues", "wisps":
			live = true
		default:
			if retired == nil {
				retired = &RetiredID{ID: id}
				if closedAt.Valid {
					retired.ClosedAt = closedAt.Time.UTC()
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, nil, fmt.Errorf("probe id occupancy for %q: %w", id, err)
	}
	if live {
		return true, nil, nil
	}
	return false, retired, nil
}

// IDTakenInTx reports whether a fresh create may not mint id into table
// ("issues" or "wisps"): a live row of that table holds it, or a tombstone in
// either tombstone table retired it. One statement, so the id generators pay
// no extra round trip for the tombstone check.
func IDTakenInTx(ctx context.Context, tx DBTX, table, id string) (bool, error) {
	if table != "issues" && table != "wisps" {
		return false, fmt.Errorf("id taken: unknown table %q", table)
	}
	var count int
	//nolint:gosec // G201: table is checked against two constants above; the tombstone tables are constants.
	query := fmt.Sprintf(`SELECT (SELECT COUNT(*) FROM %s WHERE id = ?) + (SELECT COUNT(*) FROM %s WHERE id = ?) + (SELECT COUNT(*) FROM %s WHERE id = ?)`,
		table, IDTombstoneTable, WispIDTombstoneTable)
	if err := tx.QueryRowContext(ctx, query, id, id, id).Scan(&count); err != nil {
		return false, fmt.Errorf("check whether %q is taken in %s: %w", id, table, err)
	}
	return count > 0, nil
}

// idRetiredSQL counts id's tombstones across both tombstone tables in one
// statement.
const idRetiredSQL = `SELECT (SELECT COUNT(*) FROM id_tombstones WHERE id = ?) + (SELECT COUNT(*) FROM wisp_id_tombstones WHERE id = ?)`

// IDRetiredInTx reports whether a tombstone in either table retired id. It is
// one statement, for the paths that ask about one id at a time (the counter
// mints); RetiredIDsInTx serves id lists.
func IDRetiredInTx(ctx context.Context, tx DBTX, id string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, idRetiredSQL, id, id).Scan(&count); err != nil {
		return false, fmt.Errorf("check whether %q is retired: %w", id, err)
	}
	return count > 0, nil
}

// idempotencyExpired is the typed refusal for a create of a retired id.
func idempotencyExpired(retired *RetiredID) error {
	return &publicops.IdempotencyExpiredError{ID: retired.ID, ClosedAt: retired.ClosedAt}
}

// RefuseRetiredIDInTx returns *publicops.IdempotencyExpiredError when id is
// retired and no live row holds it, and nil otherwise (including when a live
// row holds it: that is an upsert's business, not a refusal of this one). It
// is the guard an upsert-shaped create runs before writing an explicit id.
//
// It coordinates id's shard (CoordinateIDCreatesInTx) before it probes, so on
// a READ COMMITTED transaction a retaining sweep of id cannot commit between
// the probe and the upsert that follows it.
func RefuseRetiredIDInTx(ctx context.Context, tx DBTX, id string) error {
	if id == "" {
		return nil
	}
	if err := CoordinateIDCreatesInTx(ctx, tx, []string{id}); err != nil {
		return err
	}
	_, retired, err := probeIDOccupancyInTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if retired != nil {
		return idempotencyExpired(retired)
	}
	return nil
}

// RetiredIDsInTx returns which of ids a tombstone in either table retired,
// with the closed_at the tombstone recorded. It reads in chunks of
// queryBatchSize ids per table. It does not consult the row tables: a caller
// that needs "retired and not live" combines it with its own presence read.
func RetiredIDsInTx(ctx context.Context, tx DBTX, ids []string) (map[string]time.Time, error) {
	found := map[string]time.Time{}
	if len(ids) == 0 {
		return found, nil
	}
	for _, table := range IDTombstoneTables() {
		for start := 0; start < len(ids); start += queryBatchSize {
			chunk := ids[start:min(start+queryBatchSize, len(ids))]
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
			args := make([]any, len(chunk))
			for i, id := range chunk {
				args[i] = id
			}
			//nolint:gosec // G201: table is one of two hardcoded constants; only placeholders are formatted in.
			rows, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT id, closed_at FROM %s WHERE id IN (%s)", table, placeholders), args...)
			if err != nil {
				return nil, fmt.Errorf("read retired ids from %s: %w", table, err)
			}
			for rows.Next() {
				var id string
				var closedAt sql.NullTime
				if err := rows.Scan(&id, &closedAt); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("read retired ids from %s: %w", table, err)
				}
				if _, seen := found[id]; !seen {
					found[id] = closedAt.Time.UTC()
				}
			}
			err = rows.Err()
			_ = rows.Close()
			if err != nil {
				return nil, fmt.Errorf("read retired ids from %s: %w", table, err)
			}
		}
	}
	return found, nil
}

// RetireIDsInTx writes one tombstone per id: wisp into the clone-local
// wisp_id_tombstones, durable into the versioned id_tombstones. It must run in
// the transaction that deletes the rows, so a failure here rolls the delete
// back with it.
//
// INSERT IGNORE, so the first tombstone wins and a re-retire is a no-op; the
// rows are deterministic (id and closed_at truncated to the second), so the
// same tombstone written on two clones merges without conflict. A zero
// ClosedAt is refused: every row a sweep deletes carries one, and a tombstone
// without it would be a different row on every clone that wrote it.
func RetireIDsInTx(ctx context.Context, tx DBTX, wisp, durable []RetiredID) error {
	for _, side := range []struct {
		table string
		ids   []RetiredID
	}{{WispIDTombstoneTable, wisp}, {IDTombstoneTable, durable}} {
		for start := 0; start < len(side.ids); start += queryBatchSize {
			chunk := side.ids[start:min(start+queryBatchSize, len(side.ids))]
			values := make([]string, len(chunk))
			args := make([]any, 0, 2*len(chunk))
			for i, retired := range chunk {
				if retired.ID == "" {
					return errors.New("retire ids: empty id")
				}
				if retired.ClosedAt.IsZero() {
					return fmt.Errorf("retire ids: %s has no closed_at", retired.ID)
				}
				values[i] = "(?, ?)"
				args = append(args, retired.ID, retired.ClosedAt.UTC().Truncate(time.Second))
			}
			//nolint:gosec // G201: table is one of two hardcoded constants; only placeholders are formatted in.
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT IGNORE INTO %s (id, closed_at) VALUES %s", side.table, strings.Join(values, ", ")), args...); err != nil {
				return fmt.Errorf("retire ids into %s: %w", side.table, err)
			}
		}
	}
	return nil
}
