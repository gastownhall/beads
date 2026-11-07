package schema

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const ignoredMigration0032Up = "0032_wisp_is_blocked_restore_marker.up.sql"

// TestLatestIgnoredVersionIncludesMigration0032 pins the ignored slot the
// wisps.is_blocked restore claims, directly above the pre-upgrade events
// restore (ignored 0031), and that the slot carries restoreWispIsBlocked
// while the earlier is_blocked and events repairs keep theirs.
func TestLatestIgnoredVersionIncludesMigration0032(t *testing.T) {
	const wantIgnored = 32
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (wisps.is_blocked restore)", got, wantIgnored)
	}
	same := func(a, b func(context.Context, DBConn) error) bool {
		return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
	}
	for version, want := range map[int]func(context.Context, DBConn) error{
		7:  ensureWispIsBlockedForRecompute,
		15: ensureWispIsBlockedForRecompute,
		31: seedEventsFromPreflipHistory,
		32: restoreWispIsBlocked,
	} {
		got, ok := preMigrationRepairs[repairKey{ignoredSource.cursorTable, version}]
		if !ok || !same(got, want) {
			t.Errorf("ignored %04d: pre-migration repair is not the expected function", version)
		}
	}
}

// TestIgnored32MarkerIsInert pins the marker body: its only statement is
// SELECT 1, and its header declares the wisps writes its repair makes (for
// the pending-migration dirty-table guard) and no durable-table write.
func TestIgnored32MarkerIsInert(t *testing.T) {
	markerSQL, err := IgnoredMigrationSQL(ignoredMigration0032Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) = %v", ignoredMigration0032Up, err)
	}
	var statements []string
	for _, line := range strings.Split(markerSQL, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "--") {
			statements = append(statements, line)
		}
	}
	if !slices.Equal(statements, []string{"SELECT 1;"}) {
		t.Fatalf("ignored 0032 executes %q, want only SELECT 1; the work belongs to its repair", statements)
	}
	if !migrationSQLTouchesTable(markerSQL, "wisps") {
		t.Fatal("ignored 0032 does not declare its wisps writes; the dirty-table guard would not see them")
	}
	for _, table := range []string{"issues", "dependencies", "wisp_dependencies", "events"} {
		if migrationSQLTouchesTable(markerSQL, table) {
			t.Errorf("ignored 0032 declares a write to %s, which its repair never touches", table)
		}
	}
}

// wispRestoreDB runs ignored 0032 through the real runner. It answers the
// repair's INFORMATION_SCHEMA probes from an ordered sqlmock and records
// every statement the pass executes, so a test asserts the exact sequence.
type wispRestoreDB struct {
	t    *testing.T
	real *sql.DB
	mock sqlmock.Sqlmock
	body string
	// steps is every ExecContext the pass made, the marker body and cursor
	// row named rather than spelled out.
	steps []string
	// failOn fails the step it names once, after recording it.
	failOn map[string]error
	// recomputeErr, when set, is the recompute step's failure.
	recomputeErr error
}

