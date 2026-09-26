package schema

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// Step 8 of migration 0068 (be-hs42e.8 / gastownhall/beads#6132, the
// DATETIME(6) widening slice): 0067 creates issue_versions.change_at and
// .removed_at as plain DATETIME -- precision 0, whole seconds only. Dolt's
// datetime(0) does not truncate sub-second input; it ROUNDS half-up
// (pinned for a different column by testAuditImportCommentSubSecond in
// backend/conformance/audit_labels-comments-events.go, and reconfirmed
// directly against issue_versions below). For a column whose entire job is
// placing history in order, precision 0 has two consequences: a sub-second
// write can read back rounded into the *next* second, and two writes less
// than a second apart in real time can round onto the identical stored
// value and become indistinguishable by change_at. The fix is a widen to
// DATETIME(6) (microsecond), appended here as 0068's step 8 rather than
// claiming a new slot -- this bead's exit contract, and 0068's own header,
// both require reusing this still-unmerged file (see that header for why
// 0069 and 0070 are already spoken for by a different bead).
//
// removed_at widens in lockstep even though no Go code writes it yet (a
// repo-wide grep for removed_at/RemovedAt turns up exactly two hits, both
// schema: 0067's CREATE TABLE and this package's CLI-bundle mirror of it).
// It is change_at's paired lifecycle column on the same row of the same
// table, so leaving it at precision 0 while change_at moves to precision 6
// would be exactly the asymmetry hazard this bead's own title warns
// against ("before any store accumulates real history") -- a second
// migration once removed_at gets a writer, instead of one now while the
// table is still empty and the ALTER is free.
const migration0068ChangeAtDatetimeGuard = "@issue_versions_change_at_needs_widen"
const migration0068RemovedAtDatetimeGuard = "@issue_versions_removed_at_needs_widen"

// TestMigration0068WidensChangeAtAndRemovedAtPrecision is the pure-Go,
// DB-independent half of the pin, mirroring
// TestMigration0068AddsAttributionStatus's shape for steps 6/7: it checks
// the frozen migration bytes and the CLI-bundle override text directly, no
// `dolt` binary required. RED until step 8 is appended to both the up.sql
// guarded-PREPARE block and cliMigration0068AddAttributionStatus.
func TestMigration0068WidensChangeAtAndRemovedAtPrecision(t *testing.T) {
	upSQL, err := MigrationSQL(migration0068Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v, want the migration file to exist", migration0068Up, err)
	}
	for _, want := range []string{
		"ALTER TABLE issue_versions MODIFY COLUMN change_at DATETIME(6) NOT NULL",
		"ALTER TABLE issue_versions MODIFY COLUMN removed_at DATETIME(6)",
		migration0068ChangeAtDatetimeGuard,
		migration0068RemovedAtDatetimeGuard,
		"DATETIME_PRECISION",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0068 up migration missing %q (step 8, change_at/removed_at DATETIME(6) widen)\nfull SQL:\n%s", want, upSQL)
		}
	}
	if !strings.Contains(strings.ToUpper(upSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0068 up migration must keep its guarded PREPARE block for every step including step 8 -- it is what makes a raw .up.sql replay onto an already-migrated store a no-op, and Dolt accepts no unprepared conditional MODIFY COLUMN")
	}

	bundle := cliCompatibleMigrationSQL(migration0068Up, upSQL)
	for _, want := range []string{
		"ALTER TABLE issue_versions MODIFY COLUMN change_at DATETIME(6) NOT NULL;",
		"ALTER TABLE issue_versions MODIFY COLUMN removed_at DATETIME(6);",
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("0068's CLI bundle substitute (cliMigration0068AddAttributionStatus) missing direct DDL %q (step 8) -- without it a fresh CLI-built database keeps precision-0 change_at/removed_at while the runtime migration path widens them", want)
		}
	}

	// down.sql files are not part of the embedded FS (only
	// migrations/*.up.sql is //go:embed'd -- see mainSource.files), so like
	// TestMigration0068AddsAttributionStatus's own down check, this reads
	// straight from disk by package-relative path.
	downBytes, err := os.ReadFile("migrations/" + migration0068Down)
	if err != nil {
		t.Fatalf("read %s: %v, want the migration file to exist", migration0068Down, err)
	}
	downSQL := string(downBytes)
	for _, want := range []string{
		"ALTER TABLE issue_versions MODIFY COLUMN change_at DATETIME NOT NULL",
		"ALTER TABLE issue_versions MODIFY COLUMN removed_at DATETIME",
		"COLUMN_NAME = 'change_at'",
		"COLUMN_NAME = 'removed_at'",
	} {
		if !strings.Contains(downSQL, want) {
			t.Errorf("0068 down migration missing %q (step 8 reverse)\nfull SQL:\n%s", want, downSQL)
		}
	}
}

