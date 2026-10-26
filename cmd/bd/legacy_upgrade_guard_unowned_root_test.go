package main

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/testutil/legacyworkspace"
)

// unownedDoltRootRefusal is the refusal command admission prints for a
// .beads/dolt root that holds data under a current-era version witness while
// nothing — not metadata, not config.yaml, not the environment — selects a
// Dolt server to own it. Spelled out rather than built from the guard's
// constant so the tests pin the user-facing text itself.
var unownedDoltRootRefusal = legacyworkspace.LegacyDoltRefusal(
	"Dolt data root .beads/dolt without a server selection")

// TestLegacyUpgradeGuardRefusesUnownedDoltRootWithCurrentWitness pins the
// shape GH#6259 reports: a .beads/dolt root that holds data, a current-era
// version witness, no embedded repository and no server or shared-server
// selection anywhere. The witness settles the era — a current-era bd wrote
// this workspace — but it cannot say what owns the root: bd writes the same
// witness for a Dolt sql-server's data root, and when the selection is
// missing this run, store selection would open a brand-new embedded database
// beside the root and answer every query from it (GH#3817's false-empty).
// The guard refuses with the unowned-root reason instead, whatever embedded
// shape the metadata takes: absent, blank backend, or explicit embedded mode.
func TestLegacyUpgradeGuardRefusesUnownedDoltRootWithCurrentWitness(t *testing.T) {
	for name, metadata := range map[string]string{
		"metadata-less":          "",
		"blank mode":             `{"backend":"dolt"}`,
		"explicit embedded mode": `{"backend":"dolt","dolt_mode":"embedded"}`,
	} {
		t.Run(name, func(t *testing.T) {
			warnings := captureLegacyUpgradeWarnings(t)
			beadsDir := writeDoltRootWorkspace(t, metadata, "", "1.3.0")

			err := guardLegacyUpgradeWorkspace(beadsDir)
			var legacyErr *configfile.LegacyWorkspaceError
			if !errors.As(err, &legacyErr) || err.Error() != unownedDoltRootRefusal {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want %q", err, unownedDoltRootRefusal)
			}
			if warnings.Len() != 0 {
				t.Fatalf("guard warned while refusing an unowned root: %q", warnings.String())
			}
		})
	}
}
