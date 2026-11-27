package schema

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
)

// A kill between a numbered migration's SQL (and its cursor row) and its
// per-step commit leaves that one step uncommitted in the working set: the
// cursor table says the step applied while its schema change sits uncommitted.
// runMigrations documented this as a residual window of the #4566 per-step
// commit contract. On a plain open the pass then either refuses every retry
// with *DirtyTablesError (a later pending migration touches one of the
// stranded step's tables) or — when none does — a later step's commit records
// the stranded cursor row while the step's own change stays uncommitted, so
// HEAD claims a version whose schema change is only in the working set.
//
// finishStrandedMainStep recognizes exactly that shape and finishes the step
// through commitMigrationStep itself, under the step's own message, before
// the dirty-table guard runs. Recognition is deliberately narrow — every
// conjunct below must hold, and anything else reaches the guard unchanged:
//
//  1. the main cursor table is `modified` in the working set (not `new
//     table`, which the fresh-bootstrap heal owns, and not deleted/renamed);
//  2. the working cursor is HEAD's cursor plus exactly one version, with
//     every row already at HEAD unchanged (NULL-safe content_hash compare);
//     a HEAD cursor table without the content_hash column — a dead pass whose
//     first step ran ensureContentHashColumn — declines the shape;
//  3. that version is the series' next migration after HEAD's highest, the
//     only step a per-step-committing pass can leave uncommitted: it commits
//     step k before it starts k+1;
//  4. the row's content_hash is the hash of this binary's frozen migration
//     file, computed exactly as runMigrations records it — a hash this
//     binary does not ship is not a step this pass may finish.
//
// Anything else — user dirt beside a stranded cursor row, two uncommitted
// rows, a row without this binary's hash, a step killed before its cursor
// row — declines, and the existing dirty-table guard judges it exactly as at
// any other open.
//
// Recognition runs ahead of the aux-rekey exemption so it reads the pass's
// own fresh dirty set: a step that alters an aux-rekey table as its own work
// (0049 alters comments) needs that change in its commit, as an
// uninterrupted pass makes it.
//
// The finish is by table, not by row: every working-set table the step's SQL
// touches is committed with the step, under the step's message — including
// user rows written into those tables after the kill. The manual
// `bd dolt commit` the old refusal prescribed committed the same rows under
// the user's message. Unrelated dirt stays in the working set and is judged
// by the guard afterwards, unchanged.
//
// In server mode the finish runs inside the migration pass, under the
// database's migration lock; a pass that is still alive holds that lock, so
// only a dead pass's working set can be seen here.

// strandedMainStep recognizes a stranded main-series migration step from the
// database alone. It is read-only and returns ok=false for every working set
// that is not exactly the shape a killed per-step-committing pass leaves
// behind; only a read error is returned as err. It issues no statement unless
// the caller has already established that the main cursor table is dirty in
// the working set.
func strandedMainStep(ctx context.Context, db DBConn) (mf migrationFile, ok bool, err error) {
	statuses, err := cursorTableStatuses(ctx, db, mainSource.cursorTable)
	if err != nil {
		return migrationFile{}, false, err
	}
	// The caller only calls here with the cursor table dirty, so an empty
	// status read is a decline, not an error. `new table` is a fresh
	// bootstrap (the fresh-bootstrap heal owns it); `deleted` and any other
	// status mean the working set is not a step's debris.
	if len(statuses) == 0 {
		return migrationFile{}, false, nil
	}
	for _, status := range statuses {
		if status != "modified" {
			return migrationFile{}, false, nil
		}
	}

	headRows, headHasHash, err := readCursorRows(ctx, db, true)
	if err != nil {
		return migrationFile{}, false, err
	}
	workingRows, workingHasHash, err := readCursorRows(ctx, db, false)
	if err != nil {
		return migrationFile{}, false, err
	}
	// No content_hash column at HEAD: the dead pass added it after its last
	// commit (ensureContentHashColumn runs uncommitted at the pass's front),
	// so the rows that matter cannot be compared. The guard judges this
	// working set, as before.
	if !headHasHash || !workingHasHash {
		return migrationFile{}, false, nil
	}

	series := mainSource.list()
	mf, ok = recognizeStrandedMainStep(headRows, workingRows, series, mainMigrationFileHashes(series))
	return mf, ok, nil
}

