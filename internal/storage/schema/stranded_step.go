package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
)

// A numbered main migration step writes its SQL and its cursor row
// (runMigrations) and only then commits (commitMigrationStep). A pass killed
// between the two leaves that one step in the working set. The functions in
// this file let the next pass recognize such a step and commit it with the
// commit it missed, before the pre-existing-dirty-table guard would refuse it
// as user dirt (or, when no later migration touches its tables, before a later
// step's commit records its cursor row without its change).

// strandedMainStep reports the main-series step an interrupted pass applied
// and recorded but did not commit, when the working set holds exactly one.
// It is read-only. It reads the cursor table's status first and declines
// (ok=false) unless the table is modified in place: a new table is a fresh
// bootstrap, and a deleted or renamed one cannot be read AS OF HEAD without a
// failing statement that would pin the session's catalog (be-bv7x). It also
// declines when HEAD's cursor table has no content_hash column (the dead
// pass's own first step added it), since HEAD's rows then cannot be compared.
// The remaining conditions are classifyStrandedStep's. A read error is
// returned; only a shape mismatch declines.
func strandedMainStep(ctx context.Context, db DBConn) (mf migrationFile, ok bool, err error) {
	src := mainSource
	modified, err := cursorTableModifiedOnly(ctx, db, src.cursorTable)
	if err != nil || !modified {
		return migrationFile{}, false, err
	}
	headHasHash, err := headTableHasColumn(ctx, db, src.cursorTable, "content_hash")
	if err != nil || !headHasHash {
		return migrationFile{}, false, err
	}
	workingHasHash, err := src.hasContentHashColumn(ctx, db)
	if err != nil || !workingHasHash {
		return migrationFile{}, false, err
	}
	head, err := readCursorRows(ctx, db, "SELECT version, content_hash FROM "+src.cursorTable+" AS OF 'HEAD'")
	if err != nil {
		return migrationFile{}, false, fmt.Errorf("reading %s at HEAD: %w", src.cursorTable, err)
	}
	working, err := readCursorRows(ctx, db, "SELECT version, content_hash FROM "+src.cursorTable)
	if err != nil {
		return migrationFile{}, false, fmt.Errorf("reading %s: %w", src.cursorTable, err)
	}
	return classifyStrandedStep(head, working, src.list(), src.fileContentHash)
}

// classifyStrandedStep is strandedMainStep's DB-free core. It recognizes a
// stranded step only when all of these hold:
//
//   - the working cursor rows are HEAD's rows plus exactly one version v, and
//     every row already at HEAD keeps its content_hash (NULL-safe);
//   - v is the first version in series above HEAD's highest: a pass that
//     commits each step before it starts the next can strand no other;
//   - v's content_hash is non-NULL and equals fileHash of v's frozen file,
//     the value runMigrations records and nothing else writes.
//
// series must be sorted by version, as migrationSource.list returns it.
func classifyStrandedStep(head, working map[int]sql.NullString, series []migrationFile, fileHash func(migrationFile) (string, error)) (migrationFile, bool, error) {
	if len(working) != len(head)+1 {
		return migrationFile{}, false, nil
	}
	headMax := 0
	for version, hash := range head {
		got, ok := working[version]
		if !ok || got != hash {
			return migrationFile{}, false, nil
		}
		headMax = max(headMax, version)
	}
	added, found := 0, false
	for version := range working {
		if _, atHead := head[version]; !atHead {
			added, found = version, true
		}
	}
	if !found {
		return migrationFile{}, false, nil
	}
	var next migrationFile
	for _, mf := range series {
		if mf.version > headMax {
			next = mf
			break
		}
	}
	if next.name == "" || next.version != added {
		return migrationFile{}, false, nil
	}
	recorded := working[added]
	if !recorded.Valid {
		return migrationFile{}, false, nil
	}
	want, err := fileHash(next)
	if err != nil {
		return migrationFile{}, false, err
	}
	if recorded.String != want {
		return migrationFile{}, false, nil
	}
	return next, true, nil
}

