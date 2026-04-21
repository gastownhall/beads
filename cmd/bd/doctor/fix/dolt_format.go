package fix

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/doltserver"
)

// DoltFormat fixes the "Dolt Format" warning by seeding the .bd-dolt-ok marker
// for pre-0.56 dolt databases that are otherwise functional.
//
// In server mode, the .beads/dolt/.dolt/ directory is vestigial from an older
// embedded Dolt setup. The data lives on the Dolt server. Seeding the marker
// tells future doctor checks that this database has been acknowledged.
func DoltFormat(path string) error {
	// resolveBeadsDir follows .beads/redirect to find the actual beads directory
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	doltDir := filepath.Join(beadsDir, "dolt")

	if !doltserver.IsPreV56DoltDir(doltDir) {
		return nil // Already OK or no .dolt/ directory
	}

	markerPath := filepath.Join(doltDir, ".bd-dolt-ok")
	if err := os.WriteFile(markerPath, []byte("ok\n"), 0600); err != nil {
		return fmt.Errorf("creating .bd-dolt-ok marker: %w", err)
	}

	fmt.Printf("  Seeded .bd-dolt-ok marker in %s\n", doltDir)
	return nil
}
