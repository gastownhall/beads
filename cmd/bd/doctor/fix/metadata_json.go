package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
)

// FixMissingMetadataJSON detects and regenerates a missing metadata.json file.
// This is the most common failure scenario: the file is deleted but .beads/ exists.
// Regenerates with default config values (similar to bd init). (GH#2478)
func FixMissingMetadataJSON(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	sourceBeadsDir := filepath.Join(path, ".beads")

	if _, err := os.Stat(sourceBeadsDir); os.IsNotExist(err) {
		return fmt.Errorf("not a beads workspace: .beads directory not found at %s", path)
	}

	configPath := configfile.ConfigPath(sourceBeadsDir)

	if _, err := os.Stat(configPath); err == nil {
		return nil
	}

	info, err := resolveRuntimeInfoForRepo(path)
	var cfg *configfile.Config
	saveDir := sourceBeadsDir
	if err == nil {
		cfg, saveDir = metadataConfigForRepo(info)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	cfg = cloneConfig(cfg)
	cfg.Backend = configfile.BackendDolt
	if cfg.Database == "" || strings.HasSuffix(cfg.Database, ".db") || strings.HasSuffix(cfg.Database, ".sqlite") || strings.HasSuffix(cfg.Database, ".sqlite3") {
		cfg.Database = "dolt"
	}
	if cfg.DoltMode == "" {
		cfg.DoltMode = configfile.DoltModeServer
	}

	if err := cfg.Save(saveDir); err != nil {
		return fmt.Errorf("failed to regenerate metadata.json: %w", err)
	}

	fmt.Printf("  Regenerated metadata.json with default values\n")
	return nil
}
