package schema

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// updated_at guard tests (#5981, bd-chqckf). The guard snapshots
// (id, updated_at) before a registered value-rewriting migration step and
// restores the historical timestamps after its body, before the cursor row
// and per-step commit record the step. These sqlmock tests pin the runner's
// statement ORDER and the restore statement's shape; the real-Dolt outcomes
// are pinned in internal/storage/embeddeddolt
// (migrate_updated_at_preservation_test.go) and internal/storage/dolt
// (migration_is_blocked_updated_at_test.go).

const (
	// updatedGuardOldTS is a row's historical timestamp, the value that must
	// survive the step.
	updatedGuardOldTS = "2026-01-15 10:00:00"
	// updatedGuardNewTS is what the migration body stamps over it.
	updatedGuardNewTS = "2026-11-02 05:00:00"
)

// updatedGuardSelectRegex matches the guard's snapshot/reread query.
const updatedGuardSelectRegex = "SELECT id, updated_at FROM `issues` ORDER BY id"

// updatedGuardRestoreRegex matches the single-row shape of the guard's
// compare-and-swap restore statement.
const updatedGuardRestoreRegex = "UPDATE `issues` SET updated_at = CASE id WHEN \\? THEN \\? ELSE updated_at END WHERE \\(id = \\? AND updated_at = \\?\\)"

