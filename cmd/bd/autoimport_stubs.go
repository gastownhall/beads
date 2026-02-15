package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"gopkg.in/yaml.v3"
)

// checkAndAutoImport is a no-op now that JSONL sync has been removed.
// Dolt-native mode uses Dolt as the source of truth — no JSONL auto-import needed.
// Retained as a no-op to avoid touching all read command call sites.
func checkAndAutoImport(_ context.Context, _ *dolt.DoltStore) bool {
	return false
}

// checkGitForIssues is a no-op now that JSONL sync has been removed.
// Returns (0, "", "") to indicate no issues found in git.
func checkGitForIssues() (int, string, string) {
	return 0, "", ""
}

// importFromGit is a no-op now that JSONL sync has been removed.
func importFromGit(_ context.Context, _ string, _ *dolt.DoltStore, _, _ string) error {
	return nil
}

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