// recognizeStrandedMainStep is the DB-free classifier behind
// strandedMainStep: it decides whether (headRows, workingRows) is HEAD's
// cursor plus exactly the series' next step, recorded with the hash of this
// binary's frozen file for that step. headRows and workingRows map a cursor
// version to its content_hash, with a NULL hash mapped to "". series is the
// source's ordered file list and fileHashes maps each of its versions to
// hex(sha256(frozen file)), keyed as runMigrations records them.
func recognizeStrandedMainStep(headRows, workingRows map[int]string, series []migrationFile, fileHashes map[int]string) (migrationFile, bool) {
	// The working set must be HEAD's set plus exactly one version, and every
	// row already at HEAD must be unchanged. A NULL hash maps to "", and no
	// writer records an empty hash, so "" equals only "" — the NULL-safe
	// compare the cursor's pre-#4259 rows need.
	if len(workingRows) != len(headRows)+1 {
		return migrationFile{}, false
	}
	headMax := 0
	for version, headHash := range headRows {
		if workingRows[version] != headHash {
			return migrationFile{}, false
		}
		if version > headMax {
			headMax = version
		}
	}
	stranded := 0
	for version := range workingRows {
		if _, atHead := headRows[version]; !atHead {
			if stranded != 0 {
				return migrationFile{}, false
			}
			stranded = version
		}
	}
	if stranded == 0 {
		return migrationFile{}, false
	}
	// The series' next step after HEAD's highest. A per-step-committing pass
	// commits step k before it starts k+1, so this is the only row such a
	// pass can strand; anything else (two rows, an out-of-order row) declines.
	next := 0
	for _, mf := range series {
		if mf.version > headMax && (next == 0 || mf.version < next) {
			next = mf.version
		}
	}
	if next == 0 || stranded != next {
		return migrationFile{}, false
	}
	// The row must carry this binary's own hash for that step. NULL declines:
	// a row without a hash is not provably this binary's step.
	want, ok := fileHashes[stranded]
	if !ok || want == "" || workingRows[stranded] != want {
		return migrationFile{}, false
	}
	for _, mf := range series {
		if mf.version == stranded {
			return mf, true
		}
	}
	return migrationFile{}, false
}

// mainMigrationFileHashes maps every file in series to the content hash
// runMigrations records for it: hex(sha256(frozen bytes)). series comes from
// mainSource.list(), which enumerated the same embedded FS, so a read here
// cannot fail and panics like list() does if the invariant is ever broken.
func mainMigrationFileHashes(series []migrationFile) map[int]string {
	hashes := make(map[int]string, len(series))
	for _, mf := range series {
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + mf.name)
		if err != nil {
			panic(fmt.Sprintf("schema: failed to read embedded %s/%s already listed by the series: %v", mainSource.dir, mf.name, err))
		}
		sum := sha256.Sum256(data)
		hashes[mf.version] = hex.EncodeToString(sum[:])
	}
	return hashes
}

// cursorTableStatuses returns the dolt_status `status` values for table.
func cursorTableStatuses(ctx context.Context, db DBConn, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT status FROM dolt_status WHERE table_name = ?", table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var statuses []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

// readCursorRows reads table's rows — from HEAD when atHead, else the working
// set — as version -> content_hash, with NULL mapped to "". The columns are
// discovered from the result set rather than spelled out: the `AS OF 'HEAD'`
// shape must not fail on a HEAD table that predates content_hash (a failing
// statement pins a Dolt session's catalog snapshot), and SELECT * always
// succeeds, returning whatever columns that snapshot has. hasHash reports
// whether content_hash was among them.
func readCursorRows(ctx context.Context, db DBConn, atHead bool) (rowsByVersion map[int]string, hasHash bool, err error) {
	query := "SELECT * FROM " + mainSource.cursorTable
	if atHead {
		query += " AS OF 'HEAD'"
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		return nil, false, err
	}
	versionIdx, hashIdx := -1, -1
	for i, column := range columns {
		switch column {
		case "version":
			versionIdx = i
		case "content_hash":
			hashIdx = i
		}
	}
	if versionIdx < 0 {
		return nil, false, fmt.Errorf("reading %s: no version column", mainSource.cursorTable)
	}

	out := make(map[int]string)
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, false, err
		}
		version, ok := cursorVersionValue(values[versionIdx])
		if !ok {
			return nil, false, fmt.Errorf("reading %s: unexpected version value %v", mainSource.cursorTable, values[versionIdx])
		}
		hash := ""
		if hashIdx >= 0 && values[hashIdx] != nil {
			switch v := values[hashIdx].(type) {
			case []byte:
				hash = string(v)
			case string:
				hash = v
			default:
				return nil, false, fmt.Errorf("reading %s: unexpected content_hash value %v", mainSource.cursorTable, values[hashIdx])
			}
		}
		out[version] = hash
	}
	return out, hashIdx >= 0, rows.Err()
}

