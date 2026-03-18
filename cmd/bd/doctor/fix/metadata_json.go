package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// FixMissingMetadataJSON detects and regenerates a missing metadata.json file.
// This is the most common failure scenario: the file is deleted but .beads/ exists.
// Regenerates with default config values (similar to bd init). (GH#2478)
func FixMissingMetadataJSON(path string) error {
	return regenerateMetadataJSON(path, false)
}

func repairMalformedMetadataJSON(path string) error {
	return regenerateMetadataJSON(path, true)
}

func regenerateMetadataJSON(path string, overwriteExisting bool) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	sourceBeadsDir := filepath.Join(path, ".beads")
	targetBeadsDir := beads.FollowRedirect(sourceBeadsDir)

	if _, err := os.Stat(sourceBeadsDir); os.IsNotExist(err) {
		return fmt.Errorf("not a beads workspace: .beads directory not found at %s", path)
	}

	configPath := configfile.ConfigPath(sourceBeadsDir)

	if _, err := os.Stat(configPath); err == nil {
		if !overwriteExisting {
			return nil
		}
	}

	info, err := resolveFixRuntimeInfoForRepo(path)
	var cfg *configfile.Config
	saveDir := sourceBeadsDir
	if err == nil {
		cfg, saveDir = metadataConfigForRepo(info)
	}
	if cfg == nil {
		cfg = recoveredMetadataConfig(targetBeadsDir)
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

	if overwriteExisting {
		fmt.Printf("  Rewrote malformed metadata.json with recovered values\n")
	} else {
		fmt.Printf("  Regenerated metadata.json with recovered values\n")
	}
	return nil
}

func recoveredMetadataConfig(beadsDir string) *configfile.Config {
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.Database = "dolt"
	cfg.DoltMode = configfile.DoltModeServer

	if port := doltserver.DefaultConfig(beadsDir).Port; port != 0 {
		cfg.DoltServerPort = port
	}
	if dbName := inferRecoveredDoltDatabase(beadsDir); dbName != "" {
		cfg.DoltDatabase = dbName
	}

	return cfg
}

func inferRecoveredDoltDatabase(beadsDir string) string {
	dataDir := doltserver.ResolveDoltDir(beadsDir)
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return ""
	}

	var candidates []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		if hasDoltDatabaseMarkers(filepath.Join(dataDir, name)) {
			candidates = append(candidates, name)
		}
	}

	if len(candidates) == 1 {
		return candidates[0]
	}

	return ""
}

func hasDoltDatabaseMarkers(path string) bool {
	for _, marker := range []string{"noms", ".dolt"} {
		info, err := os.Stat(filepath.Join(path, marker))
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}
