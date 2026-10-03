package schema

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// Migration 0070 (design §16.3 steps 4-5, be-dt74u amendment, be-h89oq) adds
// a nullable participation_generation to issues, mirrored inertly on wisps.
// NULL means legacy-unmigrated; any non-NULL is a positive declaration
// sourced from store_epoch.epoch. RecordVersionInTx's write fence (design
// §16.2b, in internal/storage/issueops/version_history.go) reads this column
// to decide whether an update-shaped mutation against a legacy record mints
// a version row at all -- a create-shaped mutation stamps a fresh value
// instead. No backfill: NULL is the correct default for every existing row,
// on both planes.
//
// It was first drafted as steps 4-5 appended to the then-unmerged 0068;
// be-v33pa's own attribution_status-only version of 0068 shipped to main
// first (2026-09-28 15:46Z), which froze that file, so these steps have
// their own slot here (mayor ruling 2026-09-29 on tracker be-waare) -- the
// same reason 0069 got its own slot rather than landing as 0068's step 8.
//
// wisps.participation_generation is guarded on the wisps table existing as
// well as the column, mirroring 0067's wisps.current_revision guard exactly
// (see that migration's header): wisps is dolt-ignored/clone-local, so a
// clone that never synced the local wisp tables must no-op rather than
// abort. The clone-local twin for a workspace that never synced the wisps
// table at all is ignored/0028_add_wisps_participation_generation.up.sql.
const migration0070Up = "0070_add_participation_generation.up.sql"
const migration0070Down = "0070_add_participation_generation.down.sql"

// TestLatestVersionIncludesMigration0070 pins the real next free slot this
// migration claims, superseding 0069's own version of this test
// (LatestVersion() moved from 69 to 70 the moment this migration file was
// added). Deliberately a hardcoded literal for the same reason 0067's,
// 0068's, and 0069's were: LatestVersion() drifting to 70 for the wrong
// reason (an unrelated migration landing first) should still be caught by
// this test failing to explain why 70 is participation_generation-shaped,
// which the CLI test below checks.
func TestLatestVersionIncludesMigration0070(t *testing.T) {
	const want = 70
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (issues/wisps participation_generation migration slot claimed by be-h89oq)", got, want)
	}
}