// cursorVersionValue coerces a scanned version cell to an int. The column is
// INT PRIMARY KEY, so the driver delivers an integer type; the string forms
// are defensive only.
func cursorVersionValue(v any) (int, bool) {
	switch n := v.(type) {
	case int64:
		return int(n), true
	case int32:
		return int(n), true
	case uint64:
		return int(n), true
	case []byte:
		var version int
		if _, err := fmt.Sscanf(string(n), "%d", &version); err == nil {
			return version, true
		}
	case string:
		var version int
		if _, err := fmt.Sscanf(n, "%d", &version); err == nil {
			return version, true
		}
	}
	return 0, false
}

// strandedStepTouchesTable is the touched-table oracle for one main-series
// step: it reports whether the step's SQL touches table, matching the frozen
// file's text and, when migrationBodyPlans registers an execution plan for
// the step, the planned body that actually runs (today only main 59, whose
// rewrite of issues is in the plan's final UPDATE). Registered pre-repairs
// (migration_repairs.go) need no extra list — 40 and 41 self-commit their
// dolt_nonlocal_tables edit, 47 and 58 write dolt-ignored wisp tables, and
// 53's tables are named in 0053's own text; the tripwire test
// TestStrandedStepOracleCoversRegisteredPreRepairs keeps that true when a
// repair is registered.
func strandedStepTouchesTable(mf migrationFile, table string) (bool, error) {
	data, err := mainSource.files.ReadFile(mainSource.dir + "/" + mf.name)
	if err != nil {
		return false, fmt.Errorf("reading migration %s: %w", mf.name, err)
	}
	if migrationSQLTouchesTable(string(data), table) {
		return true, nil
	}
	body, err := mainSource.plannedMigrationBody(mf.version, mf.name, data)
	if err != nil {
		return false, fmt.Errorf("planning migration %s: %w", mf.name, err)
	}
	return migrationSQLTouchesTable(body, table), nil
}

// finishStrandedMainStep recognizes a stranded main-series migration step and,
// when the working set is exactly one, commits it through commitMigrationStep
// under the step's own message. It runs inside migrateUpAfterReconcile after
// the seeded dolt_ignore commit and before the dirty-table guard, so the
// guard, the changed-signature reads and the final staging see only the dirt
// the finish leaves behind.
//
// The finish stages the stranded step's cursor row and every working-set
// table the step's SQL touches (strandedStepTouchesTable); every other dirty
// table goes into dirtyBeforeStep — the set commitMigrationStep skips — and
// stays where it is for the guard to judge. dirtyTables lists dolt_ignore
// itself, so 0062's REPLACE INTO dolt_ignore row is staged because the oracle
// matches it, exactly as an uninterrupted 0062 commits it; every other step
// leaves a dirty dolt_ignore row in place.
//
// A commit error carrying a schema-encoding-drift signature is classified
// into *SchemaEncodingDriftError naming the step, as runMigrations does for
// step-commit errors. Any other commit error aborts the pass: recognition
// was exact, so the pass does not fall through to the guard.
func finishStrandedMainStep(ctx context.Context, db DBConn) (finished bool, err error) {
	mf, ok, err := strandedMainStep(ctx, db)
	if err != nil {
		return false, fmt.Errorf("reading stranded migration state: %w", err)
	}
	if !ok {
		return false, nil
	}

	dirty, err := dirtyTables(ctx, db, true)
	if err != nil {
		return false, fmt.Errorf("reading stranded migration state: %w", err)
	}
	touched := make(map[string]bool, len(dirty))
	for table := range dirty {
		yes, err := strandedStepTouchesTable(mf, table)
		if err != nil {
			return false, err
		}
		touched[table] = yes
	}
	keep := make(map[string]dirtyTableState, len(dirty))
	stagedTables := make([]string, 0, len(dirty)+1)
	for table, state := range dirty {
		if touched[table] {
			stagedTables = append(stagedTables, table)
			continue
		}
		keep[table] = state
	}
	// commitMigrationStep stages the cursor table unconditionally; name it in
	// the log line so the line lists exactly the commit's tables.
	stagedTables = append(stagedTables, mainSource.cursorTable)
	sort.Strings(stagedTables)

	if err := commitMigrationStep(ctx, db, mainSource.cursorTable, mf.name, keep); err != nil {
		return false, classifyMigrationDrift(err, mainSource, mf)
	}
	log.Printf("schema migration finishing stranded migration %s: staged tables %s", mf.name, strings.Join(stagedTables, ", "))
	return true, nil
}
