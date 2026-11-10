package schema

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// driftBodyDB records every statement the migration runner executes and can
// fail the first one (the 0059 body, with the standard runner stubs active)
// the way a dolthub/dolt#11131-drifted table does: as a returned error — the
// server engine's recovered 1105, or the current pin's returned out-of-band
// address error in embedded mode.
type driftBodyDB struct {
	mockDB
	failFirst func(query string) error
	execLog   []string
}

func (d *driftBodyDB) ExecContext(_ context.Context, q string, _ ...any) (sql.Result, error) {
	d.execLog = append(d.execLog, q)
	if d.failFirst != nil {
		if err := d.failFirst(q); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// driftPhaseDB routes the runner's DBConn calls through optional hooks so a
// test can fail one chosen phase of a selected migration step: queryErr covers
// the dirty-table snapshot and the updated_at guard's probes, execErr the
// body, cursor row, and per-step-commit exits. Unset hooks keep mockDB's
// blind-success ExecContext; QueryContext stays loud (mockDB's panic) unless
// queryErr is set, so a test that reaches an unstubbed query fails visibly.
type driftPhaseDB struct {
	mockDB
	execErr  func(query string) error
	queryErr func(query string) error
	execLog  []string
}

func (d *driftPhaseDB) ExecContext(_ context.Context, q string, _ ...any) (sql.Result, error) {
	d.execLog = append(d.execLog, q)
	if d.execErr != nil {
		if err := d.execErr(q); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (d *driftPhaseDB) QueryContext(_ context.Context, q string, _ ...any) (*sql.Rows, error) {
	if d.queryErr != nil {
		return nil, d.queryErr(q)
	}
	return d.mockDB.QueryContext(nil, q) // panics "not called" like mockDB
}

// captureMigrationStderr routes the runner's progress lines into a buffer for
// one test, so a test can assert on what an operator would and would not see.
func captureMigrationStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := stderr
	stderr = &buf
	t.Cleanup(func() { stderr = orig })
	return &buf
}

// TestRunMigrationsRefusesEncodingDriftInMigrationBody is bd-mjbau8's primary
// red/green test (gastownhall/beads#5699): a numbered migration body that
// fails with the dolthub/dolt#11131 drift signature must come back as an
// actionable refusal naming the schema-encoding-drift class and the stopped
// migration — not the raw engine error string — with the pass stopping before
// that step's cursor row, success line, or any later migration runs. The
// error-form table also pins the recognized signatures: both hash widths, the
// bare and server-wrapped forms, the embedded out-of-band address form, case
// insensitivity, and a multi-line server stack that must not reach the
// operator.
func TestRunMigrationsRefusesEncodingDriftInMigrationBody(t *testing.T) {
	cases := []struct{ name, drift string }{
		{"server 1105 read form", "Error 1105 (HY000): panic recovered: invalid hash length: 19"},
		{"server 1105 write form", "Error 1105 (HY000): panic recovered: invalid hash length: 1"},
		{"bare read form", "invalid hash length: 19"},
		{"embedded out-of-band address form", "malformed out-of-band adaptive value: invalid address length"},
		{"uppercase form", "INVALID HASH LENGTH: 19"},
		{"multiline server stack", "Error 1105 (HY000): panic recovered: invalid hash length: 19\ngoroutine 1 [running]:\nsome.Frame(...)\n\t/very/long/path.go:1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			progress := captureMigrationStderr(t)
			zeroIssueRowCounter(t)
			stubUpdatedAtGuard(t)

			db := &driftBodyDB{failFirst: func(string) error { return errors.New(tc.drift) }}
			count, err := runMigrations(context.Background(), db, mainSource, 58, 60, false)
			if err == nil {
				t.Fatal("runMigrations over a drifted table returned nil")
			}
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "encoding drift") && !strings.Contains(msg, "11131") {
				t.Errorf("migration 0059 drift failure is not classified; want an actionable error naming the schema-encoding-drift class (dolthub/dolt#11131), got: %v", err)
			}
			if !strings.Contains(msg, "0059") {
				t.Errorf("refusal does not name the stopped migration, got: %v", err)
			}

			// Typed contract: the refusal names the selected step and its
			// cursor source, and preserves the cause for errors.Is/As.
			var typed *SchemaEncodingDriftError
			if !errors.As(err, &typed) {
				t.Fatalf("body drift is not classified: want *SchemaEncodingDriftError, got %T: %v", err, err)
			}
			if typed.Migration != "0059_recompute_null_gate_is_blocked.up.sql" || typed.Version != 59 || typed.CursorTable != "schema_migrations" {
				t.Errorf("typed refusal names the wrong step: %q v%d on %q", typed.Migration, typed.Version, typed.CursorTable)
			}
			if typed.Cause == nil || !strings.Contains(typed.Cause.Error(), strings.SplitN(tc.drift, "\n", 2)[0]) {
				t.Errorf("typed refusal lost the original cause: %v", typed.Cause)
			}
			if !errors.Is(err, typed.Cause) {
				t.Errorf("errors.Is(err, cause) = false; the wrap must preserve the chain: %v", err)
			}
			// The operator-facing symptom is the bounded single line without
			// the appended engine stack; Unwrap keeps the full original.
			if symptom := typed.Symptom(); strings.Contains(symptom, "\n") || strings.Contains(symptom, "goroutine") {
				t.Errorf("Symptom() is not a single line: %q", symptom)
			} else if !strings.Contains(strings.ToLower(symptom), "hash length") && !strings.Contains(strings.ToLower(symptom), "adaptive value") {
				t.Errorf("Symptom() does not carry the engine's drift line: %q", symptom)
			}
			// Baseline-compatible no-side-effect assertions: these hold at
			// the base commit too (the pass already stopped before the
			// cursor), so the classification above is what reddens the test.
			if count != 0 {
				t.Errorf("count = %d, want 0: the failed step must not count as applied", count)
			}
			if len(db.execLog) != 1 {
				t.Errorf("executed %d statements, want the pass to stop at the failing body (no cursor row for 0059, no 0060 body): %q", len(db.execLog), db.execLog)
			}
			for _, q := range db.execLog[1:] {
				if strings.Contains(q, "INSERT IGNORE INTO schema_migrations") {
					t.Errorf("cursor row written despite the failed body: %q", q)
				}
			}
			if strings.Contains(progress.String(), "done") {
				t.Errorf("success line printed for a failed step; progress: %q", progress.String())
			}
		})
	}
}

// TestRunMigrationsEncodingDriftStepPhases pins AC-2: the typed refusal is
// attached at the selected-step boundary, so a drift-signature error from ANY
// phase of the step — dirty-table snapshot, updated_at snapshot, pre-repair,
// body, timestamp restore, cursor row, per-step commit — identifies that step
// while the pass keeps its existing order and count semantics at each exit.
// The pre-repair case runs a real ignored-source migration from its actual
// list, so the cursor-source attribution is pinned on both sources.
func TestRunMigrationsEncodingDriftStepPhases(t *testing.T) {
	drift := func() error { return errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 19") }
	requireRefusal := func(t *testing.T, err error, wantMigration string, wantVersion int, wantCursorTable, wantCauseMarker string) *SchemaEncodingDriftError {
		t.Helper()
		if err == nil {
			t.Fatal("the drifted phase returned nil")
		}
		var typed *SchemaEncodingDriftError
		if !errors.As(err, &typed) {
			t.Fatalf("phase drift is not classified: want *SchemaEncodingDriftError, got %T: %v", err, err)
		}
		if typed.Migration != wantMigration || typed.Version != wantVersion || typed.CursorTable != wantCursorTable {
			t.Errorf("typed refusal names the wrong step: %q v%d on %q, want %q v%d on %q",
				typed.Migration, typed.Version, typed.CursorTable, wantMigration, wantVersion, wantCursorTable)
		}
		if typed.Cause == nil || !strings.Contains(typed.Cause.Error(), wantCauseMarker) {
			t.Errorf("typed refusal lost the phase's own error wrap; want %q in cause, got: %v", wantCauseMarker, typed.Cause)
		}
		return typed
	}

	t.Run("dirty-table snapshot exit", func(t *testing.T) {
		quietStderr(t)
		zeroIssueRowCounter(t)
		stubUpdatedAtGuard(t)
		db := &driftPhaseDB{queryErr: func(string) error { return drift() }}
		count, err := runMigrations(context.Background(), db, mainSource, 58, 60, true)
		requireRefusal(t, err, "0059_recompute_null_gate_is_blocked.up.sql", 59, "schema_migrations", "snapshotting dirty tables before")
		if count != 0 {
			t.Errorf("count = %d, want 0", count)
		}
		if len(db.execLog) != 0 {
			t.Errorf("body ran despite the failed snapshot: %q", db.execLog)
		}
	})

	t.Run("updated_at snapshot exit", func(t *testing.T) {
		quietStderr(t)
		zeroIssueRowCounter(t)
		orig := snapshotUpdatedAtForStep
		snapshotUpdatedAtForStep = func(context.Context, DBConn, migrationSource, int) (*updatedAtSnapshot, error) {
			return nil, drift()
		}
		t.Cleanup(func() { snapshotUpdatedAtForStep = orig })
		db := &driftPhaseDB{}
		count, err := runMigrations(context.Background(), db, mainSource, 58, 60, false)
		requireRefusal(t, err, "0059_recompute_null_gate_is_blocked.up.sql", 59, "schema_migrations", "snapshotting updated_at before migration")
		if count != 0 {
			t.Errorf("count = %d, want 0", count)
		}
	})

	t.Run("pre-repair exit on the ignored source", func(t *testing.T) {
		quietStderr(t)
		stubUpdatedAtGuard(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		// ensureWispIsBlockedForRecompute's wisps probe fails with the drift
		// signature; the expectations' order (probe and nothing else) is the
		// ordering assertion for this phase.
		mock.ExpectQuery(`(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`).
			WithArgs("wisps").WillReturnError(drift())
		count, err := runMigrations(context.Background(), db, ignoredSource, 6, 7, false)
		requireRefusal(t, err, "0007_recompute_wisp_is_blocked.up.sql", 7, "ignored_schema_migrations", "pre-repair for migration")
		if count != 0 {
			t.Errorf("count = %d, want 0", count)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
	})

	t.Run("updated_at restore exit", func(t *testing.T) {
		quietStderr(t)
		zeroIssueRowCounter(t)
		orig := snapshotUpdatedAtForStep
		snapshotUpdatedAtForStep = func(context.Context, DBConn, migrationSource, int) (*updatedAtSnapshot, error) {
			// A snapshot with one restore target; its first restore probe
			// (guardColumnsOf -> showTableExists) fails via queryErr below.
			return &updatedAtSnapshot{spec: updatedAtGuardSpec{restore: []string{"issues"}}}, nil
		}
		t.Cleanup(func() { snapshotUpdatedAtForStep = orig })
		db := &driftPhaseDB{queryErr: func(string) error { return drift() }}
		count, err := runMigrations(context.Background(), db, mainSource, 58, 60, false)
		requireRefusal(t, err, "0059_recompute_null_gate_is_blocked.up.sql", 59, "schema_migrations", "restoring updated_at after migration")
		if count != 0 {
			t.Errorf("count = %d, want 0: the cursor row must not record a step whose restore failed", count)
		}
		// The body ran, the cursor row did not.
		if len(db.execLog) != 1 {
			t.Errorf("executed %d statements, want just the 0059 body: %q", len(db.execLog), db.execLog)
		}
	})

	t.Run("cursor-row exit", func(t *testing.T) {
		quietStderr(t)
		zeroIssueRowCounter(t)
		stubUpdatedAtGuard(t)
		db := &driftPhaseDB{execErr: func(q string) error {
			if strings.Contains(q, "INSERT IGNORE INTO schema_migrations") {
				return drift()
			}
			return nil
		}}
		count, err := runMigrations(context.Background(), db, mainSource, 58, 60, false)
		requireRefusal(t, err, "0059_recompute_null_gate_is_blocked.up.sql", 59, "schema_migrations", "recording 0059_recompute_null_gate_is_blocked.up.sql in schema_migrations")
		if count != 0 {
			t.Errorf("count = %d, want 0: a failed cursor write must not count as applied", count)
		}
		// 0060 never ran.
		if len(db.execLog) != 2 {
			t.Errorf("executed %d statements, want body + cursor attempt only: %q", len(db.execLog), db.execLog)
		}
	})

	t.Run("per-step commit exit", func(t *testing.T) {
		quietStderr(t)
		zeroIssueRowCounter(t)
		stubUpdatedAtGuard(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		emptyStatus := sqlmock.NewRows([]string{"table_name", "staged"})
		mock.ExpectQuery("dolt_status").WillReturnRows(emptyStatus) // snapshot before the step
		mock.ExpectExec("UPDATE issues SET is_blocked = 0").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("INSERT IGNORE INTO schema_migrations").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("dolt_status").WillReturnRows(emptyStatus) // snapshot inside commitMigrationStep
		mock.ExpectQuery("CALL DOLT_ADD").WillReturnError(drift())  // the per-step add fails
		count, err := runMigrations(context.Background(), db, mainSource, 58, 60, true)
		typed := requireRefusal(t, err, "0059_recompute_null_gate_is_blocked.up.sql", 59, "schema_migrations", "committing migration")
		// The count keeps its existing commit-phase semantics: the cursor row
		// was recorded before the commit failed, so the step counts as
		// applied — and the refusal must not promise the cursor stayed
		// unwritten the way a body failure's ordering does.
		if count != 1 {
			t.Errorf("count = %d, want 1: the cursor row was recorded before the commit exit", count)
		}
		if strings.Contains(typed.UserMessage(), "cursor") {
			t.Errorf("the commit-phase refusal promises cursor state it cannot know: %q", typed.UserMessage())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
	})

	t.Run("already-typed error is not double-wrapped", func(t *testing.T) {
		quietStderr(t)
		zeroIssueRowCounter(t)
		stubUpdatedAtGuard(t)
		pre := &SchemaEncodingDriftError{
			Migration: "0059_recompute_null_gate_is_blocked.up.sql",
			Version:   59, CursorTable: "schema_migrations",
			Cause: errors.New("invalid hash length: 19"),
		}
		db := &driftPhaseDB{execErr: func(string) error { return fmt.Errorf("body: %w", pre) }}
		_, err := runMigrations(context.Background(), db, mainSource, 58, 60, false)
		if err == nil {
			t.Fatal("the already-typed body error was swallowed")
		}
		var typed *SchemaEncodingDriftError
		if !errors.As(err, &typed) {
			t.Fatalf("typed refusal no longer reachable through the step wrap: %v", err)
		}
		if typed != pre {
			t.Errorf("the boundary re-wrapped an already-typed refusal; want the original instance, got: %v", typed)
		}
	})

	t.Run("drift-shaped error before step selection is not turned into a refusal", func(t *testing.T) {
		quietStderr(t)
		stubUpdatedAtGuard(t)
		orig := issueRowCounter
		issueRowCounter = func(context.Context, DBConn) (int64, error) {
			return 0, drift()
		}
		t.Cleanup(func() { issueRowCounter = orig })
		db := &driftPhaseDB{}
		count, err := runMigrations(context.Background(), db, mainSource, 58, 60, false)
		if err != nil {
			t.Fatalf("a drift-shaped error before step selection must keep its existing handling (the large-rig notice ignores it), got: %v", err)
		}
		if count != 2 {
			t.Errorf("count = %d, want 2: the pass must still apply 0059 and 0060", count)
		}
	})
}
