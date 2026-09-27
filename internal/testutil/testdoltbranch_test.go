package testutil

import (
	"strings"
	"testing"
)

// TestSetupSharedTestDB_RefusesAmbientPortMismatch guards the same class of
// bug as TestDoltContainerStartSites_SetBeadsServerPortEnv: if something
// upstream in the resolution chain ever passes a port that disagrees with
// the ambient BEADS_DOLT_SERVER_PORT, SetupSharedTestDB must refuse rather
// than silently create a database against whichever server
// BEADS_DOLT_SERVER_PORT happens to point at. This check must not require a
// real Dolt server: it has to fire before any connection is attempted, the
// same way the existing production-port (3307) firewall does.
func TestSetupSharedTestDB_RefusesAmbientPortMismatch(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "19191") // decoy ambient shared server

	db, err := SetupSharedTestDB(29292, "irrelevant_db") // disagrees with ambient port above
	if db != nil {
		_ = db.Close()
	}
	if err == nil {
		t.Fatal("SetupSharedTestDB: expected refusal when port disagrees with ambient BEADS_DOLT_SERVER_PORT, got nil error")
	}
	if !strings.Contains(err.Error(), "BEADS_DOLT_SERVER_PORT") {
		t.Errorf("SetupSharedTestDB error = %q, want it to name the ambient BEADS_DOLT_SERVER_PORT disagreement", err.Error())
	}
}
