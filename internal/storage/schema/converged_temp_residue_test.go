package schema

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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

// vetoedTempResidue is the committed residue name the veto tests version on
// purpose.
const vetoedTempResidue = "__temp__wisp_child_counters"

// tempResidueVetoes are the two ways an operator keeps vetoedTempResidue
// versioned beside the seeded wildcard: an exact-name ignored=0 row, and a
// wildcard one, which Dolt reads as a glob and SQL LIKE cannot match.
var tempResidueVetoes = []struct {
	name    string
	pattern string
}{
	{name: "exact name", pattern: vetoedTempResidue},
	{name: "Dolt glob", pattern: "__temp__wisp*"},
}

// expectVetoedTempResidueListing primes the gate's HEAD listing returning
// vetoedTempResidue, followed by the dolt_ignore resolution that vetoes it:
// the veto row with ignored=false, alongside the seeded wildcard the name
// also matches. The operator's override is the more specific pattern, so the
// gate yields nothing eligible.
func expectVetoedTempResidueListing(mock sqlmock.Sqlmock, veto string) {
	expectTempListing(mock, "", "HEAD", vetoedTempResidue)
	expectIgnoreResolution(mock, "", vetoedTempResidue, []doltIgnoreRow{
		{pattern: "__temp__%", ignored: true},
		{pattern: veto, ignored: false},
	})
}

// TestMigrateUpLeavesAVetoedIntermediateTracked pins the veto half of the
// gate: a __temp__ name committed at HEAD whose dolt_ignore carries an
// ignored=false override, exact or a glob, is versioned on purpose. Its
// presence alone must trigger no repair (no unstage, no COMMIT, no DOLT_RM,
// none of which the ordered mock would let pass unprimed) and the pass must
// still report no work.
func TestMigrateUpLeavesAVetoedIntermediateTracked(t *testing.T) {
	for _, veto := range tempResidueVetoes {
		t.Run(veto.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			failOnSwallowedAdvisory(t)

			expectIgnorePatternSeedNoop(mock, LatestVersion())
			expectIgnoredCursorGate(mock, "", false, nil, false)
			expectVetoedTempResidueListing(mock, veto.pattern)
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
		})
	}
}

// TestAlreadyConvergedIgnoresAVetoedIntermediate pins the same vetoes on the
// lock-free fast path: the gate evaluates the listing and the veto's
// resolution read, yields nothing eligible, and the probe stays armed. A
// deliberately versioned __temp__ table must not defeat convergence the way
// genuinely ignored residue does, or every open of that database would take
// the migration lock forever.
func TestAlreadyConvergedIgnoresAVetoedIntermediate(t *testing.T) {
	for _, veto := range tempResidueVetoes {
		t.Run(veto.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			failOnSwallowedAdvisory(t)

			expectCurrentDatabase(mock, "testdb")
			expectNoMigrationWorkNeeded(mock)
			expectDoltIgnoreRead(mock, unqualifiedDoltIgnore, seededIgnorePatterns(LatestVersion()))
			expectIgnoredCursorGate(mock, "", false, nil, false)
			expectVetoedTempResidueListing(mock, veto.pattern)
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
		})
	}
}
