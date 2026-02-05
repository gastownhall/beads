package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"gopkg.in/yaml.v3"
)

// migrationHintCooldown is the minimum duration between migration hints (24 hours).
const migrationHintCooldown = 24 * time.Hour

// maybeShowMigrationHint prints a one-time stderr hint when the rig is on SQLite
// but prefer-dolt is configured. Rate-limited to once per 24 hours via a timestamp file.
// Non-blocking: returns immediately, commands continue normally with SQLite.
func maybeShowMigrationHint(beadsDir string) {
	// Don't show hints in JSON or quiet mode
	if jsonOutput || quietFlag {
		return
	}

	// Check if backend is already Dolt
	cfg, err := configfile.Load(beadsDir)
	if err == nil && cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		return
	}

	// Check if prefer-dolt is configured
	if !isPreferDoltConfiguredLocal(beadsDir) {
		return
	}

	// Rate-limit: check timestamp file
	tsFile := filepath.Join(beadsDir, ".migration-hint-ts")
	if data, err := os.ReadFile(tsFile); err == nil { // #nosec G304 - path from beadsDir
		if ts, err := strconv.ParseInt(string(data), 10, 64); err == nil {
			lastShown := time.Unix(ts, 0)
			if time.Since(lastShown) < migrationHintCooldown {
				return
			}
		}
	}

	// Show the hint
	fmt.Fprintf(os.Stderr, "Note: This rig uses SQLite but Dolt is preferred. Run 'bd migrate dolt' to upgrade.\n")

	// Write current timestamp (best-effort)
	tsStr := strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.WriteFile(tsFile, []byte(tsStr), 0600); err != nil {
		debug.Logf("warning: failed to write migration hint timestamp: %v", err)
	}
}

// isPreferDoltConfiguredLocal checks if prefer-dolt: true is set in config.yaml.
// This is a package-local copy following the isNoDbModeConfigured pattern in autoimport.go.
func isPreferDoltConfiguredLocal(beadsDir string) bool {
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 - config file path from beadsDir
	if err != nil {
		return false
	}

	var cfg struct {
		PreferDolt bool `yaml:"prefer-dolt"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		debug.Logf("warning: failed to parse config.yaml for prefer-dolt check: %v", err)
		return false
	}

	return cfg.PreferDolt
}