// updatedGuardTableQuery matches the guard's table-existence probe.
const updatedGuardTableQuery = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`

// updatedGuardDirtyStatusQuery matches the runner's dolt_status snapshot.
const updatedGuardDirtyStatusQuery = `(?s)SELECT s\.table_name, s\.staged\s+FROM dolt_status s\s+WHERE NOT EXISTS`

// updatedGuardPrimeThroughBody primes everything runMigrations issues for
// migration 0046 (a registered step) from v45 up to and including the body:
// the pre-step dirty-table snapshot (commitEachStep path only), the guard's
// existence probe and snapshot read, the frozen body, and the restore path's
// existence probe. The caller primes the post-body reread (rows or error),
// any restore statement, and the tail via updatedGuardPrimeTail, so each test
// shapes its own failure point.
func updatedGuardPrimeThroughBody(t *testing.T, mock sqlmock.Sqlmock, commitEachStep bool) {
	t.Helper()
	if commitEachStep {
		mock.ExpectQuery(updatedGuardDirtyStatusQuery).
			WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}))
	}
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(updatedGuardSelectRegex).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("i1", updatedGuardOldTS))
	// No pre-migration repair is registered at version 46.
	mock.ExpectExec(`(?s)SET @needs_add`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
}

// updatedGuardPrimeTail primes the cursor-row insert and, on the
// commitEachStep path, the step's atomic commit of the dirtied tables.
func updatedGuardPrimeTail(mock sqlmock.Sqlmock, commitEachStep bool) {
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(46, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if commitEachStep {
		mock.ExpectQuery(updatedGuardDirtyStatusQuery).
			WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).AddRow("issues", false))
		mock.ExpectQuery(`CALL DOLT_ADD\('-f', \?\)`).WithArgs("issues").
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
		mock.ExpectQuery(`CALL DOLT_ADD\('-f', \?\)`).WithArgs("schema_migrations").
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
		mock.ExpectQuery(`CALL DOLT_COMMIT`).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	}
}

// updatedGuardWispsSelectRegex is updatedGuardSelectRegex's wisps-table
// counterpart, and updatedGuardWispsRestoreRegex the two-row restore shape
// the 0035 subtests pin.
const (
	updatedGuardWispsSelectRegex = "SELECT id, updated_at FROM `wisps` ORDER BY id"
)
const updatedGuardWispsRestoreRegex = `UPDATE ` + "`wisps`" + ` SET updated_at = CASE id WHEN \? THEN \? WHEN \? THEN \? ELSE updated_at END WHERE \(id = \? AND updated_at = \?\) OR \(id = \? AND updated_at = \?\)`

// TestUpdatedAtGuardRowMovesPreferDestinationHistory pins the 0035/0053
// lookup rules at statement level: a moved row is restored from its
// source-table snapshot, a row that already existed in the restore table
// keeps that table's own value even when the source table holds a same-id
// snapshot, and rows the step created (no snapshot anywhere) or deleted
// (absent from the reread) are left entirely alone.
func TestUpdatedAtGuardRowMovesPreferDestinationHistory(t *testing.T) {
	failOnSwallowedAdvisory(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Snapshot: issues holds the moving row plus a same-id value that must
	// NOT win; wisps holds the destination's own value for that id plus a
	// row the step will delete.
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(updatedGuardSelectRegex).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
			AddRow("mover", "2026-01-06 10:00:00").
			AddRow("native", "2026-01-01 00:00:00"))
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("wisps").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(updatedGuardWispsSelectRegex).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
			AddRow("doomed", "2026-01-02 00:00:00").
			AddRow("native", "2026-02-10 08:00:00"))
	// 0035's body (bounded pass: no per-step commit).
	mock.ExpectExec(`(?s)SET @shared_cols`).WillReturnResult(sqlmock.NewResult(0, 0))
	// Reread: mover arrived stamped, native (collision) got stamped, fresh
	// was created by the step, doomed is gone. Only mover and native are
	// restored — mover from issues, native from wisps' own value, not the
	// issues value under the same id.
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("wisps").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(updatedGuardWispsSelectRegex).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
			AddRow("fresh", updatedGuardNewTS).
			AddRow("mover", updatedGuardNewTS).
			AddRow("native", updatedGuardNewTS))
	mock.ExpectExec(updatedGuardWispsRestoreRegex).
		WithArgs("mover", "2026-01-06 10:00:00", "native", "2026-02-10 08:00:00",
			"mover", updatedGuardNewTS, "native", updatedGuardNewTS).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(35, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if _, err := runMigrations(context.Background(), db, mainSource, 34, 35, false); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations (lookup precedence or skip rules wrong): %v", err)
	}
}

// TestUpdatedAtGuardIssuesNoQueriesForUnregisteredStep pins the guard's
// default-off contract (#5981): a version outside the registry runs with
// zero snapshot, reread, or restore queries — the guard must never become a
// tax on the ordinary migration path. The mock primes ONLY the step's own
// statements (0052's body and its cursor row; 0052 carries no
// pre-migration repair), so a single guard probe or read against a
// mistakenly-registered version surfaces as an unexpected call and fails
// the step. Vacuous against the pre-guard base by construction (there is
// nothing to issue the queries); it exists to pin the registry's shape.
func TestUpdatedAtGuardIssuesNoQueriesForUnregisteredStep(t *testing.T) {
	failOnSwallowedAdvisory(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`(?s)SET @has_old`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(52, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	applied, err := runMigrations(context.Background(), db, mainSource, 51, 52, false)
	if err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if applied != 1 {
		t.Fatalf("runMigrations applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestUpdatedAtGuardRestoresTimestampsAroundRegisteredStep pins the guard's
// position in the step: snapshot before the body, restore between a
// successful body and the cursor row, so the restored values land in the same
// atomic per-step commit as the version that produced them. On the bounded
// (MigrateUpTo) path there is no per-step commit, but the restore and its
// ordering around the cursor row are identical.
func TestUpdatedAtGuardRestoresTimestampsAroundRegisteredStep(t *testing.T) {
	failOnSwallowedAdvisory(t)

	postBodyStamped := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("i1", updatedGuardNewTS)
	}

	t.Run("restore precedes cursor row and step commit", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, true)
		// The body stamped i1, so the post-body reread reports the
		// migration-time value and the guard must compare-and-swap it back
		// BEFORE the cursor row records.
		mock.ExpectQuery(updatedGuardSelectRegex).WillReturnRows(postBodyStamped())
		mock.ExpectExec(updatedGuardRestoreRegex).
			WithArgs("i1", updatedGuardOldTS, "i1", updatedGuardNewTS).
			WillReturnResult(sqlmock.NewResult(0, 1))
		updatedGuardPrimeTail(mock, true)

		applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, true)
		if err != nil {
			t.Fatalf("runMigrations: %v", err)
		}
		if applied != 1 {
			t.Fatalf("runMigrations applied = %d, want 1", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations (statement order would surface here): %v", err)
		}
	})

	t.Run("bounded pass restores the same way", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, false)
		mock.ExpectQuery(updatedGuardSelectRegex).WillReturnRows(postBodyStamped())
		mock.ExpectExec(updatedGuardRestoreRegex).
			WithArgs("i1", updatedGuardOldTS, "i1", updatedGuardNewTS).
			WillReturnResult(sqlmock.NewResult(0, 1))
		updatedGuardPrimeTail(mock, false)

		applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, false)
		if err != nil {
			t.Fatalf("runMigrations: %v", err)
		}
		if applied != 1 {
			t.Fatalf("runMigrations applied = %d, want 1", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("rows the body left alone are not rewritten", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, false)
		// Post-body value equals the snapshot: no restore statement is primed
		// at all, so a single issued UPDATE would surface as an unexpected
		// call and an unmet-expectations failure.
		mock.ExpectQuery(updatedGuardSelectRegex).
			WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("i1", updatedGuardOldTS))
		updatedGuardPrimeTail(mock, false)

		if _, err := runMigrations(context.Background(), db, mainSource, 45, 46, false); err != nil {
			t.Fatalf("runMigrations: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("a compare-and-swap miss is not an error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, false)
		mock.ExpectQuery(updatedGuardSelectRegex).WillReturnRows(postBodyStamped())
		// Another writer changed the row between the reread and the restore:
		// the predicate matches nothing (0 rows affected). The guard's CAS
		// means that is a deliberate leave-alone, not a failure — the step
		// still records, exactly as it would for any concurrent write the
		// migration does not own.
		mock.ExpectExec(updatedGuardRestoreRegex).
			WithArgs("i1", updatedGuardOldTS, "i1", updatedGuardNewTS).
			WillReturnResult(sqlmock.NewResult(0, 0))
		updatedGuardPrimeTail(mock, false)

		applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, false)
		if err != nil {
			t.Fatalf("runMigrations: %v (a CAS miss must not fail the step)", err)
		}
		if applied != 1 {
			t.Fatalf("runMigrations applied = %d, want 1", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("rows past the batch boundary split into bounded statements", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		prevBatch := updatedAtRestoreBatchRows
		updatedAtRestoreBatchRows = 2
		t.Cleanup(func() { updatedAtRestoreBatchRows = prevBatch })

		mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
		mock.ExpectQuery(updatedGuardSelectRegex).
			WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
				AddRow("i1", "2026-01-10 09:00:00").
				AddRow("i2", "2026-01-11 09:00:00").
				AddRow("i3", "2026-01-12 09:00:00"))
		mock.ExpectExec(`(?s)SET @needs_add`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
		mock.ExpectQuery(updatedGuardSelectRegex).
			WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
				AddRow("i1", updatedGuardNewTS).
				AddRow("i2", updatedGuardNewTS).
				AddRow("i3", updatedGuardNewTS))
		// i1 and i2 fill one statement (two WHEN arms, two OR-ed predicates);
		// i3 spills into a second statement in the single-row shape.
		mock.ExpectExec(`UPDATE `+"`issues`"+` SET updated_at = CASE id WHEN \? THEN \? WHEN \? THEN \? ELSE updated_at END WHERE \(id = \? AND updated_at = \?\) OR \(id = \? AND updated_at = \?\)`).
			WithArgs("i1", "2026-01-10 09:00:00", "i2", "2026-01-11 09:00:00",
				"i1", updatedGuardNewTS, "i2", updatedGuardNewTS).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(updatedGuardRestoreRegex).
			WithArgs("i3", "2026-01-12 09:00:00", "i3", updatedGuardNewTS).
			WillReturnResult(sqlmock.NewResult(0, 1))
		updatedGuardPrimeTail(mock, false)

		if _, err := runMigrations(context.Background(), db, mainSource, 45, 46, false); err != nil {
			t.Fatalf("runMigrations: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations (batch shape or order wrong): %v", err)
		}
	})
}

// The census half of the guard tests (#5981): the registry is only as good
// as its coverage of the shipped SQL, so TestMigrationUpdatesOfAutoStamped-
// TablesKeepUpdatedAt re-derives, from the migration files themselves, which
// tables re-stamp on UPDATE and which migration statements rewrite them
// without assigning updated_at, and fails when any such statement lacks a
// guard entry with the right restore target.

// censusFile is one shipped migration's identity plus its SQL.
type censusFile struct {
	cursorTable string
	name        string
	version     int
	content     string
}

// censusMigrationContents reads every shipped .up.sql file in both series.
func censusMigrationContents(t *testing.T) []censusFile {
	t.Helper()
	var files []censusFile
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		for _, mf := range src.list() {
			data, err := src.files.ReadFile(src.dir + "/" + mf.name)
			if err != nil {
				t.Fatalf("read migration %s: %v", mf.name, err)
			}
			files = append(files, censusFile{cursorTable: src.cursorTable, name: mf.name, version: mf.version, content: string(data)})
		}
	}
	return files
}

var (
	// A CREATE TABLE line opens the column list the tracked table owns until
	// the next one; __temp__ prefixes are dropped (ignored/0001 builds wisps
	// as __temp__wisps and renames it).
	censusCreateTableRE = regexp.MustCompile(`(?i)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + "`?" + `([A-Za-z0-9_]+)`)
	// A column line defining the auto-stamp clause the guard exists to undo.
	censusAutoStampRE = regexp.MustCompile(`(?i)^\s*updated_at\b.*ON\s+UPDATE\s+CURRENT_TIMESTAMP`)
	// Any UPDATE token, including the ones living inside prepared-statement
	// strings (0035/0053's ephemeral flips) — those are real statements the
	// engine executes.
	censusUpdateRE = regexp.MustCompile(`(?i)\bUPDATE\s+([A-Za-z_][A-Za-z0-9_]*)`)
	// The self-protecting form 0059/ignored-0015 use.
	censusSelfAssignRE = regexp.MustCompile(`(?i)updated_at\s*=\s*updated_at`)
)

