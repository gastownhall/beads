package main

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// noDbConfig is a minimal struct for parsing config.yaml's no-db setting.
type noDbConfig struct {
	NoDb bool `yaml:"no-db"`
}

// isNoDbModeConfigured checks if no-db: true is set in config.yaml.
// Uses proper YAML parsing to avoid false matches in comments or nested keys.
func isNoDbModeConfigured(beadsDir string) bool {
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304
	if err != nil {
		return false
	}
	var cfg noDbConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return cfg.NoDb
}

// syncBranchConfig is a minimal struct for parsing config.yaml's sync-branch setting.
type syncBranchConfig struct {
	SyncBranch string `yaml:"sync-branch"`
}

// getLocalSyncBranch returns the sync branch from config.yaml or BEADS_SYNC_BRANCH env.
// Returns empty string if not configured.
func getLocalSyncBranch(beadsDir string) string {
	// Env var takes precedence
	if envBranch := os.Getenv("BEADS_SYNC_BRANCH"); envBranch != "" {
		return envBranch
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304
	if err != nil {
		return ""
	}
	var cfg syncBranchConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.SyncBranch
}