// TestMigration0070AddsParticipationGeneration is the pure-Go, DB-independent
// half of the pin, mirroring TestMigration0069WidensChangeAtAndRemovedAtPrecision's
// shape: it checks the frozen migration bytes and the CLI-bundle override
// text directly, no `dolt` binary required.
func TestMigration0070AddsParticipationGeneration(t *testing.T) {
	upSQL, err := MigrationSQL(migration0070Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v, want the migration file to exist", migration0070Up, err)
	}
	for _, want := range []string{
		"ALTER TABLE issues ADD COLUMN participation_generation BIGINT NULL",
		"@issues_pg_needs_add",
		"ALTER TABLE wisps ADD COLUMN participation_generation BIGINT NULL",
		"@wisps_pg_needs_add",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0070 up migration missing %q (design §16.3 steps 4-5)\nfull SQL:\n%s", want, upSQL)
		}
	}
	if n := strings.Count(upSQL, "COLUMN_NAME = 'participation_generation'"); n != 2 {
		t.Errorf("0070 up migration has %d INFORMATION_SCHEMA probes for participation_generation, want 2 (one for issues, one for wisps)\nfull SQL:\n%s", n, upSQL)
	}
	if !strings.Contains(strings.ToUpper(upSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0070 up migration must keep its guarded PREPARE blocks — they are what make a raw .up.sql replay onto an already-migrated store a no-op, and Dolt accepts no unprepared conditional ADD COLUMN. Unwrapping it also invalidates cliMigration0070AddParticipationGeneration.")
	}

	// The bundle override is what keeps the two PREPARE blocks above off the
	// pre-2.3 CLI path.
	bundle := cliCompatibleMigrationSQL(migration0070Up, upSQL)
	for _, want := range []string{
		"ALTER TABLE issues ADD COLUMN participation_generation BIGINT NULL;",
		"ALTER TABLE wisps ADD COLUMN participation_generation BIGINT NULL;",
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("0070's CLI bundle substitute (cliMigration0070AddParticipationGeneration) missing direct DDL %q", want)
		}
	}
	// Design §16.3 step 5 (be-dt74u amendment, be-h89oq) makes 0070's CLI
	// substitute ALTER wisps directly, the same unconditional-wisps-ALTER
	// shape 0067's current_revision override already uses (see
	// cliSubstituteAssumesWispTables' 0067 case).
	if !cliSubstituteAssumesWispTables(migration0070Up) {
		t.Error("0070's CLI substitute ALTERs wisps unguarded, so it must be listed in cliSubstituteAssumesWispTables — a replay over a clone that never synced the wisp tables has to fall back to the frozen source text")
	}

	// down.sql files are not part of the embedded FS (only migrations/*.up.sql
	// is //go:embed'd — see mainSource.files), so unlike the up side above,
	// this reads straight from disk by package-relative path, matching
	// TestMigration0068AddsAttributionStatus's precedent.
	downBytes, err := os.ReadFile("migrations/" + migration0070Down)
	if err != nil {
		t.Fatalf("read %s: %v, want the migration file to exist", migration0070Down, err)
	}
	downSQL := string(downBytes)
	for _, want := range []string{
		"ALTER TABLE issues DROP COLUMN participation_generation",
		"ALTER TABLE wisps DROP COLUMN participation_generation",
	} {
		if !strings.Contains(downSQL, want) {
			t.Errorf("0070 down migration missing %q\nfull SQL:\n%s", want, downSQL)
		}
	}
	// Only migrations/*.up.sql is embedded into the CLI fresh bundle
	// (mainSource.files), so the pre-2.3 prepared-DDL hazard never reaches a
	// down migration and the guard is free — 0068's/0069's downs are the
	// precedent.
	if !strings.Contains(strings.ToUpper(downSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0070 down migration must guard its DROP COLUMNs the way the up migration guards its ADD COLUMNs, so a partially-applied or already-rolled-back workspace rolls back safely")
	}
}

// TestMigration0070AddsParticipationGenerationThroughDoltCLI applies the full
// migration bundle through a real `dolt` binary (skipped without one — see
// testutil.RequireDoltBinary) and checks the shape acceptance criteria a
// pure-Go SQL-text check cannot: actual column type/nullability as Dolt
// reports it, on both planes.
func TestMigration0070AddsParticipationGenerationThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := t.TempDir()
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	requireDoltDataType(t, dir, "issues", "participation_generation", "bigint", "YES")
	requireDoltDataType(t, dir, "wisps", "participation_generation", "bigint", "YES")

	// No backfill: design §16.2a's "NULL = legacy-unmigrated" declaration
	// needs every pre-existing (and freshly-inserted, pre-fence) row to read
	// back NULL. Only issues is round-tripped here — the column is read and
	// written on that plane; wisps carries it for schema-parity shape only
	// (§16.2a) and is never read or written by this phase.
	runDoltSQL(t, dir, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES ('pg-1', 't', 'd', 'des', 'ac', 'n')`)
	// id rides along with the target column: dolt sql -r csv emits no data
	// line at all for a single-column result whose only value is NULL (just
	// the header survives) -- reproduced identically against pre-existing
	// unrelated nullable columns (assignee, closed_at), so it's a CSV-writer
	// quirk, not something specific to this migration. A second,
	// always-populated column keeps the row real.
	rows := queryDoltCSV(t, dir, `SELECT id, participation_generation FROM issues WHERE id = 'pg-1'`)
	if len(rows) != 1 || rows[0]["participation_generation"] != "" {
		t.Fatalf("participation_generation for a freshly-inserted issues row = %v, want NULL (empty string in CSV form)", rows)
	}
}
