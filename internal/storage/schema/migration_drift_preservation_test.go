package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// driftPanicDB fails the first statement the way the embedded driver's raw
// drift panic does: as a Go panic on the calling goroutine, not a returned
// error. The migration runner deliberately installs no recover seam for it
// (gastownhall/beads#5699 keeps that a residual driver limitation), so it must
// reach this test's recover verbatim.
type driftPanicDB struct {
	mockDB
	panicMsg any
}

func (d *driftPanicDB) ExecContext(_ context.Context, _ string, _ ...any) (sql.Result, error) {
	panic(d.panicMsg)
}

// TestMigrationDriftClassificationPreservesOtherFailures pins the other side
// of bd-mjbau8's classification boundary (gastownhall/beads#5699): the drift
// refusal is a narrow attachment around the selected step, so a healthy pass
// keeps its count, order, and success line; unrelated migration failures —
// duplicate key, lock wait timeout, a server-recovered panic with some other
// cause — keep the generic migration-error handling; a RAW engine panic stays
// a raw panic (returned errors are converted, panics are not); and the
// auxiliary re-key's classifier keeps its pre-existing, narrower signature
// set. The drift control at the top is the subtest that reddens this test at
// the base commit; every other subtest pins baseline behavior that must hold
// on both sides of the change.
func TestMigrationDriftClassificationPreservesOtherFailures(t *testing.T) {
	t.Run("drift-signature body failure is the classified control", func(t *testing.T) {
		quietStderr(t)
		zeroIssueRowCounter(t)
		stubUpdatedAtGuard(t)
		db := &driftBodyDB{failFirst: func(string) error { return errors.New("invalid hash length: 19") }}
		count, err := runMigrations(context.Background(), db, mainSource, 58, 60, false)
		if err == nil {
			t.Fatal("the drift body failure returned nil")
		}
		var typed *SchemaEncodingDriftError
		if !errors.As(err, &typed) {
			t.Fatalf("the control case is not classified: every other subtest here pins what the boundary must NOT swallow, so the drift signature must convert to the typed refusal, got %T: %v", err, err)
		}
		if count != 0 {
			t.Errorf("count = %d, want 0: a refused step must not count as applied", count)
		}
	})

	t.Run("healthy pass keeps its count, order, and success line", func(t *testing.T) {
		progress := captureMigrationStderr(t)
		zeroIssueRowCounter(t)
		stubUpdatedAtGuard(t)
		db := &driftBodyDB{}
		count, err := runMigrations(context.Background(), db, mainSource, 58, 60, false)
		if err != nil {
			t.Fatalf("healthy 58->60 pass failed: %v", err)
		}
		if count != 2 {
			t.Errorf("count = %d, want 2 (0059 and 0060)", count)
		}
		if len(db.execLog) != 4 {
			t.Fatalf("executed %d statements, want body + cursor row per step: %q", len(db.execLog), db.execLog)
		}
		if !strings.Contains(db.execLog[0], "__bd_0059") {
			t.Errorf("first statement is not the 0059 body: %q", db.execLog[0])
		}
		if !strings.Contains(db.execLog[1], "INSERT IGNORE INTO schema_migrations") || !strings.Contains(db.execLog[3], "INSERT IGNORE INTO schema_migrations") {
			t.Errorf("cursor rows out of order between the bodies: %q", db.execLog)
		}
		if !strings.Contains(db.execLog[2], "storage_class") {
			t.Errorf("third statement is not the 0060 body: %q", db.execLog[2])
		}
		if !strings.Contains(progress.String(), "done") {
			t.Errorf("the per-step success line was lost; progress: %q", progress.String())
		}
	})

	t.Run("unrelated migration failures keep the generic handling", func(t *testing.T) {
		cases := []struct{ name, failure string }{
			{"duplicate key", "Error 1062 (HY000): Duplicate entry '42' for key 'issues.PRIMARY'"},
			{"lock wait timeout", "Error 1205 (HY000): Lock wait timeout exceeded; try restarting transaction"},
			{"recovered panic with another cause", "Error 1105 (HY000): panic recovered: runtime error: index out of range [7]"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				quietStderr(t)
				zeroIssueRowCounter(t)
				stubUpdatedAtGuard(t)
				db := &driftBodyDB{failFirst: func(string) error { return errors.New(tc.failure) }}
				count, err := runMigrations(context.Background(), db, mainSource, 58, 60, false)
				if err == nil {
					t.Fatal("the body failure returned nil")
				}
				var typed *SchemaEncodingDriftError
				if errors.As(err, &typed) {
					t.Errorf("an unrelated failure was wrapped as drift: %v", err)
				}
				if want := fmt.Sprintf("migration 0059_recompute_null_gate_is_blocked.up.sql: %s", tc.failure); err.Error() != want {
					t.Errorf("generic body wrap changed; want %q, got %q", want, err.Error())
				}
				if count != 0 {
					t.Errorf("count = %d, want 0", count)
				}
				if len(db.execLog) != 1 {
					t.Errorf("executed %d statements, want the pass to stop at the failing body (no cursor row, no 0060): %q", len(db.execLog), db.execLog)
				}
			})
		}
	})

	t.Run("raw engine panic propagates un-intercepted", func(t *testing.T) {
		quietStderr(t)
		zeroIssueRowCounter(t)
		stubUpdatedAtGuard(t)
		db := &driftPanicDB{panicMsg: "invalid hash length: 19"}
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			_, _ = runMigrations(context.Background(), db, mainSource, 58, 60, false)
		}()
		if recovered != "invalid hash length: 19" {
			t.Fatalf("the raw panic was swallowed or transformed by the migration runner; want the sentinel recovered verbatim, got: %v", recovered)
		}
	})

	t.Run("classifier scope stays pinned", func(t *testing.T) {
		drift := errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 19")
		other := errors.New("Error 1105 (HY000): panic recovered: runtime error: index out of range [7]")
		if !isMigrationEncodingDriftErr(drift) {
			t.Error("isMigrationEncodingDriftErr does not recognize the drift signature")
		}
		if isMigrationEncodingDriftErr(other) {
			t.Error("isMigrationEncodingDriftErr matched an unrelated recovered panic; the 1105 envelope alone must not classify")
		}
		// The auxiliary re-key's classifier keeps its pre-existing, narrower
		// signature set (only "invalid hash length"): widening it to the
		// returned out-of-band form would change the re-key's skip-and-record
		// policy, a decision this issue does not make.
		if !isSchemaEncodingDriftErr(errors.New("invalid hash length: 19")) {
			t.Error("the auxiliary re-key classifier lost its original signature")
		}
		if isSchemaEncodingDriftErr(errors.New("malformed out-of-band adaptive value: invalid address length")) {
			t.Error("the auxiliary re-key classifier was widened beyond its pre-existing signature; that policy change needs its own review")
		}
	})
}