// TestMigration0068ChangeAtSurvivesSubSecondPrecisionThroughDoltCLI is the
// live half: it demonstrates the bug against a real `dolt` binary (skipped
// without one) and is be-hs42e.8's exit contract's own acceptance criteria,
// verbatim -- "a sub-second write reading back rounded to the next second"
// and "same-second versions colliding". Both fixture values and the exact
// rounding direction were measured directly against dolt 2.3.3 before
// writing this test (SELECT ... datetime_precision, and a scratch
// DATETIME/DATETIME(6) round-trip), not assumed.
func TestMigration0068ChangeAtSurvivesSubSecondPrecisionThroughDoltCLI(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := t.TempDir()
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	requireDoltColumnShape(t, dir, "issue_versions", "change_at", "datetime(6)", "NO")
	requireDoltColumnShape(t, dir, "issue_versions", "removed_at", "datetime(6)", "YES")

	// Acceptance criterion 1: "a sub-second write reading back rounded to
	// the next second." .750000 is testAuditImportCommentSubSecond's own
	// fixture value, chosen because Dolt's datetime(0) rounding is HALF-UP
	// (pinned there for a different column, reconfirmed here directly) --
	// >= .5 always rounds forward a whole second, so at precision 0 this
	// value is guaranteed to land on the wrong second, not merely lose
	// precision within the right one.
	runDoltSQL(t, dir, `INSERT INTO issue_versions
		(issue_id, revision, epoch, change_at, attribution_status)
		VALUES ('iv-sub', 1, 1, '2026-09-01 00:00:00.750000', 'claimed')`)
	rows := queryDoltCSV(t, dir, `SELECT change_at FROM issue_versions WHERE issue_id = 'iv-sub'`)
	if len(rows) != 1 {
		t.Fatalf("iv-sub round-trip returned %d rows, want 1: %v", len(rows), rows)
	}
	if got, want := rows[0]["change_at"], "2026-09-01 00:00:00.750000"; got != want {
		t.Errorf("change_at sub-second round-trip = %q, want %q (DATETIME(6) must keep microseconds; DATETIME(0) rounds .750000 up to the next second, 2026-09-01 00:00:01)", got, want)
	}

	// Acceptance criterion 2: "same-second versions colliding." Two
	// revisions of the same issue, 300ms apart in wall-clock terms, both
	// inside the [.000, .500) half of the same second -- at precision 0
	// both round DOWN to the identical whole second and become
	// indistinguishable by change_at, exactly the ordering defect an
	// audit-history column exists to prevent.
	runDoltSQL(t, dir, `INSERT INTO issue_versions
		(issue_id, revision, epoch, change_at, attribution_status)
		VALUES
			('iv-collide', 1, 1, '2026-09-01 00:00:05.100000', 'claimed'),
			('iv-collide', 2, 1, '2026-09-01 00:00:05.400000', 'claimed')`)
	rows = queryDoltCSV(t, dir, `SELECT revision, change_at FROM issue_versions WHERE issue_id = 'iv-collide' ORDER BY revision`)
	if len(rows) != 2 {
		t.Fatalf("iv-collide round-trip returned %d rows, want 2: %v", len(rows), rows)
	}
	if rows[0]["change_at"] == rows[1]["change_at"] {
		t.Errorf("revisions 1 and 2 of iv-collide both read back change_at = %q; two writes 300ms apart must remain distinguishable, not collide onto the same stored second", rows[0]["change_at"])
	} else {
		if got, want := rows[0]["change_at"], "2026-09-01 00:00:05.100000"; got != want {
			t.Errorf("iv-collide revision 1 change_at = %q, want %q", got, want)
		}
		if got, want := rows[1]["change_at"], "2026-09-01 00:00:05.400000"; got != want {
			t.Errorf("iv-collide revision 2 change_at = %q, want %q", got, want)
		}
	}
}
