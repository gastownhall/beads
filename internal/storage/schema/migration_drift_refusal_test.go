package schema

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
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
