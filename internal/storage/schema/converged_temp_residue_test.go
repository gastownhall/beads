package schema

import (
	"context"
	"testing"
)

// The #7073 gate's healthy-path contracts, pinned through the two callers
// that run it on every open. Every probe here is primed individually rather
// than through the composite helpers (expectOpenReconcilesNoop and the like),
// because the subject is the gate's POSITION in each sequence and its
// write-freedom: the composites would consume the ordering this file exists
// to pin. failOnSwallowedAdvisory makes an unprimed statement the reconcile
// would otherwise demote to an advisory decline fail the test.

// TestMigrateUpLeavesAHealthyDatabaseUnwritten is the every-open contract: a
// database with no pending migration, no tracked cursor and no residue must
// see its writable open perform no write at all, the residue gate included.
// In ordered mode any COMMIT, DOLT_RM or DOLT_COMMIT the reconcile issued
// would surface as an unexpected statement and fail the pass; the empty
// listing is the only residue statement primed.
func TestMigrateUpLeavesAHealthyDatabaseUnwritten(t *testing.T) {
	db, mock := newMockDB(t)
	failOnSwallowedAdvisory(t)

	expectIgnorePatternSeedNoop(mock, LatestVersion())
	expectIgnoredCursorGate(mock, "", false, nil, false)
	expectTempResidueGate(mock, "")
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

// TestAlreadyConvergedProbesTempResidueBeforeTheLock pins the gate's position
// in the locked fast path: after the ignored-cursor gate, before the lock
// probe. A gate term missing here is silently unreachable in server mode,
// because this fast path short-circuits before MigrateUp ever runs, the
// exact way a missing probe in alreadyConverged let #4356 databases stay
// wedged on the server while the embedded lane healed them.
func TestAlreadyConvergedProbesTempResidueBeforeTheLock(t *testing.T) {
	db, mock := newMockDB(t)
	failOnSwallowedAdvisory(t)

	expectCurrentDatabase(mock, "testdb")
	expectNoMigrationWorkNeeded(mock)
	expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
	expectIgnoredCursorGate(mock, "", false, nil, false)
	expectTempResidueGate(mock, "")
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
