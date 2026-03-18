package fix

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
)

// ConfigValues fixes invalid configuration values in metadata.json.
// Currently handles: database field pointing to SQLite name when backend is Dolt.
func ConfigValues(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	info, err := resolveFixRuntimeInfoForRepo(path)
	if err != nil {
		if repairErr := repairMalformedMetadataJSON(path); repairErr != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		return nil
	}
	cfg, saveDir := metadataConfigForRepo(info)
	if cfg == nil {
		if repairErr := FixMissingMetadataJSON(path); repairErr != nil {
			return fmt.Errorf("no metadata.json found")
		}
		return nil
	}

	fixed := false

	if info != nil && info.SourceErr != nil && info.Runtime != nil && info.Runtime.SourceBeadsDir != "" && info.Runtime.SourceBeadsDir != info.Runtime.BeadsDir {
		fmt.Printf("  Regenerating redirected source metadata.json after parse failure: %v\n", info.SourceErr)
		fixed = true
	}

	// Fix database field: when backend is Dolt, database should be "dolt" not "beads.db"
	if cfg.GetBackend() == configfile.BackendDolt {
		if strings.HasSuffix(cfg.Database, ".db") || strings.HasSuffix(cfg.Database, ".sqlite") || strings.HasSuffix(cfg.Database, ".sqlite3") {
			fmt.Printf("  Updating database: %q → %q (Dolt backend uses directory)\n", cfg.Database, "dolt")
			cfg.Database = "dolt"
			fixed = true
		}
	}

	if !fixed {
		fmt.Println("  → No configuration issues to fix")
		return nil
	}

	if err := cfg.Save(saveDir); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}
