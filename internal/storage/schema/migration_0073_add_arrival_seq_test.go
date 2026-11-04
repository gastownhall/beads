package schema

import (
	"os"
	"strings"
	"testing"
)

// Migration 0073 (+ ignored/0029) adds arrival_seq, the commit-ordered create
// sequence the events journal stamps on a wisp-plane row
// (types.Issue.ArrivalSeq, issueops.RecordEventInTx).

const (
	migration0073Up        = "0073_add_arrival_seq.up.sql"
	migration0073Down      = "0073_add_arrival_seq.down.sql"
	ignoredMigration0029Up = "0029_add_wisps_arrival_seq.up.sql"
)

// TestLatestVersionIncludesMigration0073 (pinning LatestVersion() == 73 and
// LatestIgnoredVersion() == 29) is superseded by
// TestLatestVersionIncludesMigration0074
// (migration_0074_create_id_tombstones_test.go) now that 0074 and ignored/0030
// claim the next free slots -- only one such pin lives at a time, matching how
// this test itself already superseded 0072's own version.

// TestMigration0073AddsArrivalSeqToBothPlanes checks the frozen bytes and the
// CLI-bundle substitute directly, no dolt binary required.
func TestMigration0073AddsArrivalSeqToBothPlanes(t *testing.T) {
	upSQL, err := MigrationSQL(migration0073Up)
	if err != nil {
		t.Fatalf("MigrationSQL(%s) error = %v, want the migration file to exist", migration0073Up, err)
	}
	for _, want := range []string{
		"'ALTER TABLE issues ADD COLUMN arrival_seq BIGINT'",
		"'ALTER TABLE wisps ADD COLUMN arrival_seq BIGINT'",
		"COLUMN_NAME = 'arrival_seq'",
		// The wisps arm is guarded on the TABLE existing, not only on the
		// column: older workspaces created issues-only (0054 precedent).
		"INFORMATION_SCHEMA.TABLES",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0073 up migration missing %q\nfull SQL:\n%s", want, upSQL)
		}
	}
	if !strings.Contains(strings.ToUpper(upSQL), "PREPARE STMT FROM @SQL") {
		t.Error("0073 must keep its guarded PREPARE blocks: they make a raw .up.sql replay onto an already-migrated store a no-op")
	}
	// The column is nullable with no default on purpose: NULL is "never
	// stamped", and a DEFAULT would rewrite every existing row's cell.
	for _, forbidden := range []string{"DEFAULT", "NOT NULL"} {
		for _, line := range strings.Split(upSQL, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			if strings.Contains(strings.ToUpper(line), forbidden) && strings.Contains(line, "arrival_seq") {
				t.Errorf("0073 declares arrival_seq with %q (%s); the column must be a bare nullable BIGINT", forbidden, strings.TrimSpace(line))
			}
		}
	}
	// The wisps unique index is built here as well as by ignored/0029: this
	// migration's down drops it with the column and leaves 0029 recorded, so
	// a 0073 applied again is the only file left to rebuild it. Guarded like
	// 0029's, and never on the replicating issues table, where the column is
	// meaningless.
	for _, want := range []string{
		"'CREATE UNIQUE INDEX uk_wisps_arrival_seq ON wisps (arrival_seq)'",
		"INDEX_NAME = 'uk_wisps_arrival_seq'",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("0073 up migration missing %q: a down then up would leave wisps.arrival_seq without its unique index\nfull SQL:\n%s", want, upSQL)
		}
	}
	if strings.Contains(upSQL, "ON issues (arrival_seq)") {
		t.Error("0073 indexes issues.arrival_seq: the column is always NULL on the replicating plane")
	}

	bundle := cliCompatibleMigrationSQL(migration0073Up, upSQL)
	for _, want := range []string{
		"ALTER TABLE issues ADD COLUMN arrival_seq BIGINT;",
		"ALTER TABLE wisps ADD COLUMN arrival_seq BIGINT;",
		"CREATE UNIQUE INDEX uk_wisps_arrival_seq ON wisps (arrival_seq);",
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("0073's CLI bundle substitute missing direct DDL %q: a pre-2.3 CLI no-ops the prepared ALTER", want)
		}
	}
	if !cliSubstituteAssumesWispTables(migration0073Up) {
		t.Error("0073's CLI substitute ALTERs wisps unguarded, so it must be listed in cliSubstituteAssumesWispTables")
	}

	downBytes, err := os.ReadFile("migrations/" + migration0073Down)
	if err != nil {
		t.Fatalf("read %s: %v", migration0073Down, err)
	}
	downSQL := string(downBytes)
	for _, want := range []string{
		"'ALTER TABLE issues DROP COLUMN arrival_seq'",
		"'ALTER TABLE wisps DROP COLUMN arrival_seq'",
	} {
		if !strings.Contains(downSQL, want) {
			t.Errorf("0073 down migration missing %q\nfull SQL:\n%s", want, downSQL)
		}
	}
}

