package schema

import "testing"

// depLocalityMarkerUp is the ignored migration whose pre-repair the dep
// locality fix registers (gastownhall/beads#6066).
const depLocalityMarkerUp = "0032_dep_locality_repair.up.sql"

// TestDepLocalityMarkerAvoidsDirtyTableTextGuard pins the load-bearing
// property of the 0032 marker's text: the pre-flight dirty-table guard
// (pendingMigrationDirtyTables) must not read it as touching any table the
// repair writes. If it did, every open of a database with pre-existing
// uncommitted changes would refuse the pending marker before the repair's
// own, precise, need-gated refusal — including databases whose repair finds
// nothing to do, which the repair deliberately leaves alone. The typed
// refusal belongs to the repair, where the documented commit-and-retry
// recovery (DirtyTablesError) can reach it.
func TestDepLocalityMarkerAvoidsDirtyTableTextGuard(t *testing.T) {
	marker, err := IgnoredMigrationSQL(depLocalityMarkerUp)
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(%s): %v", depLocalityMarkerUp, err)
	}
	for _, table := range []string{"issues", "dependencies", "wisps", "wisp_dependencies"} {
		if migrationSQLTouchesTable(marker, table) {
			t.Fatalf(
				"ignored 0032's text looks like it writes %s to the pre-flight dirty-table guard; "+
					"the refusal for a dirty graph must stay in the repair, typed as DirtyTablesError",
				table)
		}
	}
}