// fileContentHash is the content_hash runMigrations records for mf: the hex
// SHA-256 of its frozen file bytes.
func (m migrationSource) fileContentHash(mf migrationFile) (string, error) {
	data, err := m.files.ReadFile(m.dir + "/" + mf.name)
	if err != nil {
		return "", fmt.Errorf("reading migration %s: %w", mf.name, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// stepTouchesTable reports whether migration mf of m writes table, by the
// same text match the dirty-table guard uses (migrationSQLTouchesTable), over
// its frozen file and, when a body plan is registered for it, over the body
// runMigrations actually executes. Registered pre-repairs need no list of
// their own; TestStrandedStepPreRepairsNeedNoTouchedTables keeps that true.
func (m migrationSource) stepTouchesTable(mf migrationFile, table string) (bool, error) {
	data, err := m.files.ReadFile(m.dir + "/" + mf.name)
	if err != nil {
		return false, fmt.Errorf("reading migration %s: %w", mf.name, err)
	}
	if migrationSQLTouchesTable(string(data), table) {
		return true, nil
	}
	if _, planned := migrationBodyPlans[repairKey{m.cursorTable, mf.version}]; !planned {
		return false, nil
	}
	body, err := m.plannedMigrationBody(mf.version, mf.name, data)
	if err != nil {
		return false, fmt.Errorf("planning migration %s: %w", mf.name, err)
	}
	return migrationSQLTouchesTable(body, table), nil
}

// finishStrandedMainStep commits the step strandedMainStep recognizes, if
// any, through commitMigrationStep under the step's own message: the cursor
// table and every committable dirty table the step's SQL touches are staged
// by name, and every other dirty table stays in the working set for the
// guard to judge as it would at any other open. Recognition is by table, not
// by row, so an uncommitted write that landed in one of the step's tables
// after the kill is committed with the step.
//
// cursorDirty is whether the main cursor table was in the pass's dirty set;
// when it was not, nothing is read. A commit error carrying an encoding-drift
// signature comes back as a *SchemaEncodingDriftError naming the step, as a
// step commit's does in runMigrations; any other error aborts the pass.
func finishStrandedMainStep(ctx context.Context, db DBConn, cursorDirty bool) (finished bool, err error) {
	if !cursorDirty {
		return false, nil
	}
	mf, ok, err := strandedMainStep(ctx, db)
	if err != nil {
		return false, fmt.Errorf("reading stranded migration state: %w", err)
	}
	if !ok {
		return false, nil
	}
	defer func() { err = classifyMigrationDrift(err, mainSource, mf) }()

	dirty, err := dirtyTables(ctx, db, true)
	if err != nil {
		return false, fmt.Errorf("reading dirty tables before finishing migration %s: %w", mf.name, err)
	}
	keep := make(map[string]dirtyTableState, len(dirty))
	staged := []string{mainSource.cursorTable}
	for table, state := range dirty {
		if table == mainSource.cursorTable {
			continue
		}
		touched, err := mainSource.stepTouchesTable(mf, table)
		if err != nil {
			return false, err
		}
		if touched {
			staged = append(staged, table)
			continue
		}
		keep[table] = state
	}
	sort.Strings(staged)
	if err := commitMigrationStep(ctx, db, mainSource.cursorTable, mf.name, keep); err != nil {
		return false, fmt.Errorf("committing stranded migration %s: %w", mf.name, err)
	}
	log.Printf("schema migration: committed %s, which an interrupted migration pass applied but did not commit (tables: %s)",
		mf.name, strings.Join(staged, ", "))
	return true, nil
}

// cursorTableModifiedOnly reports whether table appears in dolt_status only
// as modified.
func cursorTableModifiedOnly(ctx context.Context, db DBConn, table string) (bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT status FROM dolt_status WHERE table_name = ?", table)
	if err != nil {
		return false, fmt.Errorf("reading %s status: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	seen := false
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return false, fmt.Errorf("reading %s status: %w", table, err)
		}
		if status != "modified" {
			return false, nil
		}
		seen = true
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("reading %s status: %w", table, err)
	}
	return seen, nil
}

// headTableHasColumn reports whether table at HEAD has column. The caller
// knows table exists at HEAD (it is modified in the working set), so the
// statement cannot fail on a missing table.
func headTableHasColumn(ctx context.Context, db DBConn, table, column string) (bool, error) {
	var name, ddl string
	//nolint:gosec // G202: table is a migration-series constant.
	if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE "+table+" AS OF 'HEAD'").Scan(&name, &ddl); err != nil {
		return false, fmt.Errorf("reading %s schema at HEAD: %w", table, err)
	}
	return strings.Contains(strings.ToLower(ddl), "`"+strings.ToLower(column)+"`"), nil
}

func readCursorRows(ctx context.Context, db DBConn, query string) (map[int]sql.NullString, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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
