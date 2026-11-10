package schema

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/DATA-DOG/go-sqlmock"
)

const (
	migration0059 = "0059_recompute_null_gate_is_blocked.up.sql"
	migration0041 = "0041_split_dependencies_target.up.sql"
	// driftRecoveryURL is spelled out rather than read from the package
	// constant so the refusal's runbook link is pinned by the test itself.
	driftRecoveryURL = "https://beads.gascity.com/recovery/schema-encoding-drift"
)

// migrationBody returns the shipped SQL of one main-source migration, which is
// exactly the text the runner hands to ExecContext or DrainCall.
func migrationBody(t *testing.T, name string) string {
	t.Helper()
	data, err := mainSource.files.ReadFile(mainSource.dir + "/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// driftSignatures are the returned-error forms a migration body can hit on a
// dolthub/dolt#11131-drifted table: the server's recovered hash panic in both
// widths migration 0057 records, and the adaptive-value decode error the
// pinned Dolt returns instead of panicking.
var driftSignatures = []struct {
	name string
	err  error
}{
	{"server 1105 read width", errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 19")},
	{"server 1105 insert/merge width", errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 1")},
	{"bare hash length", errors.New("invalid hash length: 19")},
	{"mixed case", errors.New("Error 1105 (HY000): Panic Recovered: Invalid Hash Length: 19")},
	{"adaptive invalid address", errors.New("malformed out-of-band adaptive value: invalid address length 3")},
	{"adaptive truncated varint", errors.New("Error 1105 (HY000): malformed out-of-band adaptive value: truncated length varint")},
	{"multiline server stack", errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 19\ngoroutine 81 [running]:\nruntime/debug.Stack()\n\t/usr/local/go/src/runtime/debug/stack.go:26 +0x5e")},
	{"control characters", errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 19\x1b[31m\x00\x07")},
}

// userMessager is the presentation surface the CLI renders a typed migration
// refusal through; asserting it by interface keeps the red run compiling
// against a base that has no refusal type at all.
type userMessager interface {
	error
	UserMessage() string
}

// bd-mjbau8 / gastownhall/beads#5699: a pre-0059 cursor over a drifted table
// fails inside migration 0059's INSERT ... SELECT, before the v53 aux re-key's
// drift guard is ever reached. The runner must stop there with an actionable,
// typed refusal that names the migration and its cursor, keeps the engine's
// error as its cause, and does none of the step's later work: no cursor row,
// no step commit, no "done" line, and no 0060.
func TestRunMigrationsEncodingDriftRefuses(t *testing.T) {
	for _, tc := range driftSignatures {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			orig := stderr
			stderr = &out
			t.Cleanup(func() { stderr = orig })
			zeroIssueRowCounter(t)
			stubUpdatedAtGuard(t)

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			// sqlmock matches in order and refuses anything unexpected, so the
			// expectation list is the whole permitted statement sequence.
			mock.ExpectQuery(guardDirtyStatusQuery).
				WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}))
			mock.ExpectExec(regexp.QuoteMeta(migrationBody(t, migration0059))).
				WillReturnError(tc.err)

			applied, err := runMigrations(context.Background(), db, mainSource, 58, 60, true)

			assertDriftRefusal(t, err, tc.err, migration0059, 59, "schema_migrations")
			if applied != 0 {
				t.Errorf("applied = %d, want 0: the drifted step must not count", applied)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
			progress := out.String()
			if !strings.Contains(progress, "Applying migration 0059") {
				t.Errorf("progress never announced 0059:\n%s", progress)
			}
			if strings.Contains(progress, "done (") {
				t.Errorf("a refused step printed its success line:\n%s", progress)
			}
			if strings.Contains(progress, "0060") {
				t.Errorf("the pass went on to 0060 after refusing 0059:\n%s", progress)
			}
		})
	}
}

// TestRunMigrationsEncodingDriftRefusesDrainedBody covers the other body path:
// procedure bodies (0040/0041 CALL DOLT_COMMIT) go through DrainCall, whose
// error comes back from QueryContext rather than ExecContext.
func TestRunMigrationsEncodingDriftRefusesDrainedBody(t *testing.T) {
	quietStderr(t)
	stubUpdatedAtGuard(t)
	zeroIssueRowCounter(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	// The 0041 pre-repair finds the frozen nonlocal rows present and no-ops.
	mock.ExpectQuery(`dolt_nonlocal_tables`).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(4))
	cause := errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 1")
	mock.ExpectQuery(regexp.QuoteMeta(migrationBody(t, migration0041))).WillReturnError(cause)

	applied, err := runMigrations(context.Background(), db, mainSource, 40, 41, false)

	assertDriftRefusal(t, err, cause, migration0041, 41, "schema_migrations")
	if applied != 0 {
		t.Errorf("applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// assertDriftRefusal checks the refusal contract shared by every phase: a
// typed, user-presentable error naming the step and its cursor source, with
// the engine's error preserved as its cause and kept out of the summary.
func assertDriftRefusal(t *testing.T, err, cause error, migration string, version int, cursorTable string) {
	t.Helper()
	if err == nil {
		t.Fatal("runMigrations over a drifted table returned nil")
	}
	if !errors.Is(err, cause) {
		t.Errorf("refusal dropped the engine's error from its chain: %v", err)
	}
	var refusal userMessager
	if !errors.As(err, &refusal) {
		t.Fatalf("migration drift failure is not a typed refusal (no UserMessage in the chain); got %T: %v", err, err)
	}
	if got := fmt.Sprintf("%T", refusal); got != "*schema.SchemaEncodingDriftError" {
		t.Errorf("refusal type = %s, want *schema.SchemaEncodingDriftError", got)
	}
	summary := refusal.Error()
	for _, want := range []string{
		migration,
		cursorTable,
		fmt.Sprintf("v%d", version),
		"schema-encoding drift",
		"dolthub/dolt#11131",
		"partial changes may remain",
		driftRecoveryURL,
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("refusal summary lacks %q: %s", want, summary)
		}
	}
	for _, s := range []string{summary, refusal.UserMessage()} {
		if strings.Contains(s, "goroutine") || strings.Contains(s, "debug.Stack") {
			t.Errorf("refusal carries the engine stack: %q", s)
		}
		if strings.ContainsAny(s, "\x00\x07\x1b") {
			t.Errorf("refusal carries control characters: %q", s)
		}
	}
	if strings.Contains(summary, "\n") {
		t.Errorf("refusal summary is not one line: %q", summary)
	}
	if msg := refusal.UserMessage(); !strings.Contains(msg, driftRecoveryURL) || !strings.Contains(msg, migration) {
		t.Errorf("UserMessage lacks the migration or recovery URL:\n%s", msg)
	}
}

// TestSchemaEncodingDriftSymptom: the quoted symptom is chosen on the
// original text before anything is stripped or cut, so the signature survives
// a non-ASCII wrapper line ahead of it (Unicode lowercasing changes the Kelvin
// sign's byte length) and a long prefix on its own line, while C0/C1 controls
// and the appended stack never reach the output and its length stays bounded.
func TestSchemaEncodingDriftSymptom(t *testing.T) {
	for _, tc := range []struct {
		name, msg, want string
	}{
		{"kelvin sign on an earlier line",
			"wrapping store open at 300\u212A: retry\nError 1105 (HY000): panic recovered: Invalid Hash Length: 19\ngoroutine 81 [running]:",
			"Invalid Hash Length: 19"},
		{"long wrapper prefix",
			"migration 0059: " + strings.Repeat("opening table issues; ", 20) + "malformed out-of-band adaptive value: invalid address length 3",
			"malformed out-of-band adaptive value: invalid address length 3"},
		{"C1 controls",
			"Error 1105 (HY000): panic recovered: invalid hash length:\u0085 1\u009b\u0080\x1b\x07",
			"invalid hash length: 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New(tc.msg)
			if !isMigrationEncodingDriftErr(cause) {
				t.Fatalf("signature not recognized in %q", tc.msg)
			}
			refusal := &SchemaEncodingDriftError{Migration: migration0059, Version: 59, CursorTable: "schema_migrations", Cause: cause}
			if got := refusal.Symptom(); got != tc.want {
				t.Errorf("Symptom() = %q, want %q", got, tc.want)
			}
			for _, s := range []string{refusal.Error(), refusal.UserMessage()} {
				for _, r := range s {
					if r != '\n' && unicode.IsControl(r) {
						t.Errorf("refusal carries control character %U: %q", r, s)
					}
				}
				if strings.Contains(s, "goroutine") {
					t.Errorf("refusal carries the engine stack: %q", s)
				}
			}
		})
	}

	t.Run("bounded", func(t *testing.T) {
		refusal := &SchemaEncodingDriftError{Cause: errors.New("invalid hash length: 19 " + strings.Repeat("ž", 1000))}
		got := refusal.Symptom()
		if !strings.HasPrefix(got, "invalid hash length: 19") {
			t.Errorf("Symptom() lost the signature: %q", got)
		}
		if n := utf8.RuneCountInString(got); n > maxDriftSymptomRunes+1 {
			t.Errorf("Symptom() is %d runes, want at most %d", n, maxDriftSymptomRunes+1)
		}
	})
}

// TestRunMigrationsEncodingDriftStepPhases: the selected step's other exits
// carry the same typed refusal, attributed to that step, and the runner's
// order and count are unchanged at each one. A cursor- or commit-phase failure
// follows a body that succeeded, so it makes no promise about the cursor.
func TestRunMigrationsEncodingDriftStepPhases(t *testing.T) {
	cause := errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 19")
	emptyStatus := func() *sqlmock.Rows { return sqlmock.NewRows([]string{"table_name", "staged"}) }
	ok := sqlmock.NewResult(0, 0)
	stubbedGuard := func(snap *updatedAtSnapshot, err error) func(t *testing.T) {
		return func(t *testing.T) {
			orig := snapshotUpdatedAtForStep
			snapshotUpdatedAtForStep = func(context.Context, DBConn, migrationSource, int) (*updatedAtSnapshot, error) {
				return snap, err
			}
			t.Cleanup(func() { snapshotUpdatedAtForStep = orig })
		}
	}

	for _, tc := range []struct {
		name           string
		src            migrationSource
		from, to       int
		commitEachStep bool
		guard          func(t *testing.T)
		expect         func(mock sqlmock.Sqlmock)
		migration      string
		version        int
		wantApplied    int
		wantDone       bool
	}{
		{
			name: "dirty-table snapshot", src: mainSource, from: 58, to: 60, commitEachStep: true,
			guard: stubUpdatedAtGuard,
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(guardDirtyStatusQuery).WillReturnError(cause)
			},
			migration: migration0059, version: 59,
		},
		{
			name: "updated_at snapshot", src: mainSource, from: 58, to: 60, commitEachStep: false,
			guard:     stubbedGuard(nil, fmt.Errorf("reading issues: %w", cause)),
			expect:    func(sqlmock.Sqlmock) {},
			migration: migration0059, version: 59,
		},
		{
			// A real ignored-source step with a registered pre-repair: the
			// refusal names that source's own cursor.
			name: "pre-repair on the ignored source", src: ignoredSource, from: 6, to: 8, commitEachStep: false,
			guard: stubUpdatedAtGuard,
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(`.`).WillReturnError(cause)
			},
			migration: "0007_recompute_wisp_is_blocked.up.sql", version: 7,
		},
		{
			name: "updated_at restore", src: mainSource, from: 58, to: 60, commitEachStep: false,
			guard: stubbedGuard(&updatedAtSnapshot{spec: updatedAtGuardSpec{restore: []string{"issues"}}}, nil),
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta(migrationBody(t, migration0059))).WillReturnResult(ok)
				mock.ExpectQuery(`.`).WillReturnError(cause)
			},
			migration: migration0059, version: 59,
		},
		{
			name: "cursor write", src: mainSource, from: 58, to: 60, commitEachStep: false,
			guard: stubUpdatedAtGuard,
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec(regexp.QuoteMeta(migrationBody(t, migration0059))).WillReturnResult(ok)
				mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(59, sqlmock.AnyArg()).WillReturnError(cause)
			},
			migration: migration0059, version: 59,
		},
		{
			// The cursor row is written and counted before the step commit, as
			// today; the refusal keeps that count.
			name: "step commit", src: mainSource, from: 58, to: 60, commitEachStep: true,
			guard: stubUpdatedAtGuard,
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(guardDirtyStatusQuery).WillReturnRows(emptyStatus())
				mock.ExpectExec(regexp.QuoteMeta(migrationBody(t, migration0059))).WillReturnResult(ok)
				mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(59, sqlmock.AnyArg()).WillReturnResult(ok)
				mock.ExpectQuery(guardDirtyStatusQuery).
					WillReturnRows(emptyStatus().AddRow("schema_migrations", false))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('-f', ?)")).WithArgs("schema_migrations").WillReturnError(cause)
			},
			migration: migration0059, version: 59, wantApplied: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			orig := stderr
			stderr = &out
			t.Cleanup(func() { stderr = orig })
			zeroIssueRowCounter(t)
			tc.guard(t)
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			tc.expect(mock)

			applied, err := runMigrations(context.Background(), db, tc.src, tc.from, tc.to, tc.commitEachStep)

			assertDriftRefusal(t, err, cause, tc.migration, tc.version, tc.src.cursorTable)
			if applied != tc.wantApplied {
				t.Errorf("applied = %d, want %d (count semantics changed)", applied, tc.wantApplied)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
			if strings.Contains(out.String(), "done (") {
				t.Errorf("a refused step printed its success line:\n%s", out.String())
			}
			if next := fmt.Sprintf("Applying migration %04d", tc.version+1); strings.Contains(out.String(), next) {
				t.Errorf("the pass went on past the refused step:\n%s", out.String())
			}
		})
	}
}

