package schema

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// The #7073 gate's healthy-path contracts, pinned through the two callers
// that run it on every open. Every probe in this file is primed individually
// rather than through the composite helpers (expectConvergedProbe and the
// like), because the subject here is the gate's POSITION in each sequence
// and its write-freedom: the composites would consume the ordering this file
// exists to pin.
//
// The probes use only statements that exist already — the file deliberately
// references no symbol introduced with the residue reconcile, so the run
// passes exactly when the gate does its one-read probe at the pinned
// position, and fails with an unmet expectation when it does not.

// expectHealthyTempResidueGate primes the gate's single read on a healthy
// database: one empty HEAD listing, no dolt_ignore resolution (no candidate
// names survived the listing), and nothing else.
func expectHealthyTempResidueGate(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES AS OF 'HEAD' LIKE '__temp__%'")).
		WillReturnRows(sqlmock.NewRows([]string{"Tables_in_testdb"}))
}

// TestMigrateUpLeavesAHealthyDatabaseUnwritten is the every-open contract: a
// database with no pending migration, no tracked cursor and no residue must
// see its writable open perform no write at all — the residue gate included.
// In ordered mode any COMMIT, DOLT_RM or DOLT_COMMIT the reconcile issued
// would surface as an unexpected statement and fail the pass; the empty
// listing is the only residue statement primed.
func TestMigrateUpLeavesAHealthyDatabaseUnwritten(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnorePatternSeedNoop(mock, LatestVersion())
	expectIgnoredCursorGate(mock, "", false, nil, false)
	expectHealthyTempResidueGate(mock)
	expectNoMigrationWorkNeeded(mock)

	applied, err := MigrateUp(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateUp() error = %v, want nil", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUp() = %d on a healthy database, want 0: nothing may count as a repair", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (the healthy path must be pure reads): %v", err)
	}
}

// expectVetoedTempResidueListing primes the gate's HEAD listing returning one
// committed residue name, followed by the dolt_ignore resolution that vetoes
// it: an exact-name row with ignored=false, alongside the seeded wildcard the
// name also matches. The operator's exact-name override must win over the
// wildcard, so the gate yields nothing eligible.
//
// Like the other probes in this file it is written against pre-existing
// helpers only, so the whole contract — the listing, the resolution read, and
// the veto's precedence — is pinned positionally in the ordered mock.
func expectVetoedTempResidueListing(mock sqlmock.Sqlmock, table string) {
	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES AS OF 'HEAD' LIKE '__temp__%'")).
		WillReturnRows(sqlmock.NewRows([]string{"Tables_in_testdb"}).AddRow(table))
	expectIgnoreResolution(mock, "", table, []doltIgnoreRow{
		{pattern: "__temp__%", ignored: true},
		{pattern: table, ignored: false},
	})
}

// TestMigrateUpLeavesAVetoedIntermediateTracked pins the veto half of the
// gate: a __temp__ name committed at HEAD whose dolt_ignore carries an
// exact-name override with ignored=false is versioned on purpose. Its
// presence alone must trigger no repair — no unstage, no COMMIT, no DOLT_RM,
// none of which the ordered mock would let pass unprimed — and the pass must
// still report no work.
func TestMigrateUpLeavesAVetoedIntermediateTracked(t *testing.T) {
	db, mock := newMockDB(t)

	expectIgnorePatternSeedNoop(mock, LatestVersion())
	expectIgnoredCursorGate(mock, "", false, nil, false)
	expectVetoedTempResidueListing(mock, "__temp__wisp_child_counters")
	expectNoMigrationWorkNeeded(mock)

	applied, err := MigrateUp(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateUp() error = %v, want nil: a vetoed intermediate must not break the pass", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUp() = %d with a vetoed intermediate, want 0: the veto must trigger no repair", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (a vetoed intermediate must stay tracked and unwritten): %v", err)
	}
}

// TestAlreadyConvergedIgnoresAVetoedIntermediate pins the same veto on the
// lock-free fast path: the gate evaluates the listing and the veto's
// resolution read, yields nothing eligible, and the probe stays armed — a
// deliberately versioned __temp__ table must not defeat convergence the way
// genuinely ignored residue does.
func TestAlreadyConvergedIgnoresAVetoedIntermediate(t *testing.T) {
	db, mock := newMockDB(t)

	expectCurrentDatabase(mock, "testdb")
	expectNoMigrationWorkNeeded(mock)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	expectIgnoredCursorGate(mock, "", false, nil, false)
	expectVetoedTempResidueListing(mock, "__temp__wisp_child_counters")
	expectMigrationLockProbe(mock, "testdb", 1)

	converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
	if err != nil {
		t.Fatalf("alreadyConverged() error = %v, want nil: a vetoed intermediate must not break the probe", err)
	}
	if !converged {
		t.Fatal("alreadyConverged() = false with a vetoed intermediate, want true (the fast path must stay armed)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestAlreadyConvergedProbesTempResidueBeforeTheLock pins the gate's position
// in the locked fast path: after the ignored-cursor gate, before the lock
// probe. A gate term missing here is silently unreachable in server mode,
// because this fast path short-circuits before MigrateUp ever runs — the
// exact way a missing probe in alreadyConverged let #4356 databases stay
// wedged on the server while the embedded lane healed them.
func TestAlreadyConvergedProbesTempResidueBeforeTheLock(t *testing.T) {
	db, mock := newMockDB(t)

	expectCurrentDatabase(mock, "testdb")
	expectNoMigrationWorkNeeded(mock)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	expectIgnoredCursorGate(mock, "", false, nil, false)
	expectHealthyTempResidueGate(mock)
	expectMigrationLockProbe(mock, "testdb", 1)

	converged, err := alreadyConverged(context.Background(), db, "testdb", nil)
	if err != nil {
		t.Fatalf("alreadyConverged() error = %v, want nil", err)
	}
	if !converged {
		t.Fatal("alreadyConverged() = false on a healthy database, want true (the fast path must stay armed)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