const (
	wispRestoreTableQuery  = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`
	wispRestoreColumnQuery = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME = \? AND COLUMN_NAME = \?`
	wispRestoreIndexQuery  = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.STATISTICS.*TABLE_NAME = \? AND INDEX_NAME = \?`

	wispRestoreStepBody      = "marker body"
	wispRestoreStepCursor    = "cursor 32"
	wispRestoreStepRecompute = "recompute wisps.is_blocked"
)

func newWispRestoreDB(t *testing.T) *wispRestoreDB {
	t.Helper()
	failOnSwallowedAdvisory(t)
	quietStderr(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	body, err := IgnoredMigrationSQL(ignoredMigration0032Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s): %v", ignoredMigration0032Up, err)
	}
	d := &wispRestoreDB{t: t, real: db, mock: mock, body: body}
	// The live recompute is issueops' to test; here it is one recorded step.
	prev := recomputeWispIsBlocked
	recomputeWispIsBlocked = func(ctx context.Context, conn DBConn) error {
		if conn != DBConn(d) {
			t.Errorf("recompute ran on %T, not the migration connection", conn)
		}
		d.steps = append(d.steps, wispRestoreStepRecompute)
		err := d.recomputeErr
		d.recomputeErr = nil
		return err
	}
	t.Cleanup(func() { recomputeWispIsBlocked = prev })
	return d
}

// expectProbes scripts the repair's probes for one pass. A pass on a clone
// without wisps stops after the first.
func (d *wispRestoreDB) expectProbes(hasWisps, hasColumn, hasIndex bool) {
	n := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	d.mock.ExpectQuery(wispRestoreTableQuery).WithArgs("wisps").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(n(hasWisps)))
	if !hasWisps {
		return
	}
	d.mock.ExpectQuery(wispRestoreColumnQuery).WithArgs("wisps", "is_blocked").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(n(hasColumn)))
	d.mock.ExpectQuery(wispRestoreIndexQuery).WithArgs("wisps", "idx_wisps_is_blocked").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(n(hasIndex)))
}

func (d *wispRestoreDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.real.QueryRowContext(ctx, query, args...)
}

func (d *wispRestoreDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	d.t.Fatal("wispRestoreDB: QueryContext not expected on the ignored 0032 path")
	return nil, nil
}

func (d *wispRestoreDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	d.t.Helper()
	step := query
	switch {
	case query == d.body:
		step = wispRestoreStepBody
	case query == "INSERT IGNORE INTO "+ignoredSource.cursorTable+" (version, content_hash) VALUES (?, ?)":
		if len(args) == 0 || args[0] != 32 {
			d.t.Fatalf("wispRestoreDB: cursor INSERT args %v, want version 32", args)
		}
		step = wispRestoreStepCursor
	}
	d.steps = append(d.steps, step)
	if err, ok := d.failOn[step]; ok {
		delete(d.failOn, step)
		return nil, err
	}
	return execOK{}, nil
}

// migrate runs one pass with ignored 0032 the only pending version.
func (d *wispRestoreDB) migrate() (int, error) {
	return runMigrations(context.Background(), d, ignoredSource, 31, 32, false)
}

func (d *wispRestoreDB) requireSteps(want ...string) {
	d.t.Helper()
	if err := d.mock.ExpectationsWereMet(); err != nil {
		d.t.Fatalf("unmet probe expectations: %v", err)
	}
	if !slices.Equal(d.steps, want) {
		d.t.Fatalf("ignored 0032 pass executed:\n  %s\nwant:\n  %s",
			strings.Join(d.steps, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestIgnored32RestoresColumn is AC-1 and AC-2 at the runner: with the
// column and its index gone at cursor 31, the pass restores the column at
// ignored/0006's shape, derives the wisp flags, and only then creates the
// index, all before the marker and its cursor row.
func TestIgnored32RestoresColumn(t *testing.T) {
	d := newWispRestoreDB(t)
	d.expectProbes(true, false, false)
	applied, err := d.migrate()
	if err != nil {
		t.Fatalf("runMigrations(ignored 31..32): %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	d.requireSteps(wispIsBlockedColumnDDL, wispRestoreStepRecompute, wispIsBlockedIndexDDL,
		wispRestoreStepBody, wispRestoreStepCursor)
}

// TestIgnored32RestoresIndexOnly covers index-only loss: the column stays,
// its flags are recomputed anyway (nothing says they are current), and the
// index is created.
func TestIgnored32RestoresIndexOnly(t *testing.T) {
	d := newWispRestoreDB(t)
	d.expectProbes(true, true, false)
	if _, err := d.migrate(); err != nil {
		t.Fatalf("runMigrations(ignored 31..32): %v", err)
	}
	d.requireSteps(wispRestoreStepRecompute, wispIsBlockedIndexDDL, wispRestoreStepBody, wispRestoreStepCursor)
}

// TestIgnored32WithoutWispsIsANoOp leaves an absent wisps table to
// ignored/0001 and the cursor-reality repair, as the 7/15 repair does.
func TestIgnored32WithoutWispsIsANoOp(t *testing.T) {
	d := newWispRestoreDB(t)
	d.expectProbes(false, false, false)
	if _, err := d.migrate(); err != nil {
		t.Fatalf("runMigrations(ignored 31..32): %v", err)
	}
	d.requireSteps(wispRestoreStepBody, wispRestoreStepCursor)
}

// TestIgnored32HealthyCloneOnlyProbes is AC-4 at the runner: a healthy clone
// pays the three probes and nothing else (no DDL, no recompute), and once 32
// is recorded a converged pass runs nothing at all.
func TestIgnored32HealthyCloneOnlyProbes(t *testing.T) {
	d := newWispRestoreDB(t)
	d.expectProbes(true, true, true)
	if _, err := d.migrate(); err != nil {
		t.Fatalf("runMigrations(ignored 31..32): %v", err)
	}
	d.requireSteps(wispRestoreStepBody, wispRestoreStepCursor)

	d.steps = nil
	applied, err := runMigrations(context.Background(), d, ignoredSource, 32, 32, false)
	if err != nil || applied != 0 {
		t.Fatalf("converged runMigrations(ignored 32..32) = (%d, %v), want (0, nil)", applied, err)
	}
	d.requireSteps()
}

// wispDropIndexDDL drops a stray idx_wisps_is_blocked left on a clone whose
// column is gone.
const wispDropIndexDDL = "DROP INDEX idx_wisps_is_blocked ON wisps"

// requireFailedPass asserts one pass that stopped on cause: the error carries
// the migration and step context, and no cursor row was written for 32.
func (d *wispRestoreDB) requireFailedPass(applied int, err, cause error, stepContext string) {
	d.t.Helper()
	if !errors.Is(err, cause) {
		d.t.Fatalf("runMigrations error = %v, want it to wrap %v", err, cause)
	}
	for _, want := range []string{ignoredMigration0032Up, stepContext} {
		if !strings.Contains(err.Error(), want) {
			d.t.Errorf("runMigrations error %q lacks context %q", err, want)
		}
	}
	if applied != 0 {
		d.t.Errorf("applied = %d after a failed repair, want 0", applied)
	}
}

// AC-6: every failing step of the repair fails the pass with context and
// leaves 32 unrecorded, and the next pass, seeing whatever the failed one
// left, completes both the shape and the recompute.

func TestIgnored32FailedRecomputeStaysPending(t *testing.T) {
	d := newWispRestoreDB(t)
	cause := errors.New("recompute fault")
	d.recomputeErr = cause
	d.expectProbes(true, false, false)
	applied, err := d.migrate()
	d.requireFailedPass(applied, err, cause, "recomputing wisps.is_blocked")
	d.requireSteps(wispIsBlockedColumnDDL, wispRestoreStepRecompute)

	// The ALTER committed; the index is still missing, so the retry recomputes.
	d.steps = nil
	d.expectProbes(true, true, false)
	if _, err := d.migrate(); err != nil {
		t.Fatalf("retry runMigrations(ignored 31..32): %v", err)
	}
	d.requireSteps(wispRestoreStepRecompute, wispIsBlockedIndexDDL, wispRestoreStepBody, wispRestoreStepCursor)
}

func TestIgnored32FailedIndexCreateStaysPending(t *testing.T) {
	d := newWispRestoreDB(t)
	cause := errors.New("index fault")
	d.failOn = map[string]error{wispIsBlockedIndexDDL: cause}
	d.expectProbes(true, false, false)
	applied, err := d.migrate()
	d.requireFailedPass(applied, err, cause, "creating idx_wisps_is_blocked")
	d.requireSteps(wispIsBlockedColumnDDL, wispRestoreStepRecompute, wispIsBlockedIndexDDL)

	d.steps = nil
	d.expectProbes(true, true, false)
	if _, err := d.migrate(); err != nil {
		t.Fatalf("retry runMigrations(ignored 31..32): %v", err)
	}
	d.requireSteps(wispRestoreStepRecompute, wispIsBlockedIndexDDL, wispRestoreStepBody, wispRestoreStepCursor)
}

func TestIgnored32FailedCursorRecordStaysPending(t *testing.T) {
	d := newWispRestoreDB(t)
	cause := errors.New("cursor fault")
	d.failOn = map[string]error{wispRestoreStepCursor: cause}
	d.expectProbes(true, false, false)
	applied, err := d.migrate()
	d.requireFailedPass(applied, err, cause, "recording")
	d.requireSteps(wispIsBlockedColumnDDL, wispRestoreStepRecompute, wispIsBlockedIndexDDL,
		wispRestoreStepBody, wispRestoreStepCursor)

	// The repair finished, so the retry finds the clone healthy.
	d.steps = nil
	d.expectProbes(true, true, true)
	if _, err := d.migrate(); err != nil {
		t.Fatalf("retry runMigrations(ignored 31..32): %v", err)
	}
	d.requireSteps(wispRestoreStepBody, wispRestoreStepCursor)
}

// TestIgnored32StrayIndexWithoutColumnIsDroppedFirst covers a clone left with
// an idx_wisps_is_blocked but no column (a status-only index under the
// canonical name). Kept, it would make a pass that fails after the ALTER
// look converged to the retry, which would record 32 over DEFAULT 0 flags;
// dropped first, the retry still sees the index missing and recomputes.
func TestIgnored32StrayIndexWithoutColumnIsDroppedFirst(t *testing.T) {
	d := newWispRestoreDB(t)
	cause := errors.New("recompute fault")
	d.recomputeErr = cause
	d.expectProbes(true, false, true)
	applied, err := d.migrate()
	d.requireFailedPass(applied, err, cause, "recomputing wisps.is_blocked")
	d.requireSteps(wispDropIndexDDL, wispIsBlockedColumnDDL, wispRestoreStepRecompute)

	d.steps = nil
	d.expectProbes(true, true, false)
	if _, err := d.migrate(); err != nil {
		t.Fatalf("retry runMigrations(ignored 31..32): %v", err)
	}
	d.requireSteps(wispRestoreStepRecompute, wispIsBlockedIndexDDL, wispRestoreStepBody, wispRestoreStepCursor)
}

func TestIgnored32FailedStrayIndexDropStaysPending(t *testing.T) {
	d := newWispRestoreDB(t)
	cause := errors.New("drop fault")
	d.failOn = map[string]error{wispDropIndexDDL: cause}
	d.expectProbes(true, false, true)
	applied, err := d.migrate()
	d.requireFailedPass(applied, err, cause, "dropping idx_wisps_is_blocked")
	d.requireSteps(wispDropIndexDDL)
}