// TestRunMigrationsEncodingDriftAttributionBounds: the refusal never wraps
// itself twice, and an error from outside a selected step (here the per-step
// fault hook, which runs after the step completed) is not attributed to a
// migration it did not come from.
func TestRunMigrationsEncodingDriftAttributionBounds(t *testing.T) {
	cause := errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 19")

	t.Run("already typed", func(t *testing.T) {
		quietStderr(t)
		zeroIssueRowCounter(t)
		stubUpdatedAtGuard(t)
		inner := &SchemaEncodingDriftError{Migration: "0007_recompute_wisp_is_blocked.up.sql", Version: 7, CursorTable: "ignored_schema_migrations", Cause: cause}
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectExec(regexp.QuoteMeta(migrationBody(t, migration0059))).WillReturnError(fmt.Errorf("nested: %w", inner))

		_, err = runMigrations(context.Background(), db, mainSource, 58, 60, false)

		var got *SchemaEncodingDriftError
		if !errors.As(err, &got) {
			t.Fatalf("typed refusal lost: %v", err)
		}
		if got != inner {
			t.Errorf("refusal re-wrapped: outermost is %+v, want the original %+v", got, inner)
		}
	})

	t.Run("after the step completed", func(t *testing.T) {
		quietStderr(t)
		zeroIssueRowCounter(t)
		stubUpdatedAtGuard(t)
		hookErr := fmt.Errorf("simulated kill: %w", cause)
		t.Cleanup(SetMigrateStepFaultHookForTest(func(context.Context, DBConn, int) error { return hookErr }))
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectExec(regexp.QuoteMeta(migrationBody(t, migration0059))).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WillReturnResult(sqlmock.NewResult(0, 0))

		applied, err := runMigrations(context.Background(), db, mainSource, 58, 60, false)

		if err != hookErr {
			t.Errorf("err = %v (%T), want the hook's error unchanged", err, err)
		}
		if applied != 1 {
			t.Errorf("applied = %d, want 1", applied)
		}
	})

	t.Run("before step selection", func(t *testing.T) {
		quietStderr(t)
		stubUpdatedAtGuard(t)
		// The large-rig notice's row count runs before any step is
		// selected; its existing handling ignores the error, so a
		// drift-shaped failure there is neither a refusal nor a stop.
		orig := issueRowCounter
		issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, cause }
		t.Cleanup(func() { issueRowCounter = orig })
		ok := sqlmock.NewResult(0, 0)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectExec(regexp.QuoteMeta(migrationBody(t, migration0059))).WillReturnResult(ok)
		mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(59, sqlmock.AnyArg()).WillReturnResult(ok)
		mock.ExpectExec(regexp.QuoteMeta(migrationBody(t, "0060_add_storage_class.up.sql"))).WillReturnResult(ok)
		mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(60, sqlmock.AnyArg()).WillReturnResult(ok)

		applied, err := runMigrations(context.Background(), db, mainSource, 58, 60, false)

		if err != nil {
			t.Fatalf("a drift-shaped error before step selection must keep its existing handling, got: %v", err)
		}
		if applied != 2 {
			t.Errorf("applied = %d, want 2: the pass must still apply 0059 and 0060", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}