// censusAutoStampedTables scans both series' DDL for tables whose updated_at
// column carries ON UPDATE CURRENT_TIMESTAMP — the tables an UPDATE that
// forgets the column silently re-stamps.
func censusAutoStampedTables(files []censusFile) map[string]bool {
	stamped := make(map[string]bool)
	for _, f := range files {
		table := ""
		for _, line := range strings.Split(f.content, "\n") {
			if m := censusCreateTableRE.FindStringSubmatch(line); m != nil {
				table = strings.TrimPrefix(m[1], "__temp__")
				continue
			}
			if table != "" && censusAutoStampRE.MatchString(line) {
				stamped[table] = true
			}
		}
	}
	return stamped
}

// censusUnsafeUpdatesIn scans one migration's SQL for UPDATE statements that
// rewrite an auto-stamped table without assigning updated_at: ON UPDATE
// clauses in DDL/FK definitions (the token preceded by "ON ") and statements
// that self-assign `updated_at = updated_at` within the statement are not
// unsafe. The statement window runs to the next `;` — the shipped UPDATE
// statements carry no semicolons inside their string literals.
func censusUnsafeUpdatesIn(content string, stamped map[string]bool) []string {
	var out []string
	for _, m := range censusUpdateRE.FindAllStringSubmatchIndex(content, -1) {
		table := content[m[2]:m[3]]
		if !stamped[table] {
			continue
		}
		if m[0] >= 3 && strings.EqualFold(content[m[0]-3:m[0]], "ON ") {
			continue
		}
		windowEnd := strings.IndexByte(content[m[1]:], ';')
		if windowEnd < 0 {
			windowEnd = len(content)
		} else {
			windowEnd += m[1]
		}
		if censusSelfAssignRE.MatchString(content[m[1]:windowEnd]) {
			continue
		}
		out = append(out, table)
	}
	return out
}