// TestIgnoredMigration0029CarriesTheColumnAndTheUniqueIndex pins the twin that
// reaches fresh clones, and the unique index that makes a stamp impossible to
// hand out twice.
func TestIgnoredMigration0029CarriesTheColumnAndTheUniqueIndex(t *testing.T) {
	upSQL, err := IgnoredMigrationSQL(ignoredMigration0029Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) error = %v", ignoredMigration0029Up, err)
	}
	for _, want := range []string{
		"'ALTER TABLE wisps ADD COLUMN arrival_seq BIGINT'",
		"'CREATE UNIQUE INDEX uk_wisps_arrival_seq ON wisps (arrival_seq)'",
		"INDEX_NAME = 'uk_wisps_arrival_seq'",
		"INFORMATION_SCHEMA.TABLES",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("ignored/0029 missing %q\nfull SQL:\n%s", want, upSQL)
		}
	}
	if strings.Contains(upSQL, "ALTER TABLE issues") {
		t.Error("ignored/0029 must not touch issues: it is a replicated table the ignored series never owns")
	}
}

// TestIgnoredMigration0029RaisesTheSeqCounterPastTheStamps pins the step that
// keeps a replayed counter seed above every surviving stamp. ignored/0022
// re-seeds bd_events_seq from the journal's MAX(seq) whenever the counter table
// goes missing and the ignored series replays from its floor; this file re-runs
// in the same replay, after 0022, and must raise the counter past
// MAX(arrival_seq) — never lower it — or a pruned journal hands the next create
// a value below rows that are still there.
func TestIgnoredMigration0029RaisesTheSeqCounterPastTheStamps(t *testing.T) {
	upSQL, err := IgnoredMigrationSQL(ignoredMigration0029Up)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s) error = %v", ignoredMigration0029Up, err)
	}
	for _, want := range []string{
		"'UPDATE bd_events_seq SET next_seq = GREATEST(next_seq, COALESCE((SELECT MAX(arrival_seq) FROM wisps), 0)) WHERE id = 0'",
		"TABLE_NAME = 'bd_events_seq'",
	} {
		if !strings.Contains(upSQL, want) {
			t.Errorf("ignored/0029 missing %q\nfull SQL:\n%s", want, upSQL)
		}
	}
	raise := strings.Index(upSQL, "UPDATE bd_events_seq")
	if index := strings.Index(upSQL, "'CREATE UNIQUE INDEX uk_wisps_arrival_seq"); raise < index {
		t.Error("ignored/0029 raises the counter before it ensures the column and its index; the raise must come last, over the column it reads")
	}

	// The replay this step exists for starts at the counter table's own floor:
	// bd_events_seq must stay a floored sentinel at or below 0022, the migration
	// that re-creates and re-seeds it, so that 0029 is in every replay that
	// re-seeds the counter.
	var floor int
	for _, st := range ignoredSource.sentinelFlooredTables {
		if st.table == "bd_events_seq" {
			floor = st.replayFloor
		}
	}
	if floor == 0 || floor >= 22 {
		t.Fatalf("bd_events_seq replayFloor = %d, want a floor below 22 (ignored/0022 re-creates the counter), so a lost counter replays 0022 and then 0029", floor)
	}
}
