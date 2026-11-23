package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// strandedStep is one numbered migration whose SQL and cursor row reached the
// working set but whose per-step commit never ran: a pass killed (crash, kill,
// open timeout) between the cursor INSERT in runMigrations and its
// commitMigrationStep. tables are the committable dirty tables recognised as
// that step's own work.
type strandedStep struct {
	file   migrationFile
	tables []string
}

// findStrandedStep reports the stranded step when the working set is provably
// nothing but one of this series' own steps left uncommitted, and nil
// otherwise. dirty is the pass's committable dirty set (cursor tables and
// pass-owned exemptions already removed). Every condition must hold, because
// a recognised step is committed without the pre-existing-dirty-table guard,
// and that guard exists to keep a user's uncommitted rows out of a migration
// commit (#4566):
//
//   - the cursor table exists at HEAD, and its versions in the working set
//     are HEAD's versions plus exactly one;
//   - that row's version is the first migration of this series above HEAD's
//     highest version, which is the only step a per-step-committing pass can
//     leave uncommitted;
//   - its content_hash is the hash of this binary's frozen migration file,
//     the value runMigrations writes and nothing else does;
//   - every committable dirty table is one that migration's SQL touches.
//
// Any other shape returns nil, and the caller's guard refuses as it always
// has. The reads run only when the cursor table is dirty, so a clean or
// user-dirty open issues no extra statement.
func (m migrationSource) findStrandedStep(ctx context.Context, db DBConn, cursorDirty bool, dirty map[string]dirtyTableState) (*strandedStep, error) {
	if !cursorDirty {
		return nil, nil
	}
	// A cursor table that is new, deleted or renamed in the working set is not
	// a single stranded step (a fresh bootstrap has its own heal), and reading
	// it AS OF HEAD would fail and pin the session's catalog (be-bv7x).
	var notModified int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = ? AND status <> 'modified'",
		m.cursorTable).Scan(&notModified); err != nil {
		return nil, fmt.Errorf("reading %s status: %w", m.cursorTable, err)
	}
	if notModified > 0 {
		return nil, nil
	}
	hasHash, err := m.hasContentHashColumn(ctx, db)
	if err != nil {
		return nil, err
	}
	if !hasHash {
		return nil, nil
	}

	working, err := readCursorRows(ctx, db, "SELECT version, content_hash FROM "+m.cursorTable)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", m.cursorTable, err)
	}
	// HEAD is read by version only: a pass that added the content_hash column
	// (ensureContentHashColumn) and was killed inside its first step leaves a
	// HEAD cursor without that column, and naming it there would fail.
	head, err := readCursorRows(ctx, db, "SELECT version, NULL FROM "+m.cursorTable+" AS OF 'HEAD'")
	if err != nil {
		return nil, fmt.Errorf("reading %s at HEAD: %w", m.cursorTable, err)
	}
	if len(working) != len(head)+1 {
		return nil, nil
	}
	added := -1
	headMax := 0
	for version := range head {
		if _, ok := working[version]; !ok {
			return nil, nil
		}
		headMax = max(headMax, version)
	}
	for version := range working {
		if _, ok := head[version]; !ok {
			added = version
		}
	}

	var next *migrationFile
	for _, mf := range m.list() {
		if mf.version > headMax {
			next = &mf
			break
		}
	}
	if next == nil || next.version != added {
		return nil, nil
	}
	data, err := m.files.ReadFile(m.dir + "/" + next.name)
	if err != nil {
		return nil, fmt.Errorf("reading migration %s: %w", next.name, err)
	}
	sum := sha256.Sum256(data)
	if hash := working[added]; !hash.Valid || hash.String != hex.EncodeToString(sum[:]) {
		return nil, nil
	}

	sqlText := string(data)
	tables := sortedDirtyTableNames(dirty)
	for _, table := range tables {
		if !migrationSQLTouchesTable(sqlText, table) {
			return nil, nil
		}
	}
	return &strandedStep{file: *next, tables: tables}, nil
}

// finishStrandedStep commits a step findStrandedStep recognised, through the
// same commitMigrationStep a pass that had not been interrupted would have
// run: the step's tables and the cursor table are staged by name, every other
// dirty table is left where it is, and the commit carries the step's own
// message.
func (m migrationSource) finishStrandedStep(ctx context.Context, db DBConn, step *strandedStep) (err error) {
	defer func() { err = classifyMigrationDrift(err, m, step.file) }()
	fmt.Fprintf(stderr, "Finishing migration %04d: %s (applied by an interrupted pass, not yet committed)…\n",
		step.file.version, humanMigrationName(step.file.name))
	leave, err := dirtyTables(ctx, db, true)
	if err != nil {
		return fmt.Errorf("reading dirty tables before finishing %s: %w", step.file.name, err)
	}
	delete(leave, m.cursorTable)
	for _, table := range step.tables {
		delete(leave, table)
	}
	if err := commitMigrationStep(ctx, db, m.cursorTable, step.file.name, leave); err != nil {
		return fmt.Errorf("committing stranded migration %s: %w", step.file.name, err)
	}
	return nil
}

func readCursorRows(ctx context.Context, db DBConn, query string) (map[int]sql.NullString, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int]sql.NullString)
	for rows.Next() {
		var version int
		var hash sql.NullString
		if err := rows.Scan(&version, &hash); err != nil {
			return nil, err
		}
		out[version] = hash
	}
	return out, rows.Err()
}
