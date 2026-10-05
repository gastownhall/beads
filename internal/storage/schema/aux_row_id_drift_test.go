package schema

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

var eventsTable = auxRekeyTables[0]

// driftErr is the error a drifted table returns: dolt recovers the decode panic
// per query and surfaces it as a 1105.
var driftErr = errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 19")

func expectEventsSelect(mock sqlmock.Sqlmock) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(regexp.QuoteMeta(
		fmt.Sprintf("SELECT id, %s FROM %s", eventsTable.columns, eventsTable.name)))
}

// captureLog redirects the standard logger, which is where the drift notices
// go: the package's own stderr writer is TTY-gated to io.Discard, and these are
// data-integrity warnings that a piped or CI caller must still see.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	origOut, origFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(origOut); log.SetFlags(origFlags) })
	return &buf
}

// The re-key tolerates exactly the dolthub/dolt#11131 schema-encoding-drift
// signature and nothing else: every other failure must still abort the pass.
func TestIsSchemaEncodingDriftErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"server 1105 drift panic", driftErr, true},
		{
			"wrapped drift panic",
			fmt.Errorf("scan rows of events: %w", driftErr),
			true,
		},
		{
			// Migration 0057 records this width for the opposite tag flip.
			"insert/merge side of the flip",
			errors.New("Error 1105 (HY000): panic recovered: invalid hash length: 1"),
			true,
		},
		{
			"unwrapped panic text",
			errors.New("panic: invalid hash length: 19"),
			true,
		},
		{"plain sql error", errors.New("Error 1062 (23000): duplicate entry"), false},
		{"connection drop", errors.New("invalid connection"), false},
		{"unrelated panic", errors.New("Error 1105 (HY000): panic recovered: index out of range"), false},
		{"lock timeout", errors.New("Error 1205 (HY000): lock wait timeout exceeded"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSchemaEncodingDriftErr(c.err); got != c.want {
				t.Fatalf("isSchemaEncodingDriftErr(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestRekeyAuxRowIDsSkipsDriftedTable covers the #4380 unblock: a table whose
// rows Dolt cannot decode is skipped so the rest of the migration pass
// completes, instead of aborting it — which left affected databases unopenable,
// because the pass is re-attempted on every open. The skipped table is recorded
// so a later pass can finish the job, and the in-progress sentinel is cleared
// like any completed pass: it means "a rewrite is in flight", and MigrateUp
// relaxes its dirty-table guards while it is set.
func TestRekeyAuxRowIDsSkipsDriftedTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	logged := captureLog(t)

	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		"version", auxRekeyPassInitial.markerVersion-1)
	expectIgnoredSentinelProbes(mock, true)
	expectAuxRekeyState(mock, false)
	expectSetAuxRekeySentinel(mock)
	// events carries the drift: its scan panics server-side.
	expectColumnExists(mock, true)
	expectEventsSelect(mock).WillReturnError(driftErr)
	// The remaining three tables still run.
	for range auxRekeyTables[1:] {
		expectColumnExists(mock, false)
	}
	expectSetAuxRekeyDrifted(mock, "events")
	expectClearAuxRekeySentinel(mock)

	if _, err := rekeyAuxRowIDs(context.Background(), db, auxRekeyPassInitial.shippedMainVersion-1, auxRekeyPassInitial); err != nil {
		t.Fatalf("drift must not abort the pass: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
	if got := logged.String(); !strings.Contains(got, `skipped "events"`) ||
		!strings.Contains(got, "may duplicate those rows") {
		t.Errorf("log = %q\nwant a skip warning naming events and its merge consequence", got)
	}
}

// TestRekeyAuxRowIDsSkipsDriftDuringRewrite covers the other half of the drift
// surface: #4380 reports that UPDATE of an affected row panics just like a read,
// so the drift can land mid-rewrite, after some ids have already changed. The
// half-re-keyed table must be recorded and committed rather than failing the
// pass — it stays resumable because a row already holding one of its group's
// target ids keeps it, so a later attempt never swaps ids within a group.
func TestRekeyAuxRowIDsSkipsDriftDuringRewrite(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	captureLog(t)

	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		"version", auxRekeyPassInitial.markerVersion-1)
	expectIgnoredSentinelProbes(mock, true)
	expectAuxRekeyState(mock, false)
	expectSetAuxRekeySentinel(mock)
	expectColumnExists(mock, true)
	// The scan succeeds — these are the readable-but-drifted rows — and the
	// rewrite panics instead.
	expectEventsSelect(mock).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "issue_id", "event_type", "actor", "old_value", "new_value", "comment", "created_at",
		}).AddRow("random-a", "bd-1", "status", "steve", "open", "closed", "", "2026-06-09 12:00:00"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE events SET id = ? WHERE id = ?")).
		WithArgs(sqlmock.AnyArg(), "random-a").
		WillReturnError(driftErr)
	for range auxRekeyTables[1:] {
		expectColumnExists(mock, false)
	}
	expectSetAuxRekeyDrifted(mock, "events")
	expectClearAuxRekeySentinel(mock)

	wrote, err := rekeyAuxRowIDs(context.Background(), db, auxRekeyPassInitial.shippedMainVersion-1, auxRekeyPassInitial)
	if err != nil {
		t.Fatalf("drift mid-rewrite must not abort the pass: %v", err)
	}
	if !wrote {
		t.Error("expected wrote=true so MigrateUp commits the partially re-keyed table")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyAuxRowIDsAbortsOnNonDriftError: the tolerance is narrow. An ordinary
// table failure must still propagate and keep the sentinel, exactly as before
// #4380 — otherwise it would swallow real migration bugs.
func TestRekeyAuxRowIDsAbortsOnNonDriftError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	captureLog(t)

	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		"version", auxRekeyPassInitial.markerVersion-1)
	expectIgnoredSentinelProbes(mock, true)
	expectAuxRekeyState(mock, false)
	expectSetAuxRekeySentinel(mock)
	expectColumnExists(mock, true)
	expectEventsSelect(mock).WillReturnError(errors.New("Error 1062 (23000): duplicate entry"))

	if _, err := rekeyAuxRowIDs(context.Background(), db, auxRekeyPassInitial.shippedMainVersion-1, auxRekeyPassInitial); err == nil {
		t.Fatal("expected a non-drift table failure to propagate")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyAuxRowIDsResumesDriftedTableOnly is the counterpart to the skip. The
// pass that skipped completed, so MigrateUp recorded the clone-local marker and
// the marker gate can no longer re-admit the re-key; the drift record must, or
// the skipped table would keep its 0037 per-clone-random ids forever, even
// after the storage drift is repaired.
//
// It must also re-key ONLY the recorded table: the other three converged in the
// first pass, and rows minted since then carry app-minted ids that are already
// consistent across clones — re-keying those here would manufacture the
// divergence the whole pass exists to remove.
func TestRekeyAuxRowIDsResumesDriftedTableOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	captureLog(t)

	// Marker recorded, no crash sentinel — the post-skip steady state.
	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		"version", auxRekeyPassInitial.markerVersion)
	expectIgnoredSentinelProbes(mock, true)
	expectAuxRekeyState(mock, false, "events")
	expectSetAuxRekeySentinel(mock)
	// Exactly one table probe: any second one is an unexpected call, which is
	// what proves the other three are left alone.
	expectColumnExists(mock, false)
	expectClearAuxRekeyDrifted(mock)
	expectClearAuxRekeySentinel(mock)

	// Pre-pass cursor past the watershed too, so the drift record is overriding
	// both the marker gate and the fresh-clone gate.
	wrote, err := rekeyAuxRowIDs(context.Background(), db, auxRekeyPassInitial.shippedMainVersion, auxRekeyPassInitial)
	if err != nil {
		t.Fatalf("rekeyAuxRowIDs: %v", err)
	}
	if wrote {
		t.Error("expected wrote=false when the resumed table has nothing to re-key")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRekeyAuxRowIDsResumeStaysScopedAfterCrash: a drift-resume pass sets the
// in-flight sentinel like any other, so an unrelated failure (Ctrl-C, lock
// timeout, dropped connection) can leave the sentinel set on a clone whose
// marker is already recorded. That state must NOT be read as the bd-578h9.16
// crash-resume and widened back to all four tables: the other three converged
// in the completed pass, and re-keying rows minted since — on this clone alone —
// is exactly the cross-clone divergence the scoping exists to prevent.
func TestRekeyAuxRowIDsResumeStaysScopedAfterCrash(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	captureLog(t)

	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		"version", auxRekeyPassInitial.markerVersion)
	expectIgnoredSentinelProbes(mock, true)
	// Marker recorded AND sentinel set: a drift-resume that died partway.
	expectAuxRekeyState(mock, true, "events")
	expectSetAuxRekeySentinel(mock)
	expectColumnExists(mock, false)
	expectClearAuxRekeyDrifted(mock)
	expectClearAuxRekeySentinel(mock)

	if _, err := rekeyAuxRowIDs(context.Background(), db, auxRekeyPassInitial.shippedMainVersion, auxRekeyPassInitial); err != nil {
		t.Fatalf("rekeyAuxRowIDs: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestAuxRekeyExemptTablesScopedToRewrittenTables pins the #4380 dirtyBefore
// fix at its source: MigrateUp drops exactly auxRekeyExemptTables' set from the
// pre-existing-dirty guard before the re-key runs, so a table that is exempt is
// a table whose uncommitted user edits stageSchemaTables is then free to sweep
// into the "schema: apply migrations" commit. The exemption must therefore never
// name a table the re-key will not itself rewrite.
//
// The regression is the post-marker resume: a drift retry whose crash left the
// in-flight sentinel set, with the drift record naming only a subset. The old
// collapsed "some rewrite is in flight" bit exempted all four aux tables there;
// a non-drifted table (comments, say) then lost its dirty protection while the
// rewrite touched only events, carrying the user's comments edits into the
// migration commit. Asserting the exempt set equals the drifted subset — and, on
// a converged database, is empty — is asserting those non-drifted tables stay in
// dirtyBefore and so never reach the commit.
func TestAuxRekeyExemptTablesScopedToRewrittenTables(t *testing.T) {
	allFour := []string{"comments", "compaction_snapshots", "events", "issue_snapshots"}
	cases := []struct {
		name              string
		mainVersionBefore int
		setup             func(sqlmock.Sqlmock)
		want              []string
	}{
		{
			// The bug's exact state: marker already recorded (nothing ignored
			// pending), a drift record for events alone, and pass initial's
			// crash sentinel still set. Exemption must be {events}, not all four.
			name:              "post-marker resume with crashed sentinel exempts only the drifted subset",
			mainVersionBefore: auxRekeyPassDerivedInsert.shippedMainVersion,
			setup: func(mock sqlmock.Sqlmock) {
				expectCursorProbe(mock, "ignored_schema_migrations", true)
				expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
					"version", LatestIgnoredVersion())
				expectIgnoredSentinelProbes(mock, true)
				// Pass initial died mid-resume (sentinel set); the shared drift
				// record names events, so pass derived-insert reads it too.
				expectAuxRekeyStateForPass(mock, auxRekeyPassInitial, true, "events")
				expectAuxRekeyStateForPass(mock, auxRekeyPassDerivedInsert, false, "events")
			},
			want: []string{"events"},
		},
		{
			// A markerPending pass is the one-time convergence and legitimately
			// rewrites — and so exempts — all four.
			name:              "marker-pending first pass exempts all four",
			mainVersionBefore: auxRekeyPassInitial.shippedMainVersion - 1,
			setup: func(mock sqlmock.Sqlmock) {
				expectCursorProbe(mock, "ignored_schema_migrations", true)
				expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
					"version", auxRekeyPassInitial.markerVersion-1)
				expectIgnoredSentinelProbes(mock, true)
				expectAuxRekeyStateForPass(mock, auxRekeyPassInitial, false)
				expectAuxRekeyStateForPass(mock, auxRekeyPassDerivedInsert, false)
			},
			want: allFour,
		},
		{
			// Converged and current, nothing owed: no exemption at all, so a
			// dirty aux table is fully protected on a healthy database.
			name:              "converged database exempts nothing",
			mainVersionBefore: auxRekeyPassDerivedInsert.shippedMainVersion,
			setup: func(mock sqlmock.Sqlmock) {
				expectCursorProbe(mock, "ignored_schema_migrations", true)
				expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
					"version", LatestIgnoredVersion())
				expectIgnoredSentinelProbes(mock, true)
				expectAuxRekeyStateForPass(mock, auxRekeyPassInitial, false)
				expectAuxRekeyStateForPass(mock, auxRekeyPassDerivedInsert, false)
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			tc.setup(mock)

			exempt, err := auxRekeyExemptTables(context.Background(), db, tc.mainVersionBefore)
			if err != nil {
				t.Fatalf("auxRekeyExemptTables: %v", err)
			}
			got := make([]string, 0, len(exempt))
			for name := range exempt {
				got = append(got, name)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("exempt = %v, want %v", got, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

// TestRekeyAuxRowIDsKeepsDriftRecordWhileUnrepaired: a resumed pass that finds
// the storage still drifted must re-record the table rather than lose it, so
// the retry survives arbitrarily many passes before the repair happens.
func TestRekeyAuxRowIDsKeepsDriftRecordWhileUnrepaired(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	captureLog(t)

	expectCursorProbe(mock, "ignored_schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
		"version", auxRekeyPassInitial.markerVersion)
	expectIgnoredSentinelProbes(mock, true)
	expectAuxRekeyState(mock, false, "events")
	expectSetAuxRekeySentinel(mock)
	expectColumnExists(mock, true)
	expectEventsSelect(mock).WillReturnError(driftErr)
	expectSetAuxRekeyDrifted(mock, "events")
	expectClearAuxRekeySentinel(mock)

	if _, err := rekeyAuxRowIDs(context.Background(), db, auxRekeyPassInitial.shippedMainVersion, auxRekeyPassInitial); err != nil {
		t.Fatalf("rekeyAuxRowIDs: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