// censusCoverageProblems reports, one line each, every migration version that
// rewrites an auto-stamped table without a guard plan restoring that exact
// table (and snapshotting it). guards is a parameter, not the global, so the
// mutation subtests can run a doctored copy.
func censusCoverageProblems(files []censusFile, stamped map[string]bool, guards map[repairKey]updatedAtGuardPlan) []string {
	rewrites := make(map[repairKey]map[string]bool)
	for _, f := range files {
		for _, table := range censusUnsafeUpdatesIn(f.content, stamped) {
			key := repairKey{f.cursorTable, f.version}
			if rewrites[key] == nil {
				rewrites[key] = make(map[string]bool)
			}
			rewrites[key][table] = true
		}
	}
	var problems []string
	for key, tables := range rewrites {
		plan, ok := guards[key]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s version %d rewrites an auto-stamped table without an updated_at guard entry", key.cursorTable, key.version))
			continue
		}
		for table := range tables {
			if plan.RestoreTable != table {
				problems = append(problems, fmt.Sprintf("%s version %d rewrites %s but the guard restores %s", key.cursorTable, key.version, table, plan.RestoreTable))
			}
			snapshotted := false
			for _, st := range plan.SnapshotTables {
				if st == table {
					snapshotted = true
				}
			}
			if !snapshotted {
				problems = append(problems, fmt.Sprintf("%s version %d rewrites %s but the guard never snapshots it", key.cursorTable, key.version, table))
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// TestMigrationUpdatesOfAutoStampedTablesKeepUpdatedAt is the census for
// #5981: every UPDATE a shipped migration issues against an auto-stamped
// table is either self-protecting (updated_at = updated_at, the 0059 shape)
// or covered by a guard registry entry whose restore target is the rewritten
// table. A new migration that forgets the rule fails the pinned-findings
// subtest; a registry edit that drops or misaims coverage fails the coverage
// subtests — including the two mutation proofs the AC names (remove main/47,
// add an unguarded UPDATE).
func TestMigrationUpdatesOfAutoStampedTablesKeepUpdatedAt(t *testing.T) {
	failOnSwallowedAdvisory(t)
	files := censusMigrationContents(t)
	stamped := censusAutoStampedTables(files)

	t.Run("auto-stamped table set is pinned", func(t *testing.T) {
		want := []string{"federation_peers", "issues", "routes", "wisps"}
		var got []string
		for table := range stamped {
			got = append(got, table)
		}
		sort.Strings(got)
		if len(got) != len(want) {
			t.Fatalf("auto-stamped tables = %v, want %v (a DDL change here must be reflected in the guard census)", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("auto-stamped tables = %v, want %v (a DDL change here must be reflected in the guard census)", got, want)
			}
		}
	})

	t.Run("unsafe UPDATE findings are pinned", func(t *testing.T) {
		var got []string
		for _, f := range files {
			for _, table := range censusUnsafeUpdatesIn(f.content, stamped) {
				got = append(got, f.name+" -> "+table)
			}
		}
		sort.Strings(got)
		want := []string{
			"0007_recompute_wisp_is_blocked.up.sql -> wisps",
			"0007_recompute_wisp_is_blocked.up.sql -> wisps",
			"0035_migrate_infra_to_wisps.up.sql -> wisps",
			"0046_add_is_blocked.up.sql -> issues",
			"0046_add_is_blocked.up.sql -> issues",
			"0047_recompute_mixed_is_blocked.up.sql -> issues",
			"0047_recompute_mixed_is_blocked.up.sql -> issues",
			"0053_repair_rig_wisps.up.sql -> issues",
		}
		if len(got) != len(want) {
			t.Fatalf("census found %d unsafe UPDATEs (%v), want the pinned %d (%v): a new migration that rewrites an auto-stamped table without assigning updated_at needs a guard registry entry", len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("census findings = %v, want %v", got, want)
			}
		}
	})

	t.Run("every unsafe UPDATE has the correct restore target", func(t *testing.T) {
		if problems := censusCoverageProblems(files, stamped, updatedAtGuards); len(problems) > 0 {
			t.Fatalf("unsafe UPDATEs without correct guard coverage:\n%s", strings.Join(problems, "\n"))
		}
	})

	t.Run("removing main/47 coverage fails the census", func(t *testing.T) {
		doctored := make(map[repairKey]updatedAtGuardPlan, len(updatedAtGuards))
		for k, v := range updatedAtGuards {
			doctored[k] = v
		}
		delete(doctored, repairKey{"schema_migrations", 47})
		problems := censusCoverageProblems(files, stamped, doctored)
		if len(problems) == 0 {
			t.Fatal("census accepted a registry without main/47 coverage; it must fail")
		}
		if !strings.Contains(problems[0], "version 47") {
			t.Fatalf("census blamed %q, want the missing main/47 entry", problems[0])
		}
	})

	t.Run("an unguarded UPDATE fails the census", func(t *testing.T) {
		synthetic := []censusFile{{
			cursorTable: mainSource.cursorTable,
			name:        "9999_synthetic.up.sql",
			version:     9999,
			content:     "UPDATE issues SET priority = 1;\nUPDATE issues SET is_blocked = 1, updated_at = updated_at;",
		}}
		// The scanner itself must find the unguarded statement and skip the
		// self-protecting one.
		got := censusUnsafeUpdatesIn(synthetic[0].content, stamped)
		if len(got) != 1 || got[0] != "issues" {
			t.Fatalf("scanner findings on synthetic SQL = %v, want exactly [issues] (the unguarded statement; the self-assigning one is safe)", got)
		}
		problems := censusCoverageProblems(synthetic, stamped, updatedAtGuards)
		if len(problems) == 0 {
			t.Fatal("census accepted an unguarded UPDATE; it must fail")
		}
		if !strings.Contains(problems[0], "version 9999") {
			t.Fatalf("census blamed %q, want the synthetic version", problems[0])
		}
	})
}

// TestUpdatedAtGuardSnapshotFailureAbortsStep proves a snapshot failure fails
// the step before anything it could corrupt: no migration body, no cursor row,
// no commit.
func TestUpdatedAtGuardSnapshotFailureAbortsStep(t *testing.T) {
	failOnSwallowedAdvisory(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// commitEachStep primes the dirty-table snapshot first; the failure point
	// is the guard's snapshot read, after its table probe.
	mock.ExpectQuery(updatedGuardDirtyStatusQuery).
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}))
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(updatedGuardSelectRegex).
		WillReturnError(errors.New("snapshot read failed"))

	applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, true)
	if err == nil || !strings.Contains(err.Error(), "updated_at") {
		t.Fatalf("runMigrations err = %v, want an updated_at snapshot failure", err)
	}
	if applied != 0 {
		t.Fatalf("runMigrations applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations (a body/cursor statement ran after the failure?): %v", err)
	}
}

