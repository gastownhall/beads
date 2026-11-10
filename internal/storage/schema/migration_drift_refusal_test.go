package schema

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

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