// TestUpdatedAtGuardRestoreFailureAbortsStep proves a restore failure fails
// the step after a successful body but before the cursor row records: the
// version stays pending, so the next pass re-runs body and restore together.
func TestUpdatedAtGuardRestoreFailureAbortsStep(t *testing.T) {
	failOnSwallowedAdvisory(t)

	t.Run("post-body reread fails", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, true)
		mock.ExpectQuery(updatedGuardSelectRegex).
			WillReturnError(errors.New("post-body read failed"))

		applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, true)
		if err == nil || !strings.Contains(err.Error(), "updated_at") {
			t.Fatalf("runMigrations err = %v, want an updated_at restore failure", err)
		}
		if applied != 0 {
			t.Fatalf("runMigrations applied = %d, want 0 (cursor must not record the step)", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations (a cursor/commit statement ran after the failure?): %v", err)
		}
	})

	t.Run("restore statement fails", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, true)
		mock.ExpectQuery(updatedGuardSelectRegex).
			WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("i1", updatedGuardNewTS))
		mock.ExpectExec(updatedGuardRestoreRegex).
			WithArgs("i1", updatedGuardOldTS, "i1", updatedGuardNewTS).
			WillReturnError(errors.New("restore write failed"))

		applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, true)
		if err == nil || !strings.Contains(err.Error(), "updated_at") {
			t.Fatalf("runMigrations err = %v, want an updated_at restore failure", err)
		}
		if applied != 0 {
			t.Fatalf("runMigrations applied = %d, want 0 (cursor must not record the step)", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations (a cursor/commit statement ran after the failure?): %v", err)
		}
	})
}
